from __future__ import annotations

import pytest

from app import platform
from app.audit import stable_hash
from app.enums import RiskLevel
from app.investigation import InvestigationEngine
from app.llm.client import LLMClient
from app.models import AuditEvent, Evidence, Task
from app.tools import ToolSpec, build_registry

SS_OUTPUT = (
    'LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=123,fd=3))\n'
    "LISTEN 0 128 *:8080 *:*\n"
)

DF_OUTPUT = (
    "Filesystem     Size  Used Avail Use% Mounted on\n"
    "/dev/vda1       40G   12G   26G  32% /\n"
)


@pytest.fixture()
def canned_commands(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)
    outputs = {"ss": SS_OUTPUT, "df": DF_OUTPUT}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        return platform.CommandResult(
            exit_code=0, stdout=outputs.get(argv[0], ""), stderr="", timed_out=False
        )

    monkeypatch.setattr(platform, "run_command", fake_run_command)


def _make_task(session) -> Task:
    task = Task(request_text="磁盘占用异常，请调查根因")
    session.add(task)
    session.flush()
    return task


def _tool_call(tool: str, params: dict | None = None) -> dict:
    return {"action": "tool_call", "tool": tool, "params": params or {}}


def _event_types(session, task: Task) -> list[str]:
    events = (
        session.query(AuditEvent).filter_by(trace_id=task.trace_id).order_by(AuditEvent.seq).all()
    )
    return [ev.event_type for ev in events]


def test_investigation_collects_evidence_and_concludes(session, canned_commands):
    task = _make_task(session)
    llm = LLMClient(mode="mock")
    llm.set_script(
        [
            _tool_call("list_listening_ports"),
            _tool_call("disk_usage"),
            {
                "action": "conclude",
                "root_cause": "磁盘根分区使用率过高",
                "claims": [{"statement": "根分区已用 32%", "evidence_ids": [1, 2]}],
                "confidence": 0.8,
                "proposed_action": None,
            },
        ]
    )
    engine = InvestigationEngine(session, llm, build_registry())
    result = engine.run(task)

    assert result.task_id == task.id
    assert result.truncated is False
    assert result.iterations == 3
    assert result.tool_call_names == ["list_listening_ports", "disk_usage"]
    evidences = session.query(Evidence).filter_by(task_id=task.id).all()
    assert len(evidences) == 2
    assert result.evidence_ids == [ev.id for ev in evidences]
    for ev in evidences:
        assert ev.kind == "observation"
        assert ev.tool_call_id is None
        assert ev.content_hash == stable_hash(ev.payload_json["result"])
        assert ev.payload_json["tool"] in {"list_listening_ports", "disk_usage"}
    assert result.conclusions["root_cause"] == "磁盘根分区使用率过高"
    assert result.conclusions["claims"][0]["evidence_ids"] == [1, 2]
    assert result.conclusions["confidence"] == 0.8
    event_types = _event_types(session, task)
    assert len(event_types) >= 3
    assert event_types == [
        "investigation.start",
        "investigation.tool_call",
        "investigation.tool_call",
        "investigation.conclude",
        "investigation.done",
    ]


def test_investigation_stops_at_max_tool_calls(session, canned_commands):
    task = _make_task(session)
    llm = LLMClient(mode="mock")
    llm.set_script(
        [
            _tool_call("list_listening_ports"),
            _tool_call("disk_usage"),
            _tool_call("list_processes"),
            _tool_call("list_listening_ports"),
            _tool_call("disk_usage"),
            {"action": "conclude", "root_cause": "never reached", "claims": [], "confidence": 0.0},
        ]
    )
    engine = InvestigationEngine(session, llm, build_registry(), max_tool_calls=2)
    result = engine.run(task)

    assert result.truncated is True
    assert result.tool_call_names == ["list_listening_ports", "disk_usage"]
    assert session.query(Evidence).filter_by(task_id=task.id).count() == 2
    assert result.conclusions["claims"] == []


def test_investigation_timeout(session, canned_commands, monkeypatch):
    task = _make_task(session)
    llm = LLMClient(mode="mock")
    llm.set_script(
        [
            _tool_call("disk_usage"),
            {
                "action": "conclude",
                "root_cause": "never reached",
                "claims": [],
                "confidence": 0.0,
                "proposed_action": None,
            },
        ]
    )
    clock_state = {"t": 0.0}

    def fake_clock() -> float:
        return clock_state["t"]

    original_complete = llm.complete_json

    def jumping_complete_json(**kwargs):
        response = original_complete(**kwargs)
        clock_state["t"] = 9999.0
        return response

    monkeypatch.setattr(llm, "complete_json", jumping_complete_json)
    engine = InvestigationEngine(session, llm, build_registry(), clock=fake_clock)
    result = engine.run(task)

    assert result.truncated is True
    assert result.iterations <= engine.max_iterations
    assert result.conclusions["claims"] == []
    assert "未在限制内收敛" in result.conclusions["root_cause"]
    assert session.query(Evidence).filter_by(task_id=task.id).count() == 1


def test_investigation_rejects_side_effect_tool_call(session, canned_commands):
    task = _make_task(session)

    def boom(params: dict) -> dict:
        raise AssertionError("rotate_log_file handler must not be invoked")

    registry = build_registry()
    registry.register(
        ToolSpec(
            name="rotate_log_file",
            description="Rotate a log file (side effect).",
            parameters={"type": "object", "properties": {}},
            risk_level=RiskLevel.R1,
            has_side_effect=True,
            handler=boom,
        )
    )
    llm = LLMClient(mode="mock")
    llm.set_script([_tool_call("rotate_log_file", {"path": "/var/log/app.log"})])
    engine = InvestigationEngine(session, llm, registry)
    result = engine.run(task)

    assert result.truncated is True
    assert session.query(Evidence).filter_by(task_id=task.id).count() == 0
    assert "investigation.invalid_tool_call" in _event_types(session, task)
    assert result.conclusions["root_cause"] == "调查中止：模型请求了非只读工具"
    assert result.conclusions["claims"] == []


def test_investigation_invalid_response_truncated(session, canned_commands):
    task = _make_task(session)
    llm = LLMClient(mode="mock")
    llm.set_script([{"action": "nonsense"}])
    engine = InvestigationEngine(session, llm, build_registry())
    result = engine.run(task)

    assert result.truncated is True
    assert result.conclusions["claims"] == []
    assert "investigation.invalid_response" in _event_types(session, task)
