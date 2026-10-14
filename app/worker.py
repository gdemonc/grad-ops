from __future__ import annotations

import time

from sqlalchemy.orm import Session

from app.audit import AuditLog
from app.contract import build_contract
from app.enums import ProposalStatus, TaskStatus
from app.executor import execute_approved, issue_approval_token
from app.grounding import apply_grounding, bind_claims_to_evidence
from app.investigation import InvestigationEngine
from app.llm.client import LLMClient
from app.models import ActionProposal, Approval, Evidence, Task, TaskJob, utcnow
from app.policy.controller import PolicyController
from app.policy.rules import SIDE_EFFECT_WHITELIST, scan_forbidden
from app.queue import TaskQueue
from app.tools.registry import ToolRegistry, build_registry
from app.verification import verify_recovery


class Worker:
    def __init__(
        self,
        session: Session,
        *,
        worker_id: str = "worker-1",
        llm: LLMClient | None = None,
        registry: ToolRegistry | None = None,
        dev_allowed_roots: tuple[str, ...] = (),
    ) -> None:
        self.session = session
        self.worker_id = worker_id
        self.llm = llm if llm is not None else LLMClient()
        self.registry = registry if registry is not None else build_registry()
        self.dev_allowed_roots = tuple(dev_allowed_roots)
        self.queue = TaskQueue(session)

    def run_once(self) -> bool:
        job = self.queue.claim_next(self.worker_id)
        if job is None:
            return False
        try:
            self._process(job)
        except Exception as exc:  # noqa: BLE001
            task = self.session.get(Task, job.task_id)
            AuditLog(self.session).append(
                task.trace_id if task is not None else "",
                "worker",
                "worker.error",
                f"任务 {job.task_id} 处理失败：{exc}",
                {"job_id": job.id, "task_id": job.task_id, "error": str(exc)},
            )
            self.queue.mark_failed(job.id, self.worker_id, str(exc))
            self.session.commit()
            return True
        self.queue.mark_succeeded(job.id, self.worker_id)
        self.session.commit()
        return True

    def run_forever(self, *, idle_seconds: float = 0.5, stop_event=None) -> None:
        while True:
            if stop_event is not None and stop_event.is_set():
                return
            if not self.run_once():
                time.sleep(idle_seconds)

    def _process(self, job: TaskJob) -> None:
        task = self.session.get(Task, job.task_id)
        if task is None:
            raise RuntimeError(f"任务不存在：{job.task_id}")
        if task.status == TaskStatus.EXECUTING.value:
            self._resume(task)
        elif task.status in (TaskStatus.QUEUED.value, TaskStatus.INVESTIGATING.value):
            self._investigate(task)

    def _investigate(self, task: Task) -> None:
        audit = AuditLog(self.session)
        trace_id = task.trace_id
        hits = scan_forbidden(task.request_text)
        if hits:
            task.status = TaskStatus.REJECTED.value
            task.summary = "请求命中禁止规则"
            task.updated_at = utcnow()
            audit.append(
                trace_id,
                "policy",
                "policy.request_rejected",
                f"请求命中禁止规则：{'、'.join(hits)}",
                {"hits": hits, "request_text": task.request_text},
            )
            self.session.flush()
            return
        if task.status == TaskStatus.QUEUED.value:
            task.status = TaskStatus.INVESTIGATING.value
            self.session.flush()
            audit.append(
                trace_id,
                "worker",
                "worker.investigating",
                f"开始调查任务 {task.id}",
                {"task_id": task.id},
            )
        result = InvestigationEngine(self.session, self.llm, self.registry).run(task)
        grounding = bind_claims_to_evidence(
            result.conclusions,
            result.evidence_ids,
            audit=audit,
            trace_id=trace_id,
        )
        conclusions = apply_grounding(result.conclusions, grounding)
        root_cause = str(conclusions.get("root_cause") or "")
        task.summary = root_cause
        self.session.flush()
        proposed = conclusions.get("proposed_action")
        if not isinstance(proposed, dict):
            task.status = TaskStatus.SEALED.value
            if not task.summary:
                task.summary = "只读调查完成"
            task.updated_at = utcnow()
            audit.append(
                trace_id,
                "worker",
                "worker.sealed",
                f"只读调查完成：{task.summary}",
                {"task_id": task.id, "root_cause": root_cause},
            )
            self.session.flush()
            return
        self._handle_proposal(task, proposed, audit)

    def _handle_proposal(self, task: Task, proposed: dict, audit: AuditLog) -> None:
        input_json = proposed.get("input")
        preconditions = proposed.get("preconditions")
        postconditions = proposed.get("postconditions")
        rollback = proposed.get("rollback")
        policy_ref = proposed.get("policy_ref")
        proposal = ActionProposal(
            task_id=task.id,
            tool_name=str(proposed.get("tool") or ""),
            input_json=input_json if isinstance(input_json, dict) else {},
            risk_level=str(proposed.get("risk_level") or "R0"),
            rationale=str(proposed.get("rationale") or ""),
            policy_ref=str(policy_ref) if isinstance(policy_ref, str) and policy_ref else None,
            preconditions_json=preconditions if isinstance(preconditions, list) else [],
            postconditions_json=postconditions if isinstance(postconditions, list) else [],
            rollback_json=rollback if isinstance(rollback, dict) else {},
            status=ProposalStatus.DRAFT.value,
        )
        self.session.add(proposal)
        self.session.flush()
        evidence = (
            self.session.query(Evidence)
            .filter_by(task_id=task.id)
            .order_by(Evidence.id.asc())
            .all()
        )
        decision = PolicyController().evaluate(
            proposal, evidence, dev_allowed_roots=self.dev_allowed_roots
        )
        if decision.decision == "DENY":
            self._deny(task, proposal, decision, audit)
        elif decision.decision == "ALLOW_AUTO":
            if proposal.tool_name in SIDE_EFFECT_WHITELIST:
                self._auto_execute(task, proposal, audit)
            else:
                self._seal_readonly(task, proposal, audit)
        else:
            self._park(task, proposal, decision, audit)

    def _deny(self, task: Task, proposal: ActionProposal, decision, audit: AuditLog) -> None:
        proposal.status = ProposalStatus.DENIED_BY_POLICY.value
        task.status = TaskStatus.REJECTED.value
        task.summary = f"策略拒绝:{decision.reason}"
        task.updated_at = utcnow()
        audit.append(
            task.trace_id,
            "policy",
            "policy.denied",
            f"策略拒绝：{decision.reason}",
            {
                "task_id": task.id,
                "proposal_id": proposal.id,
                "reason": decision.reason,
                "risk_level": decision.risk_level.value,
                "matched_rules": list(decision.matched_rules),
            },
        )
        self.session.flush()

    def _seal_readonly(self, task: Task, proposal: ActionProposal, audit: AuditLog) -> None:
        task.status = TaskStatus.SEALED.value
        task.summary = "只读调查完成"
        task.updated_at = utcnow()
        audit.append(
            task.trace_id,
            "worker",
            "worker.sealed",
            f"只读调查完成：{proposal.tool_name}",
            {"task_id": task.id, "proposal_id": proposal.id, "tool": proposal.tool_name},
        )
        self.session.flush()

    def _park(self, task: Task, proposal: ActionProposal, decision, audit: AuditLog) -> None:
        proposal.status = ProposalStatus.PENDING_APPROVAL.value
        approval = Approval(proposal_id=proposal.id, token_hash="", status="PENDING")
        self.session.add(approval)
        self.session.flush()
        task.status = TaskStatus.AWAITING_APPROVAL.value
        task.summary = f"等待人工审批:{decision.reason}"
        task.updated_at = utcnow()
        audit.append(
            task.trace_id,
            "approval",
            "approval.requested",
            f"提案 {proposal.id} 等待人工审批",
            {
                "task_id": task.id,
                "proposal_id": proposal.id,
                "approval_id": approval.id,
                "risk_level": proposal.risk_level,
                "reason": decision.reason,
            },
        )
        self.session.flush()

    def _auto_execute(self, task: Task, proposal: ActionProposal, audit: AuditLog) -> None:
        _raw_token, token_hash = issue_approval_token()
        approval = Approval(
            proposal_id=proposal.id,
            token_hash=token_hash,
            status="APPROVED",
            decided_by="policy-auto",
            decided_at=utcnow(),
            comment="策略自动放行",
        )
        self.session.add(approval)
        self.session.flush()
        audit.append(
            task.trace_id,
            "approval",
            "approval.auto_granted",
            f"策略自动批准提案 {proposal.id}",
            {"proposal_id": proposal.id, "approval_id": approval.id},
        )
        self._execute_and_verify(task, proposal, approval, audit)

    def _resume(self, task: Task) -> None:
        audit = AuditLog(self.session)
        proposal = (
            self.session.query(ActionProposal)
            .filter(
                ActionProposal.task_id == task.id,
                ActionProposal.status.in_(
                    [
                        ProposalStatus.PENDING_APPROVAL.value,
                        ProposalStatus.APPROVED.value,
                    ]
                ),
            )
            .order_by(ActionProposal.id.desc())
            .first()
        )
        if proposal is None:
            raise RuntimeError("恢复执行失败：未找到待执行的处置方案")
        approval = (
            self.session.query(Approval)
            .filter(
                Approval.proposal_id == proposal.id,
                Approval.status == "APPROVED",
            )
            .order_by(Approval.id.desc())
            .first()
        )
        if approval is None:
            raise RuntimeError("恢复执行失败：未找到已批准的审批记录")
        self._execute_and_verify(task, proposal, approval, audit)

    def _execute_and_verify(
        self, task: Task, proposal: ActionProposal, approval: Approval, audit: AuditLog
    ) -> None:
        trace_id = task.trace_id
        contract = build_contract(proposal)
        task.status = TaskStatus.EXECUTING.value
        task.updated_at = utcnow()
        self.session.flush()
        audit.append(
            trace_id,
            "worker",
            "worker.executing",
            f"开始执行提案 {proposal.id}：{proposal.tool_name}",
            {"proposal_id": proposal.id, "tool": proposal.tool_name},
        )
        execution = execute_approved(
            self.session,
            proposal,
            approval,
            contract,
            dev_allowed_roots=self.dev_allowed_roots,
        )
        task.status = TaskStatus.VERIFYING.value
        task.updated_at = utcnow()
        self.session.flush()
        audit.append(
            trace_id,
            "worker",
            "worker.verifying",
            f"独立验证提案 {proposal.id}",
            {"proposal_id": proposal.id, "tool": proposal.tool_name},
        )
        verification = verify_recovery(
            self.session,
            task,
            proposal,
            execution,
            dev_allowed_roots=self.dev_allowed_roots,
        )
        task.summary = "执行并独立验证通过" if verification.passed else "验证未通过，已回滚"
        task.updated_at = utcnow()
        self.session.flush()
