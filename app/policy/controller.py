from __future__ import annotations

import json
from dataclasses import dataclass, field

from app.config import settings
from app.enums import RiskLevel
from app.models import ActionProposal, Evidence
from app.policy.boundary import BoundaryViolation, authorize_path
from app.policy.rules import (
    OBSERVATION_TOOLS,
    RISK_DECISION,
    SIDE_EFFECT_WHITELIST,
    scan_forbidden,
)

PATH_KEYS = ("path", "restore_target", "artifact_path")


@dataclass
class PolicyDecision:
    decision: str
    reason: str
    risk_level: RiskLevel
    requires_restricted_identity: bool = False
    matched_rules: list[str] = field(default_factory=list)


class PolicyController:
    def evaluate(
        self,
        proposal: ActionProposal,
        evidence: list[Evidence],
        *,
        approval_token_valid: bool | None = None,
        dev_allowed_roots: tuple[str, ...] = (),
    ) -> PolicyDecision:
        try:
            risk = RiskLevel(proposal.risk_level)
        except ValueError:
            return PolicyDecision(decision="DENY", reason="未知风险等级", risk_level=RiskLevel.R4)
        if risk is RiskLevel.R4:
            return PolicyDecision(decision="DENY", reason="R4 危险动作直接拒绝", risk_level=risk)

        if proposal.tool_name in SIDE_EFFECT_WHITELIST:
            pass
        elif proposal.tool_name in OBSERVATION_TOOLS and risk is RiskLevel.R0:
            return PolicyDecision(decision="ALLOW_AUTO", reason="只读工具自动放行", risk_level=risk)
        else:
            return PolicyDecision(decision="DENY", reason="工具不在白名单", risk_level=risk)

        input_json = proposal.input_json if isinstance(proposal.input_json, dict) else {}
        for key in PATH_KEYS:
            value = input_json.get(key)
            if isinstance(value, str):
                try:
                    authorize_path(value, dev_allowed_roots=dev_allowed_roots)
                except BoundaryViolation as exc:
                    return PolicyDecision(
                        decision="DENY",
                        reason=str(exc),
                        risk_level=risk,
                        matched_rules=["path_boundary"],
                    )

        scan_text = (
            (proposal.tool_name or "")
            + json.dumps(proposal.input_json, ensure_ascii=False)
            + (proposal.rationale or "")
        )
        hits = scan_forbidden(scan_text)
        if hits:
            return PolicyDecision(
                decision="DENY",
                reason="命中禁止规则:" + ",".join(hits),
                risk_level=risk,
                matched_rules=hits,
            )

        preconditions = proposal.preconditions_json or []
        if preconditions:
            evidence_ids = {item.id for item in evidence}
            for precondition in preconditions:
                evidence_id = None
                if isinstance(precondition, dict):
                    evidence_id = precondition.get("evidence_id")
                if evidence_id not in evidence_ids:
                    return PolicyDecision(
                        decision="DENY",
                        reason="缺少前置条件证据",
                        risk_level=risk,
                        matched_rules=["precondition_evidence"],
                    )

        mapped = RISK_DECISION[risk]
        if mapped == "ALLOW_AUTO":
            return PolicyDecision(
                decision="ALLOW_AUTO", reason=f"{risk.value} 风险自动放行", risk_level=risk
            )
        if mapped == "CONDITIONAL":
            if proposal.policy_ref and proposal.policy_ref in settings.auto_policy_refs:
                return PolicyDecision(
                    decision="ALLOW_AUTO", reason="策略引用允许自动执行", risk_level=risk
                )
            return PolicyDecision(
                decision="NEEDS_APPROVAL", reason="R1 默认需人工审批", risk_level=risk
            )
        return PolicyDecision(
            decision="NEEDS_APPROVAL",
            reason=f"{risk.value} 需人工审批",
            risk_level=risk,
            requires_restricted_identity=risk is RiskLevel.R3,
        )
