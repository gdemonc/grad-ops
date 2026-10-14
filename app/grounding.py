from __future__ import annotations

from dataclasses import dataclass, field

from app.audit import AuditLog


@dataclass
class GroundingResult:
    supported: list[dict] = field(default_factory=list)
    unsupported: list[dict] = field(default_factory=list)
    audit_events: list[str] = field(default_factory=list)


def bind_claims_to_evidence(
    conclusions: dict,
    evidence_ids: list[int],
    *,
    audit: AuditLog | None = None,
    trace_id: str | None = None,
) -> GroundingResult:
    valid = set(evidence_ids)
    supported: list[dict] = []
    unsupported: list[dict] = []
    dropped_reasons: list[str] = []
    for claim in conclusions.get("claims", []):
        if not isinstance(claim, dict):
            unsupported.append(
                {"statement": str(claim), "evidence_ids": [], "drop_reason": "结论格式非法：非字典"}
            )
            dropped_reasons.append("结论格式非法：非字典")
            continue
        statement = claim.get("statement")
        cited = claim.get("evidence_ids")
        if not isinstance(statement, str) or not statement or not isinstance(cited, list):
            unsupported.append({**claim, "drop_reason": "结论格式非法：缺少陈述或引用"})
            dropped_reasons.append("结论格式非法：缺少陈述或引用")
            continue
        if not cited:
            unsupported.append({**claim, "drop_reason": "结论未引用任何证据"})
            dropped_reasons.append("结论未引用任何证据")
            continue
        missing = [eid for eid in cited if eid not in valid]
        if missing:
            reason = f"引用了不存在的证据:{missing}"
            unsupported.append({**claim, "drop_reason": reason})
            dropped_reasons.append(reason)
            continue
        supported.append(claim)
    audit_events: list[str] = []
    if audit is not None and trace_id is not None:
        audit.append(
            trace_id,
            "grounding",
            "grounding.claims_bound",
            f"结论绑定证据：supported={len(supported)} unsupported={len(unsupported)}",
            {
                "supported": len(supported),
                "unsupported": len(unsupported),
                "dropped_reasons": dropped_reasons,
            },
        )
        audit_events.append("grounding.claims_bound")
    return GroundingResult(
        supported=supported, unsupported=unsupported, audit_events=audit_events
    )


def apply_grounding(conclusions: dict, result: GroundingResult) -> dict:
    had_claims = bool(conclusions.get("claims", []))
    grounding = {
        "supported_count": len(result.supported),
        "unsupported_count": len(result.unsupported),
        "unsupported": result.unsupported,
    }
    if had_claims and not result.supported:
        grounding["warning"] = "所有结论均缺少证据支撑"
    new_conclusions = dict(conclusions)
    new_conclusions["claims"] = [
        {k: v for k, v in claim.items() if k != "drop_reason"} for claim in result.supported
    ]
    new_conclusions["grounding"] = grounding
    return new_conclusions
