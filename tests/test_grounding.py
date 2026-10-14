from __future__ import annotations

from app.audit import AuditLog
from app.grounding import apply_grounding, bind_claims_to_evidence
from app.models import AuditEvent, Task


def _conclusions(claims):
    return {
        "root_cause": "磁盘写满",
        "claims": claims,
        "confidence": 0.8,
        "proposed_action": None,
    }


def test_cited_evidence_supported():
    conclusions = _conclusions(
        [
            {"statement": "日志目录占用过高", "evidence_ids": [1, 2]},
            {"statement": "inode 耗尽", "evidence_ids": [2]},
        ]
    )

    result = bind_claims_to_evidence(conclusions, [1, 2, 3])

    assert len(result.supported) == 2
    assert result.unsupported == []
    assert result.supported[0]["evidence_ids"] == [1, 2]


def test_nonexistent_id_dropped():
    conclusions = _conclusions([{"statement": "伪造证据", "evidence_ids": [1, 99]}])

    result = bind_claims_to_evidence(conclusions, [1, 2, 3])

    assert result.supported == []
    assert len(result.unsupported) == 1
    assert "不存在" in result.unsupported[0]["drop_reason"]
    assert result.unsupported[0]["statement"] == "伪造证据"


def test_empty_citation_dropped():
    conclusions = _conclusions([{"statement": "无依据结论", "evidence_ids": []}])

    result = bind_claims_to_evidence(conclusions, [1, 2, 3])

    assert result.supported == []
    assert len(result.unsupported) == 1
    assert "未引用任何证据" in result.unsupported[0]["drop_reason"]


def test_zero_claims_ok():
    result = bind_claims_to_evidence(_conclusions([]), [1, 2, 3])

    assert result.supported == []
    assert result.unsupported == []


def test_malformed_claim_dropped():
    conclusions = _conclusions(
        [
            {"evidence_ids": [1]},
            "not-a-dict",
            {"statement": "", "evidence_ids": [1]},
        ]
    )

    result = bind_claims_to_evidence(conclusions, [1])

    assert result.supported == []
    assert len(result.unsupported) == 3
    assert all("drop_reason" in claim for claim in result.unsupported)


def test_apply_grounding_replaces_claims():
    claims = [
        {"statement": "有效结论", "evidence_ids": [1]},
        {"statement": "无效结论", "evidence_ids": [99]},
    ]
    conclusions = _conclusions(claims)
    result = bind_claims_to_evidence(conclusions, [1])

    applied = apply_grounding(conclusions, result)

    assert conclusions["claims"] == claims
    assert len(applied["claims"]) == 1
    assert applied["claims"][0]["statement"] == "有效结论"
    assert "drop_reason" not in applied["claims"][0]
    assert applied["root_cause"] == conclusions["root_cause"]
    assert applied["confidence"] == conclusions["confidence"]
    assert applied["proposed_action"] is None
    assert applied["grounding"]["supported_count"] == 1
    assert applied["grounding"]["unsupported_count"] == 1
    assert applied["grounding"]["unsupported"] == result.unsupported
    assert "warning" not in applied["grounding"]


def test_all_unsupported_sets_warning():
    conclusions = _conclusions([{"statement": "无效", "evidence_ids": [99]}])
    result = bind_claims_to_evidence(conclusions, [1])

    applied = apply_grounding(conclusions, result)

    assert applied["claims"] == []
    assert applied["grounding"]["warning"] == "所有结论均缺少证据支撑"


def test_audit_event_written(session):
    task = Task(request_text="磁盘告警")
    session.add(task)
    session.flush()
    conclusions = _conclusions(
        [
            {"statement": "有效", "evidence_ids": [1]},
            {"statement": "无效", "evidence_ids": [99]},
        ]
    )

    result = bind_claims_to_evidence(
        conclusions, [1], audit=AuditLog(session), trace_id=task.trace_id
    )

    events = (
        session.query(AuditEvent)
        .filter_by(trace_id=task.trace_id, event_type="grounding.claims_bound")
        .all()
    )
    assert len(events) == 1
    assert events[0].payload_json["supported"] == 1
    assert events[0].payload_json["unsupported"] == 1
    assert events[0].payload_json["dropped_reasons"]
    assert result.audit_events == ["grounding.claims_bound"]
