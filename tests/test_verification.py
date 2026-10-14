from __future__ import annotations

import pytest

from app.enums import TaskStatus
from app.models import ActionProposal, AuditEvent, ExecutionRecord, Task, VerificationRecord
from app.verification import VerificationIndependenceError, verify_recovery


def _persist(
    session,
    *,
    input_json,
    postconditions,
    tool_name="rotate_log_file",
    execution_ok=True,
):
    task = Task(request_text="轮转日志并独立验证恢复结果")
    session.add(task)
    session.flush()
    proposal = ActionProposal(
        task_id=task.id,
        tool_name=tool_name,
        input_json=input_json,
        risk_level="R1",
        rationale="执行后由验证器独立回读状态",
        postconditions_json=postconditions,
    )
    session.add(proposal)
    session.flush()
    execution = ExecutionRecord(
        proposal_id=proposal.id,
        ok=execution_ok,
        exit_code=0 if execution_ok else 1,
    )
    session.add(execution)
    session.flush()
    return task, proposal, execution


def _events(session, task, event_type):
    return (
        session.query(AuditEvent)
        .filter_by(trace_id=task.trace_id, event_type=event_type)
        .order_by(AuditEvent.seq)
        .all()
    )


def test_path_exists_postcondition_passes(session, tmp_path):
    target = tmp_path / "app.log"
    target.write_text("2026-10-14 INFO ready\n", encoding="utf-8")
    task, proposal, execution = _persist(
        session,
        input_json={"path": str(target)},
        postconditions=[{"type": "path_exists", "path": str(target), "min_size": 1}],
    )
    record = verify_recovery(
        session, task, proposal, execution, dev_allowed_roots=(str(tmp_path),)
    )
    assert record.passed is True
    assert record.note == "1/1 项验证通过"
    assert record.checks_json[0]["name"] == "path_exists"
    assert record.checks_json[0]["ok"] is True
    assert record.checks_json[0]["tool_call_names"] == []
    assert task.status == TaskStatus.SEALED.value
    persisted = session.query(VerificationRecord).filter_by(task_id=task.id).one()
    assert persisted.id == record.id
    assert persisted.proposal_id == proposal.id
    completed = _events(session, task, "verification.completed")
    assert len(completed) == 1
    assert completed[0].payload_json["passed"] is True
    assert completed[0].payload_json["count"] == 1
    assert not _events(session, task, "verification.rollback")


def test_path_exists_fails_triggers_rollback(session, tmp_path):
    target = tmp_path / "app.log"
    rotated = tmp_path / "app.log.1"
    rotated.write_text("rotated content\n", encoding="utf-8")
    task, proposal, execution = _persist(
        session,
        tool_name="rotate_log_file",
        input_json={"path": str(target)},
        postconditions=[{"type": "path_exists", "path": str(target)}],
    )
    record = verify_recovery(session, task, proposal, execution)
    assert record.passed is False
    assert record.checks_json[0]["ok"] is False
    assert record.checks_json[0]["detail"].startswith("路径不存在")
    assert task.status == TaskStatus.ROLLED_BACK.value
    assert target.exists()
    assert target.read_text(encoding="utf-8") == "rotated content\n"
    assert not rotated.exists()
    completed = _events(session, task, "verification.completed")
    assert completed[0].payload_json["passed"] is False
    rollbacks = _events(session, task, "verification.rollback")
    assert len(rollbacks) == 1
    assert rollbacks[0].payload_json["restored"] is True
    assert rollbacks[0].payload_json["tool"] == "rotate_log_file"


def test_execution_ok_postcondition(session, tmp_path):
    target = tmp_path / "app.log"
    target.write_text("content\n", encoding="utf-8")
    task, proposal, execution = _persist(
        session,
        tool_name="backup_log_file",
        input_json={"path": str(target)},
        postconditions=[{"type": "execution_ok"}],
        execution_ok=False,
    )
    record = verify_recovery(session, task, proposal, execution)
    assert record.passed is False
    assert record.checks_json[0]["name"] == "execution_ok"
    assert record.checks_json[0]["ok"] is False
    assert task.status == TaskStatus.ROLLED_BACK.value
    assert session.query(VerificationRecord).filter_by(task_id=task.id).one().passed is False


def test_independence_error(session, tmp_path):
    target = tmp_path / "app.log"
    target.write_text("content\n", encoding="utf-8")
    task, proposal, execution = _persist(
        session,
        input_json={"path": str(target)},
        postconditions=[{"type": "path_exists", "path": str(target)}],
    )
    with pytest.raises(VerificationIndependenceError) as excinfo:
        verify_recovery(
            session, task, proposal, execution, investigation_evidence_ids=[1, 2]
        )
    assert "验证不得复用调查阶段证据" in str(excinfo.value)
    assert session.query(VerificationRecord).count() == 0
    assert not _events(session, task, "verification.completed")
    assert not _events(session, task, "verification.rollback")
    assert task.status == TaskStatus.QUEUED.value


def test_empty_postconditions_uses_execution_ok(session, tmp_path):
    target = tmp_path / "app.log"
    target.write_text("content\n", encoding="utf-8")
    task, proposal, execution = _persist(
        session,
        tool_name="backup_log_file",
        input_json={"path": str(target)},
        postconditions=[],
        execution_ok=True,
    )
    record = verify_recovery(session, task, proposal, execution)
    assert record.passed is True
    assert record.checks_json == []
    assert record.note == "无验证项，依据执行结果"
    assert task.status == TaskStatus.SEALED.value
    assert len(_events(session, task, "verification.completed")) == 1


def test_unknown_postcondition_type_fails(session, tmp_path):
    target = tmp_path / "app.log"
    target.write_text("content\n", encoding="utf-8")
    task, proposal, execution = _persist(
        session,
        input_json={"path": str(target)},
        postconditions=[{"type": "magic"}],
    )
    record = verify_recovery(session, task, proposal, execution)
    assert record.passed is False
    assert record.checks_json[0]["ok"] is False
    assert "未知" in record.checks_json[0]["detail"]
    assert task.status == TaskStatus.ROLLED_BACK.value


def test_backup_rollback_is_noop(session, tmp_path):
    missing = tmp_path / "ghost.log"
    task, proposal, execution = _persist(
        session,
        tool_name="backup_log_file",
        input_json={"path": str(tmp_path / "app.log")},
        postconditions=[{"type": "path_exists", "path": str(missing)}],
    )
    record = verify_recovery(session, task, proposal, execution)
    assert record.passed is False
    assert task.status == TaskStatus.ROLLED_BACK.value
    rollbacks = _events(session, task, "verification.rollback")
    assert len(rollbacks) == 1
    assert rollbacks[0].payload_json["tool"] == "backup_log_file"
    assert rollbacks[0].payload_json["restored"] is False
    assert "无需回滚" in rollbacks[0].payload_json["detail"]
    assert not missing.exists()
