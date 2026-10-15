from __future__ import annotations

import pytest

from app import executor, platform
from app.audit import ZERO_HASH, AuditLog, stable_hash
from app.enums import TaskStatus
from app.llm.client import LLMClient
from app.models import AuditEvent, Task
from app.queue import TaskQueue
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


def _events(session, trace_id: str) -> list[AuditEvent]:
    return (
        session.query(AuditEvent)
        .filter_by(trace_id=trace_id)
        .order_by(AuditEvent.seq.asc())
        .all()
    )


def _sealed_trace(session) -> Task:
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
    session.refresh(task)
    assert task.status == TaskStatus.SEALED.value
    return task


def _replay(events: list[AuditEvent]) -> tuple[list[dict], str]:
    previous_hash = ZERO_HASH
    replayed: list[dict] = []
    for ev in events:
        payload_hash = stable_hash(
            {
                "seq": ev.seq,
                "stage": ev.stage,
                "event_type": ev.event_type,
                "message": ev.message,
                "payload": ev.payload_json,
            }
        )
        event_hash = stable_hash({"prev_hash": previous_hash, "payload_hash": payload_hash})
        replayed.append(
            {
                "seq": ev.seq,
                "stage": ev.stage,
                "event_type": ev.event_type,
                "prev_hash": previous_hash,
                "payload_hash": payload_hash,
                "event_hash": event_hash,
            }
        )
        previous_hash = event_hash
    return replayed, previous_hash


def test_full_pipeline_chain_valid_and_replayable(session, canned_commands):
    task = _sealed_trace(session)
    trace_id = task.trace_id

    result = AuditLog(session).verify_chain(trace_id)
    assert result["valid"] is True
    assert result["entry_count"] >= 4

    events = _events(session, trace_id)
    assert [ev.seq for ev in events] == list(range(1, len(events) + 1))

    replayed, head_hash = _replay(events)
    assert [item["prev_hash"] for item in replayed] == [ev.prev_hash for ev in events]
    assert [item["payload_hash"] for item in replayed] == [ev.payload_hash for ev in events]
    assert [item["event_hash"] for item in replayed] == [ev.event_hash for ev in events]
    assert head_hash == result["head_hash"]
    assert head_hash == events[-1].event_hash

    stages = [ev.stage for ev in events]
    event_types = [ev.event_type for ev in events]
    assert stages[0] == "queue"
    assert event_types[0] == "task.enqueued"
    assert event_types[-1] in {"investigation.conclude", "investigation.done", "worker.sealed"}
    assert "investigation.conclude" in event_types or "investigation.done" in event_types


def test_tamper_detected_by_seq(session, canned_commands):
    task = _sealed_trace(session)
    trace_id = task.trace_id
    events = _events(session, trace_id)
    assert len(events) >= 4
    target = events[len(events) // 2]

    target.payload_json = {"tampered": True}
    session.flush()

    result = AuditLog(session).verify_chain(trace_id)
    assert result["valid"] is False
    assert result["entry_count"] == len(events)

    by_seq = {entry["seq"]: entry for entry in result["entries"]}
    tampered = by_seq[target.seq]
    assert tampered["prev_ok"] is True
    assert tampered["payload_ok"] is False
    assert tampered["event_ok"] is False
    assert tampered["valid"] is False

    for entry in result["entries"]:
        if entry["seq"] == target.seq:
            continue
        assert entry["prev_ok"] is True
        assert entry["payload_ok"] is True
        assert entry["event_ok"] is True
        assert entry["valid"] is True

    head = result["entries"][-1]
    assert head["seq"] == events[-1].seq
    assert head["valid"] is True


def test_delete_event_detected(session, canned_commands):
    task = _sealed_trace(session)
    trace_id = task.trace_id
    events = _events(session, trace_id)
    original_count = len(events)
    target = events[original_count // 2]

    session.delete(target)
    session.flush()

    result = AuditLog(session).verify_chain(trace_id)
    assert result["valid"] is False
    assert result["entry_count"] == original_count - 1

    seqs = [entry["seq"] for entry in result["entries"]]
    assert target.seq not in seqs
    assert seqs == sorted(seqs)

    successor = next(entry for entry in result["entries"] if entry["seq"] == target.seq + 1)
    assert successor["prev_ok"] is False
    assert successor["payload_ok"] is True
    assert successor["event_ok"] is False
    assert successor["valid"] is False

    for entry in result["entries"]:
        if entry["seq"] < target.seq:
            assert entry["valid"] is True


def test_relink_attack_detected(session, canned_commands):
    task = _sealed_trace(session)
    trace_id = task.trace_id
    events = _events(session, trace_id)
    target = events[len(events) // 2]

    target.payload_json = {"forged": True}
    target.payload_hash = stable_hash(
        {
            "seq": target.seq,
            "stage": target.stage,
            "event_type": target.event_type,
            "message": target.message,
            "payload": target.payload_json,
        }
    )
    session.flush()

    result = AuditLog(session).verify_chain(trace_id)
    assert result["valid"] is False
    assert result["entry_count"] == len(events)

    by_seq = {entry["seq"]: entry for entry in result["entries"]}
    forged = by_seq[target.seq]
    assert forged["prev_ok"] is True
    assert forged["payload_ok"] is True
    assert forged["event_ok"] is False
    assert forged["valid"] is False

    for entry in result["entries"]:
        if entry["seq"] == target.seq:
            continue
        assert entry["prev_ok"] is True
        assert entry["payload_ok"] is True
        assert entry["event_ok"] is True
        assert entry["valid"] is True


def test_timeline_api_reflects_tamper(client, session, canned_commands):
    created = client.post("/api/tasks", json={"request_text": "磁盘占用异常，请调查根因"})
    assert created.status_code == 201
    trace_id = created.json()["trace_id"]

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

    ok = client.get(f"/api/audit/{trace_id}/verify")
    assert ok.status_code == 200
    assert ok.json()["valid"] is True

    events = _events(session, trace_id)
    target = events[len(events) // 2]
    target.payload_json = {"tampered": True}
    session.flush()

    resp = client.get(f"/api/audit/{trace_id}/verify")
    assert resp.status_code == 200
    body = resp.json()
    assert body["trace_id"] == trace_id
    assert body["valid"] is False
    assert body["entry_count"] == len(events)
    assert body["head_hash"]

    full = client.get(f"/api/audit/{trace_id}/verify", params={"full": "true"})
    assert full.status_code == 200
    entries = full.json()["entries"]
    assert len(entries) == len(events)
    flagged = [entry for entry in entries if not entry["payload_ok"]]
    assert [entry["seq"] for entry in flagged] == [target.seq]
