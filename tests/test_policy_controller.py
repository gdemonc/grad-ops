from __future__ import annotations

import pytest

from app.config import settings
from app.models import ActionProposal, Evidence, Task
from app.policy.controller import PolicyController, PolicyDecision

CASES = [
    {
        "id": "r0_observation_allow",
        "tool": "list_processes",
        "risk": "R0",
        "decision": "ALLOW_AUTO",
        "reason": "只读工具自动放行",
    },
    {
        "id": "r1_policy_ref_auto",
        "tool": "rotate_log_file",
        "risk": "R1",
        "path": "log",
        "policy_ref": "approved-policy",
        "auto_refs": ["approved-policy"],
        "decision": "ALLOW_AUTO",
        "reason": "策略引用允许自动执行",
    },
    {
        "id": "r1_empty_auto_refs",
        "tool": "rotate_log_file",
        "risk": "R1",
        "path": "log",
        "auto_refs": [],
        "decision": "NEEDS_APPROVAL",
        "reason": "R1 默认需人工审批",
    },
    {
        "id": "r2_needs_approval",
        "tool": "backup_log_file",
        "risk": "R2",
        "path": "log",
        "decision": "NEEDS_APPROVAL",
        "reason": "需人工审批",
    },
    {
        "id": "r3_restricted_identity",
        "tool": "backup_log_file",
        "risk": "R3",
        "path": "log",
        "decision": "NEEDS_APPROVAL",
        "reason": "需人工审批",
        "restricted": True,
    },
    {
        "id": "r4_direct_deny",
        "tool": "backup_log_file",
        "risk": "R4",
        "decision": "DENY",
        "reason": "R4 危险动作直接拒绝",
    },
    {
        "id": "unknown_risk_deny",
        "tool": "backup_log_file",
        "risk": "R9",
        "decision": "DENY",
        "reason": "未知风险等级",
    },
    {
        "id": "unknown_tool_deny",
        "tool": "drop_database",
        "risk": "R2",
        "decision": "DENY",
        "reason": "工具不在白名单",
    },
    {
        "id": "path_escape_deny",
        "tool": "rotate_log_file",
        "risk": "R1",
        "path": "escape",
        "decision": "DENY",
        "reason_any": ["路径", "受保护", "受限"],
        "rules": ["path_boundary"],
    },
    {
        "id": "rm_rf_rationale_deny",
        "tool": "rotate_log_file",
        "risk": "R1",
        "path": "log",
        "rationale": "按要求执行 rm -rf / 清理所有文件",
        "decision": "DENY",
        "reason": "命中禁止规则",
        "rules": ["rm_rf_root"],
    },
    {
        "id": "missing_precondition_deny",
        "tool": "rotate_log_file",
        "risk": "R2",
        "path": "log",
        "precondition": "missing",
        "decision": "DENY",
        "reason": "缺少前置条件证据",
        "rules": ["precondition_evidence"],
    },
    {
        "id": "valid_precondition_needs_approval",
        "tool": "rotate_log_file",
        "risk": "R2",
        "path": "log",
        "precondition": "valid",
        "decision": "NEEDS_APPROVAL",
        "reason": "需人工审批",
    },
    {
        "id": "mkfs_input_deny",
        "tool": "rotate_log_file",
        "risk": "R1",
        "path": "log",
        "input": {"command": "mkfs.ext4 -t ext4 /dev/sda1"},
        "decision": "DENY",
        "reason": "命中禁止规则",
        "rules": ["mkfs"],
    },
]


@pytest.mark.parametrize("case", CASES, ids=[case["id"] for case in CASES])
def test_policy_controller_table(session, monkeypatch, tmp_path, case):
    monkeypatch.setattr(settings, "auto_policy_refs", case.get("auto_refs", []))

    log_file = tmp_path / "logs" / "app.log"
    log_file.parent.mkdir(parents=True, exist_ok=True)
    log_file.write_text("hello", encoding="utf-8")

    input_json = dict(case.get("input", {}))
    if case.get("path") == "log":
        input_json["path"] = str(log_file)
    elif case.get("path") == "escape":
        input_json["path"] = str(tmp_path / ".." / ".." / "etc" / "passwd")

    task = Task(request_text="test task")
    session.add(task)
    session.flush()

    evidence: list[Evidence] = []
    preconditions: list = []
    if case.get("precondition"):
        ev = Evidence(
            task_id=task.id,
            kind="log",
            summary="observed",
            payload_json={},
            content_hash="0" * 64,
        )
        session.add(ev)
        session.flush()
        evidence.append(ev)
        evidence_id = ev.id if case["precondition"] == "valid" else ev.id + 9999
        preconditions = [{"evidence_id": evidence_id}]

    proposal = ActionProposal(
        task_id=task.id,
        tool_name=case["tool"],
        input_json=input_json,
        risk_level=case["risk"],
        rationale=case.get("rationale", ""),
        preconditions_json=preconditions,
        postconditions_json=[],
        rollback_json={},
        policy_ref=case.get("policy_ref"),
    )
    session.add(proposal)
    session.flush()

    decision = PolicyController().evaluate(
        proposal,
        evidence,
        dev_allowed_roots=(str(tmp_path),),
    )

    assert isinstance(decision, PolicyDecision)
    assert decision.decision == case["decision"]
    if "reason" in case:
        assert case["reason"] in decision.reason
    if "reason_any" in case:
        assert any(text in decision.reason for text in case["reason_any"])
    for rule in case.get("rules", []):
        assert rule in decision.matched_rules
    if "restricted" in case:
        assert decision.requires_restricted_identity is case["restricted"]
    else:
        assert decision.requires_restricted_identity is False
