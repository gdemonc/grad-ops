from __future__ import annotations

import getpass
import hashlib
import json
import os
import secrets
import time

from sqlalchemy.orm import Session

from app.audit import AuditLog
from app.config import settings
from app.contract import ContractTampered, assert_contract_matches
from app.enums import ProposalStatus
from app.models import ActionProposal, Approval, ExecutionRecord, Task
from app.policy.boundary import BoundaryViolation, authorize_path
from app.policy.rules import SIDE_EFFECT_WHITELIST, scan_forbidden
from app.tools.base import ToolError
from app.tools.registry import build_registry

_PATH_KEYS = ("path", "restore_target", "artifact_path")


class ExecutionDenied(PermissionError):
    def __init__(self, message: str, *, context: dict | None = None) -> None:
        super().__init__(message)
        self.context = dict(context) if context else {}


def _deny(reason: str, stage: str) -> ExecutionDenied:
    return ExecutionDenied(reason, context={"reason": reason, "stage": stage})


def current_identity() -> dict:
    geteuid = getattr(os, "geteuid", None)
    uid = geteuid() if geteuid is not None else -1
    return {"uid": uid, "user": getpass.getuser()}


def issue_approval_token() -> tuple[str, str]:
    raw_token = secrets.token_urlsafe(32)
    return raw_token, hashlib.sha256(raw_token.encode("utf-8")).hexdigest()


def verify_approval_token(approval: Approval, raw_token: str) -> bool:
    return hashlib.sha256(raw_token.encode("utf-8")).hexdigest() == approval.token_hash


def _gate_contract(proposal: ActionProposal, contract: dict) -> None:
    try:
        assert_contract_matches(contract, proposal)
    except ContractTampered as exc:
        raise _deny("契约不一致", "contract") from exc


def _gate_approval(session: Session, approval: Approval) -> None:
    if approval.status == "CONSUMED":
        raise _deny("审批令牌已使用", "approval_replay")
    if approval.status != "APPROVED":
        raise _deny("审批未通过", "approval")
    approval.status = "CONSUMED"
    session.flush()


def _gate_identity() -> None:
    identity = current_identity()
    if identity["uid"] == 0 and not settings.allow_root_executor:
        raise _deny("副作用工具禁止以 root 身份运行", "identity")


def _gate_whitelist(proposal: ActionProposal) -> None:
    if proposal.tool_name not in SIDE_EFFECT_WHITELIST:
        raise _deny(f"工具不在副作用白名单：{proposal.tool_name}", "whitelist")


def _gate_forbidden(proposal: ActionProposal) -> None:
    text = json.dumps(proposal.input_json, ensure_ascii=False) + proposal.rationale
    hits = scan_forbidden(text)
    if hits:
        raise _deny(f"检测到禁止模式：{'、'.join(hits)}", "forbidden")


def _gate_boundary(proposal: ActionProposal, dev_allowed_roots: tuple[str, ...]) -> None:
    input_json = proposal.input_json
    for key in _PATH_KEYS:
        value = input_json.get(key) if isinstance(input_json, dict) else None
        if isinstance(value, str) and value:
            try:
                authorize_path(value, dev_allowed_roots=dev_allowed_roots)
            except BoundaryViolation as exc:
                raise _deny(str(exc), "boundary") from exc


def _lookup_tool(proposal: ActionProposal):
    try:
        return build_registry().get(proposal.tool_name)
    except ToolError as exc:
        raise _deny(f"未注册的副作用工具：{proposal.tool_name}", "whitelist") from exc


def _trace_id(session: Session, proposal: ActionProposal) -> str:
    task = session.get(Task, proposal.task_id)
    return task.trace_id if task is not None else ""


def _audit_denied(session: Session, proposal: ActionProposal, denied: ExecutionDenied) -> None:
    reason = str(denied.context.get("reason") or denied)
    stage = str(denied.context.get("stage") or "")
    AuditLog(session).append(
        _trace_id(session, proposal),
        "executor",
        "executor.denied",
        f"执行被拒绝：{reason}",
        {"reason": reason, "stage": stage},
    )


def _run_handler(
    session: Session, proposal: ActionProposal, spec
) -> ExecutionRecord:
    started = time.perf_counter()
    try:
        result = spec.handler(proposal.input_json)
    except Exception as exc:  # noqa: BLE001
        result = {"ok": False, "summary": f"工具执行异常：{exc}", "error": str(exc)}
    duration_ms = int((time.perf_counter() - started) * 1000)
    if not isinstance(result, dict):
        result = {
            "ok": False,
            "summary": "工具返回非字典结果",
            "error": "tool returned non-dict result",
        }
    ok = bool(result.get("ok"))
    summary = str(result.get("summary") or "")
    error = None if ok else str(result.get("error") or summary or "执行失败")
    record = ExecutionRecord(
        proposal_id=proposal.id,
        ok=ok,
        exit_code=0 if ok else 1,
        stdout=summary,
        stderr=error,
        duration_ms=duration_ms,
    )
    session.add(record)
    session.flush()
    AuditLog(session).append(
        _trace_id(session, proposal),
        "executor",
        "executor.executed",
        f"工具 {proposal.tool_name} 执行完成 ok={ok}",
        {"ok": ok, "tool": proposal.tool_name},
    )
    proposal.status = ProposalStatus.EXECUTED.value if ok else ProposalStatus.FAILED.value
    session.flush()
    return record


def execute_approved(
    session: Session,
    proposal: ActionProposal,
    approval: Approval,
    contract: dict,
    *,
    dev_allowed_roots: tuple[str, ...] = (),
) -> ExecutionRecord:
    try:
        _gate_contract(proposal, contract)
        _gate_approval(session, approval)
        _gate_identity()
        _gate_whitelist(proposal)
        _gate_forbidden(proposal)
        _gate_boundary(proposal, dev_allowed_roots)
        spec = _lookup_tool(proposal)
    except ExecutionDenied as exc:
        _audit_denied(session, proposal, exc)
        raise
    return _run_handler(session, proposal, spec)
