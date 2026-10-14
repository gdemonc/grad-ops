from __future__ import annotations

from app.audit import ZERO_HASH, AuditLog, stable_hash
from app.models import AuditEvent


def test_append_and_verify_chain(session):
    trace_id = "trace-chain-ok"
    log = AuditLog(session)
    log.append(
        trace_id=trace_id,
        stage="investigation",
        event_type="tool.call",
        message="first event",
        payload={"step": 1},
    )
    log.append(
        trace_id=trace_id,
        stage="approval",
        event_type="approval.granted",
        message="second event",
        payload={"step": 2},
    )
    log.append(
        trace_id=trace_id,
        stage="execution",
        event_type="execution.done",
        message="third event",
        payload={"step": 3},
    )

    result = log.verify_chain(trace_id)

    assert result["valid"] is True
    assert result["entry_count"] == 3
    events = (
        session.query(AuditEvent)
        .filter_by(trace_id=trace_id)
        .order_by(AuditEvent.seq.asc())
        .all()
    )
    assert [ev.seq for ev in events] == [1, 2, 3]
    assert result["head_hash"] == events[-1].event_hash
    assert events[0].prev_hash == ZERO_HASH


def test_detect_payload_tamper(session):
    trace_id = "trace-payload-tamper"
    log = AuditLog(session)
    for i in range(3):
        log.append(
            trace_id=trace_id,
            stage="investigation",
            event_type="tool.call",
            message=f"event {i}",
            payload={"i": i},
        )

    mid = session.query(AuditEvent).filter_by(trace_id=trace_id, seq=2).one()
    mid.payload_json = {"tampered": True}
    session.flush()

    result = log.verify_chain(trace_id)

    assert result["valid"] is False
    assert result["entries"][1]["payload_ok"] is False


def test_detect_hash_relink(session):
    trace_id = "trace-hash-relink"
    log = AuditLog(session)
    for i in range(3):
        log.append(
            trace_id=trace_id,
            stage="investigation",
            event_type="tool.call",
            message=f"event {i}",
            payload={"i": i},
        )

    mid = session.query(AuditEvent).filter_by(trace_id=trace_id, seq=2).one()
    mid.payload_json = {"tampered": True}
    mid.payload_hash = stable_hash(
        {
            "seq": mid.seq,
            "stage": mid.stage,
            "event_type": mid.event_type,
            "message": mid.message,
            "payload": mid.payload_json,
        }
    )
    session.flush()

    result = log.verify_chain(trace_id)

    assert result["valid"] is False
    assert result["entries"][1]["payload_ok"] is True
    assert result["entries"][1]["event_ok"] is False
