from __future__ import annotations

import pytest

from app import executor
from app.config import settings
from app.contract import build_contract
from app.enums import ProposalStatus, RiskLevel
from app.executor import (
    ExecutionDenied,
    execute_approved,
    issue_approval_token,
    verify_approval_token,
)
from app.models import ActionProposal, Approval, AuditEvent, ExecutionRecord, Task
from app.tools import ToolError, ToolRegistry, build_registry
from app.tools.side_effect import (
    SIDE_EFFECT_SPECS,
    backup_log_file,
    register_side_effect_tools,
    rotate_log_file,
)


@pytest.fixture(autouse=True)
def non_root_identity(monkeypatch):
    monkeypatch.setattr(executor, "current_identity", lambda: {"uid": -1, "user": "tester"})


@pytest.fixture()
def tmp_log(tmp_path):
    path = tmp_path / "app.log"
    path.write_text("2026-10-14 INFO boot\n2026-10-14 INFO ready\n", encoding="utf-8")
    return path


def _persist(
    session,
    *,
    input_json,
    tool_name="backup_log_file",
    rationale="备份日志",
    approval_status="APPROVED",
):
    task = Task(request_text="清理并备份应用日志")
    session.add(task)
    session.flush()
    proposal = ActionProposal(
        task_id=task.id,
        tool_name=tool_name,
        input_json=input_json,
        risk_level="R2",
        rationale=rationale,
        policy_ref="backup-policy",
    )
    session.add(proposal)
    session.flush()
    approval = Approval(proposal_id=proposal.id, token_hash="x", status=approval_status)
    session.add(approval)
    session.flush()
    contract = build_contract(proposal)
    return task, proposal, approval, contract


def _events(session, task):
    return (
        session.query(AuditEvent)
        .filter_by(trace_id=task.trace_id)
        .order_by(AuditEvent.seq)
        .all()
    )


def test_side_effect_specs_registered():
    assert {spec.name for spec in SIDE_EFFECT_SPECS} == {"backup_log_file", "rotate_log_file"}
    for spec in SIDE_EFFECT_SPECS:
        assert spec.risk_level == RiskLevel.R1
        assert spec.has_side_effect is True
        assert spec.parameters["properties"]["path"]["type"] == "string"
        assert spec.parameters["required"] == ["path"]
    registry = build_registry()
    assert {spec.name for spec in registry.side_effect_only()} == {
        "backup_log_file",
        "rotate_log_file",
    }


def test_register_side_effect_tools():
    registry = ToolRegistry()
    register_side_effect_tools(registry)
    assert set(registry.names()) == {"backup_log_file", "rotate_log_file"}
    with pytest.raises(ToolError):
        register_side_effect_tools(registry)


def test_backup_log_file_missing_source(tmp_path):
    result = backup_log_file({"path": str(tmp_path / "ghost.log")})
    assert result == {"ok": False, "summary": "源文件不存在", "data": {}}
    rotated = rotate_log_file({"path": str(tmp_path / "ghost.log")})
    assert rotated["ok"] is False


def test_happy_path_backup(session, tmp_log, tmp_path):
    task, proposal, approval, contract = _persist(session, input_json={"path": str(tmp_log)})
    original = tmp_log.read_text(encoding="utf-8")
    record = execute_approved(
        session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
    )
    assert isinstance(record, ExecutionRecord)
    assert record.ok is True
    assert record.exit_code == 0
    assert record.stdout.startswith("已备份到")
    assert record.stderr is None
    assert record.duration_ms is not None
    backups = sorted(tmp_path.glob("app.log.bak.*"))
    assert len(backups) == 1
    assert backups[0].read_text(encoding="utf-8") == original
    assert approval.status == "CONSUMED"
    assert proposal.status == ProposalStatus.EXECUTED.value
    executed = [ev for ev in _events(session, task) if ev.event_type == "executor.executed"]
    assert len(executed) == 1
    assert executed[0].payload_json["ok"] is True
    assert executed[0].payload_json["tool"] == "backup_log_file"


def test_reject_without_approval(session, tmp_log, tmp_path):
    task, proposal, approval, contract = _persist(
        session, input_json={"path": str(tmp_log)}, approval_status="PENDING"
    )
    with pytest.raises(ExecutionDenied) as excinfo:
        execute_approved(
            session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
        )
    assert excinfo.value.context["stage"] == "approval"
    assert excinfo.value.context["reason"] == "审批未通过"
    assert not list(tmp_path.glob("app.log.bak.*"))
    assert approval.status == "PENDING"
    assert proposal.status != ProposalStatus.EXECUTED.value
    denied = [ev for ev in _events(session, task) if ev.event_type == "executor.denied"]
    assert len(denied) == 1
    assert denied[0].payload_json["stage"] == "approval"
    assert denied[0].payload_json["reason"] == "审批未通过"


def test_token_single_use(session, tmp_log, tmp_path):
    _task, proposal, approval, contract = _persist(
        session, input_json={"path": str(tmp_log)}
    )
    roots = (str(tmp_path),)
    execute_approved(session, proposal, approval, contract, dev_allowed_roots=roots)
    assert approval.status == "CONSUMED"
    with pytest.raises(ExecutionDenied) as excinfo:
        execute_approved(
            session, proposal, approval, build_contract(proposal), dev_allowed_roots=roots
        )
    assert "已使用" in str(excinfo.value)
    assert excinfo.value.context["stage"] == "approval_replay"
    assert approval.status == "CONSUMED"


def test_contract_mismatch_denied(session, tmp_log, tmp_path):
    task, proposal, approval, contract = _persist(session, input_json={"path": str(tmp_log)})
    contract["input"]["path"] = str(tmp_path / "tampered.log")
    with pytest.raises(ExecutionDenied) as excinfo:
        execute_approved(
            session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
        )
    assert excinfo.value.context["stage"] == "contract"
    assert excinfo.value.context["reason"] == "契约不一致"
    assert approval.status == "APPROVED"
    assert not list(tmp_path.glob("app.log.bak.*"))
    assert any(ev.event_type == "executor.denied" for ev in _events(session, task))


def test_root_denied(session, tmp_log, tmp_path, monkeypatch):
    monkeypatch.setattr(executor, "current_identity", lambda: {"uid": 0, "user": "root"})
    monkeypatch.setattr(settings, "allow_root_executor", False)
    _task, proposal, approval, contract = _persist(session, input_json={"path": str(tmp_log)})
    with pytest.raises(ExecutionDenied) as excinfo:
        execute_approved(
            session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
        )
    assert excinfo.value.context["stage"] == "identity"
    assert "root" in str(excinfo.value)
    assert not list(tmp_path.glob("app.log.bak.*"))


def test_unknown_tool_denied(session, tmp_path):
    task, proposal, approval, contract = _persist(
        session, tool_name="drop_database", input_json={"path": str(tmp_path / "app.log")}
    )
    with pytest.raises(ExecutionDenied) as excinfo:
        execute_approved(
            session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
        )
    assert excinfo.value.context["stage"] == "whitelist"
    assert approval.status == "CONSUMED"
    denied = [ev for ev in _events(session, task) if ev.event_type == "executor.denied"]
    assert denied[0].payload_json["stage"] == "whitelist"


def test_forbidden_pattern_denied(session, tmp_log, tmp_path):
    _task, proposal, approval, contract = _persist(
        session, input_json={"path": str(tmp_log)}, rationale="rm -rf /"
    )
    with pytest.raises(ExecutionDenied) as excinfo:
        execute_approved(
            session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
        )
    assert excinfo.value.context["stage"] == "forbidden"
    assert not list(tmp_path.glob("app.log.bak.*"))


def test_path_escape_denied(session, tmp_path):
    escape = str(tmp_path / ".." / "escape.log")
    _task, proposal, approval, contract = _persist(session, input_json={"path": escape})
    with pytest.raises(ExecutionDenied) as excinfo:
        execute_approved(
            session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
        )
    assert excinfo.value.context["stage"] == "boundary"
    assert not list(tmp_path.glob("*.bak.*"))
    assert not list(tmp_path.glob("*.1"))
    assert not (tmp_path.parent / "escape.log").exists()


def test_rotate_happy_path(session, tmp_log, tmp_path):
    original = tmp_log.read_text(encoding="utf-8")
    (tmp_path / "app.log.1").write_text("stale\n", encoding="utf-8")
    task, proposal, approval, contract = _persist(
        session, tool_name="rotate_log_file", input_json={"path": str(tmp_log)}
    )
    record = execute_approved(
        session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
    )
    assert record.ok is True
    assert record.exit_code == 0
    assert not tmp_log.exists()
    rotated = tmp_path / "app.log.1"
    assert rotated.exists()
    assert rotated.read_text(encoding="utf-8") == original
    assert approval.status == "CONSUMED"
    assert proposal.status == ProposalStatus.EXECUTED.value
    assert "executor.executed" in [ev.event_type for ev in _events(session, task)]


def test_rotate_missing_source(session, tmp_path):
    missing = tmp_path / "missing.log"
    task, proposal, approval, contract = _persist(
        session, tool_name="rotate_log_file", input_json={"path": str(missing)}
    )
    record = execute_approved(
        session, proposal, approval, contract, dev_allowed_roots=(str(tmp_path),)
    )
    assert record.ok is False
    assert record.exit_code == 1
    assert record.stdout == "源文件不存在"
    assert not (tmp_path / "missing.log.1").exists()
    assert proposal.status == ProposalStatus.FAILED.value
    executed = [ev for ev in _events(session, task) if ev.event_type == "executor.executed"]
    assert executed[0].payload_json["ok"] is False


def test_issue_and_verify_token():
    raw_token, token_hash = issue_approval_token()
    approval = Approval(proposal_id=1, token_hash=token_hash, status="APPROVED")
    assert verify_approval_token(approval, raw_token) is True
    assert verify_approval_token(approval, "wrong-token") is False
