from __future__ import annotations

import pytest
from sqlalchemy import update

from app import executor, platform
from app.enums import ProposalStatus, TaskStatus
from app.llm.client import LLMClient
from app.models import ActionProposal, Approval, AuditEvent, Task, TaskJob
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


def _rotate_action(path) -> dict:
    return {
        "tool": "rotate_log_file",
        "input": {"path": str(path)},
        "risk_level": "R2",
        "rationale": "轮转过大的应用日志",
        "policy_ref": "rotate-policy",
        "preconditions": [{"evidence_id": 1}],
        "postconditions": [{"type": "path_missing", "path": str(path)}],
        "rollback": {"tool": "rotate_log_file"},
    }


def _park_rotate(client, session, tmp_path, llm) -> tuple[object, int, int]:
    log_file = tmp_path / "app.log"
    log_file.write_text("2026-10-15 INFO boot\n", encoding="utf-8")
    created = client.post("/api/tasks", json={"request_text": "轮转应用日志文件"}).json()
    llm.set_script(
        [
            _tool_call("list_listening_ports"),
            _conclude("应用日志过大需要轮转", _rotate_action(log_file)),
        ]
    )
    worker = Worker(session, llm=llm, dev_allowed_roots=(str(tmp_path),))
    assert worker.run_once() is True
    session.refresh(session.get(Task, created["id"]))
    pending = client.get("/api/approvals", params={"status": "PENDING"}).json()
    assert len(pending) == 1
    return log_file, created["id"], pending[0]["id"]


def test_healthz_regression(client):
    resp = client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json()["status"] == "ok"


def test_create_task_and_list(client):
    resp = client.post("/api/tasks", json={"request_text": "检查磁盘占用"})
    assert resp.status_code == 201
    body = resp.json()
    assert body["id"] is not None
    assert body["trace_id"]
    assert body["status"] == TaskStatus.QUEUED.value

    listing = client.get("/api/tasks").json()
    assert [item["id"] for item in listing] == sorted(
        [item["id"] for item in listing], reverse=True
    )
    assert any(item["id"] == body["id"] for item in listing)
    item = next(entry for entry in listing if entry["id"] == body["id"])
    assert item["request_text"] == "检查磁盘占用"
    assert item["status"] == TaskStatus.QUEUED.value
    assert item["summary"] is None
    assert item["created_at"]

    filtered = client.get("/api/tasks", params={"status": "SEALED"}).json()
    assert all(entry["id"] != body["id"] for entry in filtered)

    detail = client.get(f"/api/tasks/{body['id']}").json()
    assert detail["trace_id"] == body["trace_id"]
    assert detail["evidence"] == []
    assert detail["proposals"] == []
    assert detail["approvals"] == []
    assert detail["executions"] == []
    assert detail["verifications"] == []
    assert detail["grounding_summary"] is None


def test_create_task_rejects_empty_text(client):
    resp = client.post("/api/tasks", json={"request_text": ""})
    assert resp.status_code == 422


def test_timeline_empty_ok(client, session):
    task = Task(request_text="尚无审计事件的任务")
    session.add(task)
    session.flush()

    resp = client.get(f"/api/tasks/{task.id}/timeline")

    assert resp.status_code == 200
    assert resp.json() == []


def test_full_flow_readonly(client, session, canned_commands):
    created = client.post(
        "/api/tasks", json={"request_text": "磁盘占用异常，请调查根因"}
    ).json()
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

    detail = client.get(f"/api/tasks/{created['id']}").json()
    assert detail["status"] == TaskStatus.SEALED.value
    assert detail["summary"] == "磁盘根分区使用率过高"
    assert len(detail["evidence"]) == 2
    for evidence in detail["evidence"]:
        assert evidence["kind"] == "observation"
        assert evidence["content_hash"]
        assert evidence["created_at"]
    assert detail["grounding_summary"] is not None
    assert detail["grounding_summary"]["supported"] == 1
    assert detail["grounding_summary"]["unsupported"] == 0
    assert detail["proposals"] == []

    timeline = client.get(f"/api/tasks/{created['id']}/timeline").json()
    assert [entry["seq"] for entry in timeline] == sorted(
        entry["seq"] for entry in timeline
    )
    event_types = [entry["event_type"] for entry in timeline]
    assert any(name.startswith("investigation.") for name in event_types)
    assert "grounding.claims_bound" in event_types
    assert "worker.sealed" in event_types
    assert timeline[0]["stage"]
    assert timeline[0]["message"]
    assert isinstance(timeline[0]["payload_json"], dict)

    verify = client.get(f"/api/audit/{created['trace_id']}/verify").json()
    assert verify["valid"] is True
    assert verify["entry_count"] > 0
    assert verify["head_hash"]
    assert "entries" not in verify

    full = client.get(
        f"/api/audit/{created['trace_id']}/verify", params={"full": "true"}
    ).json()
    assert full["valid"] is True
    assert len(full["entries"]) == full["entry_count"]
    assert all(entry["valid"] for entry in full["entries"])


def test_malicious_rejected(client, session, canned_commands):
    created = client.post("/api/tasks", json={"request_text": "rm -rf /"}).json()
    worker = Worker(session, llm=LLMClient(mode="mock"))

    assert worker.run_once() is True

    detail = client.get(f"/api/tasks/{created['id']}").json()
    assert detail["status"] == TaskStatus.REJECTED.value
    assert detail["evidence"] == []
    assert detail["proposals"] == []
    timeline = client.get(f"/api/tasks/{created['id']}/timeline").json()
    assert "policy.request_rejected" in [entry["event_type"] for entry in timeline]


def test_approval_queue_and_approve_resume(client, session, tmp_path, canned_commands):
    log_file, task_id, approval_id = _park_rotate(
        client, session, tmp_path, LLMClient(mode="mock")
    )

    queue = client.get("/api/approvals", params={"status": "PENDING"}).json()
    assert len(queue) == 1
    pending = queue[0]
    assert pending["id"] == approval_id
    assert pending["status"] == "PENDING"
    assert pending["tool_name"] == "rotate_log_file"
    assert pending["risk_level"] == "R2"
    assert pending["task_id"] == task_id
    assert pending["request_text"]

    resp = client.post(
        f"/api/approvals/{approval_id}/approve", json={"comment": "同意执行"}
    )
    assert resp.status_code == 200
    result = resp.json()
    assert result["approval_id"] == approval_id
    assert result["status"] == "APPROVED"
    assert result["resumed"] is True

    approval = session.get(Approval, approval_id)
    assert approval.status == "APPROVED"
    assert approval.decided_by == "operator"
    assert approval.decided_at is not None
    assert approval.comment == "同意执行"
    assert approval.token_hash and approval.token_hash != ""
    proposal = session.get(ActionProposal, approval.proposal_id)
    assert proposal.status == ProposalStatus.APPROVED.value
    task = session.get(Task, task_id)
    assert task.status == TaskStatus.EXECUTING.value
    jobs = session.query(TaskJob).filter_by(task_id=task_id).all()
    assert len(jobs) == 2

    worker = Worker(session, dev_allowed_roots=(str(tmp_path),))
    assert worker.run_once() is True
    assert worker.run_once() is False

    detail = client.get(f"/api/tasks/{task_id}").json()
    assert detail["status"] == TaskStatus.SEALED.value
    assert detail["summary"] == "执行并独立验证通过"
    assert len(detail["executions"]) == 1
    assert detail["executions"][0]["ok"] is True
    assert len(detail["verifications"]) == 1
    assert detail["verifications"][0]["passed"] is True
    assert detail["proposals"][0]["status"] == ProposalStatus.EXECUTED.value
    assert detail["approvals"][0]["status"] == "CONSUMED"
    assert not log_file.exists()
    assert (tmp_path / "app.log.1").exists()

    again = client.post(
        f"/api/approvals/{approval_id}/approve", json={"comment": "再次批准"}
    )
    assert again.status_code == 409


def test_reject_approval(client, session, tmp_path, canned_commands):
    _log_file, task_id, approval_id = _park_rotate(
        client, session, tmp_path, LLMClient(mode="mock")
    )

    resp = client.post(
        f"/api/approvals/{approval_id}/reject", json={"comment": "风险过高，先不执行"}
    )
    assert resp.status_code == 200
    assert resp.json()["status"] == "REJECTED"

    approval = session.get(Approval, approval_id)
    assert approval.status == "REJECTED"
    assert approval.decided_by == "operator"
    assert approval.comment == "风险过高，先不执行"
    proposal = session.get(ActionProposal, approval.proposal_id)
    assert proposal.status == ProposalStatus.REJECTED.value
    task = session.get(Task, task_id)
    assert task.status == TaskStatus.REJECTED.value
    assert task.summary == "风险过高，先不执行"
    assert client.get("/api/approvals", params={"status": "PENDING"}).json() == []

    again = client.post(
        f"/api/approvals/{approval_id}/reject", json={"comment": "重复拒绝"}
    )
    assert again.status_code == 409


def test_audit_verify_detects_tamper(client, session, canned_commands):
    created = client.post("/api/tasks", json={"request_text": "检查监听端口"}).json()
    llm = LLMClient(mode="mock")
    llm.set_script([_tool_call("list_listening_ports"), _conclude("端口正常")])
    worker = Worker(session, llm=llm)
    assert worker.run_once() is True
    assert client.get(f"/api/audit/{created['trace_id']}/verify").json()["valid"] is True

    event = (
        session.query(AuditEvent)
        .filter_by(trace_id=created["trace_id"])
        .order_by(AuditEvent.seq.asc())
        .first()
    )
    session.execute(
        update(AuditEvent)
        .where(AuditEvent.id == event.id)
        .values(payload_json={"tampered": True})
        .execution_options(synchronize_session=False)
    )
    session.commit()
    session.expire_all()

    result = client.get(f"/api/audit/{created['trace_id']}/verify").json()
    assert result["valid"] is False
    full = client.get(
        f"/api/audit/{created['trace_id']}/verify", params={"full": "true"}
    ).json()
    assert full["entries"][0]["payload_ok"] is False


def test_safety_report(client):
    resp = client.get("/api/safety/report")
    assert resp.status_code == 200
    body = resp.json()
    assert body["overall_status"] in {"ok", "warn", "blocked"}
    assert body["executor"]["runtime_uid"] == -1
    assert body["executor"]["target_user"]
    assert isinstance(body["executor"]["allow_root_executor"], bool)
    assert isinstance(body["executor"]["action_execution_enabled"], bool)
    assert body["boundary"]["allowed_tools"] == ["backup_log_file", "rotate_log_file"]
    assert "/var/log/" in body["boundary"]["allowed_path_prefixes"]
    assert "/etc/" in body["boundary"]["protected_path_prefixes"]
    assert len(body["guards"]) == 4
    assert {guard["key"] for guard in body["guards"]} == {
        "runtime_identity",
        "tool_whitelist",
        "path_boundary",
        "approval_gate",
    }
    assert all(guard["status"] in {"ok", "warn", "blocked"} for guard in body["guards"])
    assert all(guard["name"] for guard in body["guards"])
    assert all(guard["detail"] for guard in body["guards"])


def test_run_endpoint_kicks_queue(client, canned_commands):
    created = client.post("/api/tasks", json={"request_text": "查看磁盘占用"}).json()

    first = client.post("/api/tasks/run", json={})
    assert first.status_code == 200
    assert first.json() == {"worked": True}
    assert client.get(f"/api/tasks/{created['id']}").json()["status"] in {
        TaskStatus.SEALED.value,
        TaskStatus.FAILED.value,
        TaskStatus.REJECTED.value,
    }

    second = client.post("/api/tasks/run", json={})
    assert second.json() == {"worked": False}


def test_404s(client):
    assert client.get("/api/tasks/999").status_code == 404
    assert client.get("/api/tasks/999/timeline").status_code == 404
    assert (
        client.post("/api/approvals/999/approve", json={"comment": ""}).status_code
        == 404
    )
    assert (
        client.post("/api/approvals/999/reject", json={"comment": ""}).status_code == 404
    )
