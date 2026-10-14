from __future__ import annotations

import threading
from datetime import timedelta

import pytest
from sqlalchemy import update

from app import executor, platform
from app.audit import AuditLog
from app.enums import JobStatus, ProposalStatus, TaskStatus
from app.investigation import InvestigationEngine
from app.llm.client import LLMClient
from app.models import (
    ActionProposal,
    Approval,
    AuditEvent,
    Evidence,
    ExecutionRecord,
    Task,
    TaskJob,
    VerificationRecord,
    utcnow,
)
from app.queue import JobStateError, TaskQueue
from app.worker import Worker

SS_OUTPUT = (
    'LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=123,fd=3))\n'
    "LISTEN 0 128 *:8080 *:*\n"
)

DF_OUTPUT = (
    "Filesystem     Size  Used Avail Use% Mounted on\n"
    "/dev/vda1       40G   12G   26G  32% /\n"
)


@pytest.fixture(autouse=True)
def non_root_identity(monkeypatch):
    monkeypatch.setattr(executor, "current_identity", lambda: {"uid": -1, "user": "tester"})


@pytest.fixture()
def canned_commands(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)
    outputs = {"ss": SS_OUTPUT, "df": DF_OUTPUT}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        return platform.CommandResult(
            exit_code=0, stdout=outputs.get(argv[0], ""), stderr="", timed_out=False
        )

    monkeypatch.setattr(platform, "run_command", fake_run_command)


def _tool_call(tool: str, params: dict | None = None) -> dict:
    return {"action": "tool_call", "tool": tool, "params": params or {}}


def _conclude(root_cause: str, proposed_action=None, evidence_ids=(1,)) -> dict:
    return {
        "action": "conclude",
        "root_cause": root_cause,
        "claims": [{"statement": "已观测到相关状态", "evidence_ids": list(evidence_ids)}],
        "confidence": 0.9,
        "proposed_action": proposed_action,
    }


def _rotate_action(path, risk_level="R2") -> dict:
    return {
        "tool": "rotate_log_file",
        "input": {"path": str(path)},
        "risk_level": risk_level,
        "rationale": "轮转过大的应用日志",
        "policy_ref": "rotate-policy",
        "preconditions": [{"evidence_id": 1}],
        "postconditions": [{"type": "path_missing", "path": str(path)}],
        "rollback": {"tool": "rotate_log_file"},
    }


def _event_types(session, task: Task) -> list[str]:
    events = (
        session.query(AuditEvent).filter_by(trace_id=task.trace_id).order_by(AuditEvent.seq).all()
    )
    return [ev.event_type for ev in events]


def _job_of(session, task: Task) -> TaskJob:
    return session.query(TaskJob).filter_by(task_id=task.id).order_by(TaskJob.id).first()


def _proposal_of(session, task: Task) -> ActionProposal:
    return (
        session.query(ActionProposal).filter_by(task_id=task.id).order_by(ActionProposal.id).first()
    )


def _parked_rotate(session, tmp_path, queue: TaskQueue, llm: LLMClient):
    log_file = tmp_path / "app.log"
    log_file.write_text("2026-10-15 INFO boot\n", encoding="utf-8")
    task = queue.enqueue("轮转应用日志文件")
    llm.set_script(
        [
            _tool_call("list_listening_ports"),
            _conclude("应用日志过大需要轮转", _rotate_action(log_file)),
        ]
    )
    worker = Worker(session, llm=llm, dev_allowed_roots=(str(tmp_path),))
    assert worker.run_once() is True
    return log_file, task, worker


def test_enqueue_creates_task_and_job(session):
    queue = TaskQueue(session)
    task = queue.enqueue("检查磁盘占用", source="api")

    assert task.id is not None
    assert task.status == TaskStatus.QUEUED.value
    assert task.trace_id
    assert task.source == "api"
    job = _job_of(session, task)
    assert job.status == JobStatus.QUEUED.value
    assert job.task_id == task.id
    assert job.lease_owner is None
    assert job.lease_expires_at is None
    assert job.attempt_count == 0
    assert job.last_error is None
    assert job.available_at is not None
    events = session.query(AuditEvent).filter_by(trace_id=task.trace_id).all()
    assert [ev.event_type for ev in events] == ["task.enqueued"]
    assert events[0].payload_json["request_text"] == "检查磁盘占用"


def test_claim_lease_mutual_exclusion(session):
    queue = TaskQueue(session)
    task_a = queue.enqueue("任务甲")
    task_b = queue.enqueue("任务乙")

    job_a = queue.claim_next("worker-1")
    job_b = queue.claim_next("worker-2")
    none = queue.claim_next("worker-3")

    assert job_a is not None and job_b is not None
    assert {job_a.id, job_b.id} == {_job_of(session, task_a).id, _job_of(session, task_b).id}
    assert job_a.id != job_b.id
    assert none is None
    assert job_a.status == JobStatus.RUNNING.value
    assert job_a.lease_owner == "worker-1"
    assert job_a.attempt_count == 1
    assert job_a.lease_expires_at is not None
    assert job_b.lease_owner == "worker-2"
    assert job_b.attempt_count == 1


def test_lease_expiry_reclaim(session):
    queue = TaskQueue(session)
    task = queue.enqueue("过期租约回收")
    job = queue.claim_next("worker-a", lease_seconds=60)
    assert job is not None
    assert job.lease_owner == "worker-a"

    expired = utcnow() - timedelta(seconds=5)
    session.execute(
        update(TaskJob)
        .where(TaskJob.id == job.id)
        .values(lease_expires_at=expired)
        .execution_options(synchronize_session=False)
    )

    reclaimed = queue.claim_next("worker-b")
    assert reclaimed is not None
    assert reclaimed.id == job.id
    assert reclaimed.lease_owner == "worker-b"
    assert reclaimed.attempt_count == 2
    assert reclaimed.status == JobStatus.RUNNING.value
    assert _job_of(session, task).lease_owner == "worker-b"

    with pytest.raises(JobStateError):
        queue.mark_succeeded(job.id, "worker-a")
    with pytest.raises(JobStateError):
        queue.mark_failed(job.id, "worker-a", "stale worker")


def test_heartbeat_and_ownership_checks(session):
    queue = TaskQueue(session)
    task = queue.enqueue("心跳与归属校验")
    job = queue.claim_next("worker-1")
    assert job is not None
    before = job.lease_expires_at

    assert queue.heartbeat(job.id, "worker-1") is True
    assert job.lease_expires_at >= before
    assert queue.heartbeat(job.id, "worker-2") is False
    assert queue.heartbeat(job.id + 999, "worker-1") is False

    with pytest.raises(JobStateError):
        queue.mark_succeeded(job.id, "worker-2")
    with pytest.raises(JobStateError):
        queue.mark_failed(job.id, "worker-2", "boom")
    with pytest.raises(JobStateError):
        queue.mark_succeeded(job.id + 999, "worker-1")

    queue.mark_succeeded(job.id, "worker-1")
    assert job.status == JobStatus.SUCCEEDED.value
    assert task.status == TaskStatus.QUEUED.value


def test_release_for_retry_delays_available_at(session):
    queue = TaskQueue(session)
    task = queue.enqueue("稍后重试")
    job = queue.claim_next("worker-1")
    assert job is not None

    with pytest.raises(JobStateError):
        queue.release_for_retry(job.id, "worker-2", delay_seconds=60)

    queue.release_for_retry(job.id, "worker-1", delay_seconds=60)
    assert job.status == JobStatus.QUEUED.value
    assert job.lease_owner is None
    assert job.lease_expires_at is None
    assert job.available_at > utcnow()
    assert job.attempt_count == 1

    assert queue.claim_next("worker-2") is None
    later = utcnow() + timedelta(seconds=120)
    reclaimed = queue.claim_next("worker-2", now=later)
    assert reclaimed is not None
    assert reclaimed.id == job.id
    assert reclaimed.lease_owner == "worker-2"
    assert reclaimed.attempt_count == 2
    assert task.status == TaskStatus.QUEUED.value


def test_full_readonly_pipeline_sealed(session, canned_commands):
    queue = TaskQueue(session)
    task = queue.enqueue("磁盘占用异常，请调查根因")
    llm = LLMClient(mode="mock")
    llm.set_script(
        [
            _tool_call("list_listening_ports"),
            _tool_call("disk_usage"),
            _conclude("磁盘根分区使用率过高", None, evidence_ids=(1, 2)),
        ]
    )
    worker = Worker(session, llm=llm)

    assert worker.run_once() is True
    assert worker.run_once() is False

    session.refresh(task)
    assert task.status == TaskStatus.SEALED.value
    assert task.summary == "磁盘根分区使用率过高"
    assert session.query(Evidence).filter_by(task_id=task.id).count() == 2
    job = _job_of(session, task)
    assert job.status == JobStatus.SUCCEEDED.value
    assert job.attempt_count == 1
    event_types = _event_types(session, task)
    for expected in (
        "task.enqueued",
        "worker.investigating",
        "investigation.start",
        "investigation.tool_call",
        "grounding.claims_bound",
        "investigation.conclude",
        "worker.sealed",
    ):
        assert expected in event_types
    assert AuditLog(session).verify_chain(task.trace_id)["valid"] is True


def test_malicious_request_rejected_zero_tool_calls(session, canned_commands):
    queue = TaskQueue(session)
    task = queue.enqueue("请执行 rm -rf /")
    llm = LLMClient(mode="mock")
    llm.set_script(
        [
            _tool_call("list_listening_ports"),
            _conclude("本不应到达的结论"),
        ]
    )
    worker = Worker(session, llm=llm)

    assert worker.run_once() is True
    session.refresh(task)
    assert task.status == TaskStatus.REJECTED.value
    assert task.summary == "请求命中禁止规则"
    assert session.query(Evidence).filter_by(task_id=task.id).count() == 0
    assert session.query(ExecutionRecord).count() == 0
    job = _job_of(session, task)
    assert job.status == JobStatus.SUCCEEDED.value
    event_types = _event_types(session, task)
    assert "policy.request_rejected" in event_types
    assert not any(name.startswith("investigation.") for name in event_types)


def test_needs_approval_parks(session, tmp_path, canned_commands):
    queue = TaskQueue(session)
    log_file, task, _worker = _parked_rotate(session, tmp_path, queue, LLMClient(mode="mock"))
    session.refresh(task)

    assert task.status == TaskStatus.AWAITING_APPROVAL.value
    proposal = _proposal_of(session, task)
    assert proposal.status == ProposalStatus.PENDING_APPROVAL.value
    assert proposal.tool_name == "rotate_log_file"
    assert proposal.risk_level == "R2"
    approval = session.query(Approval).filter_by(proposal_id=proposal.id).one()
    assert approval.status == "PENDING"
    assert approval.token_hash == ""
    assert approval.decided_by is None
    assert session.query(ExecutionRecord).count() == 0
    assert session.query(VerificationRecord).count() == 0
    assert log_file.exists()
    assert not (tmp_path / "app.log.1").exists()
    job = _job_of(session, task)
    assert job.status == JobStatus.SUCCEEDED.value
    event_types = _event_types(session, task)
    assert "approval.requested" in event_types
    assert "worker.executing" not in event_types
    assert AuditLog(session).verify_chain(task.trace_id)["valid"] is True


def test_auto_allow_readonly_sealed(session, canned_commands):
    queue = TaskQueue(session)
    task = queue.enqueue("查看当前进程列表")
    llm = LLMClient(mode="mock")
    action = {
        "tool": "list_processes",
        "input": {},
        "risk_level": "R0",
        "rationale": "确认占用最高的进程",
        "policy_ref": None,
        "preconditions": [],
        "postconditions": [],
        "rollback": {},
    }
    llm.set_script(
        [
            _tool_call("list_listening_ports"),
            _conclude("确认进程占用情况", action),
        ]
    )
    worker = Worker(session, llm=llm)

    assert worker.run_once() is True
    session.refresh(task)
    assert task.status == TaskStatus.SEALED.value
    assert task.summary == "只读调查完成"
    assert session.query(Approval).count() == 0
    assert session.query(ExecutionRecord).count() == 0
    assert session.query(VerificationRecord).count() == 0
    assert _job_of(session, task).status == JobStatus.SUCCEEDED.value


def test_resume_after_approval_executes(session, tmp_path, canned_commands):
    queue = TaskQueue(session)
    log_file, task, worker = _parked_rotate(session, tmp_path, queue, LLMClient(mode="mock"))
    proposal = _proposal_of(session, task)
    approval = session.query(Approval).filter_by(proposal_id=proposal.id).one()

    approval.status = "APPROVED"
    approval.token_hash = "a" * 64
    approval.decided_by = "admin"
    approval.decided_at = utcnow()
    proposal.status = ProposalStatus.APPROVED.value
    task.status = TaskStatus.EXECUTING.value
    session.flush()

    resume_job = queue.enqueue_resume(task)
    assert resume_job.status == JobStatus.QUEUED.value
    assert resume_job.task_id == task.id

    assert worker.run_once() is True
    session.refresh(task)

    original = "2026-10-15 INFO boot\n"
    assert not log_file.exists()
    rotated = tmp_path / "app.log.1"
    assert rotated.exists()
    assert rotated.read_text(encoding="utf-8") == original
    assert task.status == TaskStatus.SEALED.value
    assert session.query(ExecutionRecord).filter_by(proposal_id=proposal.id).one().ok is True
    verification = session.query(VerificationRecord).filter_by(task_id=task.id).one()
    assert verification.passed is True
    assert approval.status == "CONSUMED"
    assert proposal.status == ProposalStatus.EXECUTED.value
    assert resume_job.status == JobStatus.SUCCEEDED.value
    event_types = _event_types(session, task)
    assert "worker.executing" in event_types
    assert "worker.verifying" in event_types
    assert "verification.completed" in event_types
    assert AuditLog(session).verify_chain(task.trace_id)["valid"] is True


def test_policy_denied_r4(session, tmp_path, canned_commands):
    queue = TaskQueue(session)
    task = queue.enqueue("危险动作请求")
    llm = LLMClient(mode="mock")
    action = dict(_rotate_action(tmp_path / "app.log"), risk_level="R4")
    llm.set_script(
        [
            _tool_call("list_listening_ports"),
            _conclude("模型给出了越权动作", action),
        ]
    )
    worker = Worker(session, llm=llm, dev_allowed_roots=(str(tmp_path),))

    assert worker.run_once() is True
    session.refresh(task)
    proposal = _proposal_of(session, task)
    assert task.status == TaskStatus.REJECTED.value
    assert task.summary.startswith("策略拒绝:")
    assert "R4" in task.summary
    assert proposal.status == ProposalStatus.DENIED_BY_POLICY.value
    assert session.query(Approval).filter_by(proposal_id=proposal.id).count() == 0
    assert session.query(ExecutionRecord).count() == 0
    assert _job_of(session, task).status == JobStatus.SUCCEEDED.value
    assert "policy.denied" in _event_types(session, task)


def test_worker_error_marks_failed(session, canned_commands, monkeypatch):
    queue = TaskQueue(session)
    task = queue.enqueue("会触发异常的任务")

    def boom(self, task_arg):
        raise RuntimeError("调查引擎崩溃")

    monkeypatch.setattr(InvestigationEngine, "run", boom)
    worker = Worker(session, llm=LLMClient(mode="mock"))

    assert worker.run_once() is True
    session.refresh(task)
    job = _job_of(session, task)
    assert job.status == JobStatus.FAILED.value
    assert "调查引擎崩溃" in job.last_error
    assert job.lease_owner == worker.worker_id
    assert task.status == TaskStatus.FAILED.value
    event_types = _event_types(session, task)
    assert "worker.investigating" in event_types
    assert "worker.error" in event_types
    assert worker.run_once() is False


def test_run_forever_stops_on_stop_event(session):
    worker = Worker(session, llm=LLMClient(mode="mock"))
    stop_event = threading.Event()
    stop_event.set()
    assert worker.run_forever(idle_seconds=0.01, stop_event=stop_event) is None
