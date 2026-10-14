from __future__ import annotations

import pytest

from app.audit import stable_hash
from app.contract import ContractTampered, assert_contract_matches, build_contract, copy_contract
from app.models import ActionProposal, Task


@pytest.fixture()
def proposal(session):
    task = Task(request_text="test task")
    session.add(task)
    session.flush()
    p = ActionProposal(
        task_id=task.id,
        tool_name="backup_log_file",
        input_json={"path": "/var/log/app/app.log"},
        risk_level="R2",
        rationale="备份日志",
        policy_ref="backup-policy",
    )
    session.add(p)
    session.flush()
    return p


def test_digest_stable_across_calls(proposal):
    c1 = build_contract(proposal)
    c2 = build_contract(proposal)
    assert c1["contract_digest"] == c2["contract_digest"]


def test_digest_changes_when_input_changes(proposal):
    c1 = build_contract(proposal)
    proposal.input_json = {"path": "/var/log/app/other.log"}
    c2 = build_contract(proposal)
    assert c1["contract_digest"] != c2["contract_digest"]


def test_requires_persisted_proposal(session):
    task = Task(request_text="test task")
    session.add(task)
    session.flush()
    p = ActionProposal(
        task_id=task.id,
        tool_name="backup_log_file",
        input_json={"path": "/var/log/app/app.log"},
        risk_level="R2",
        rationale="备份日志",
    )
    assert p.id is None
    with pytest.raises(ValueError, match="action proposal must be persisted before binding"):
        build_contract(p)


def test_assert_passes_on_unchanged(proposal):
    contract = build_contract(proposal)
    assert_contract_matches(contract, proposal)


def test_assert_fails_on_tampered_input(proposal):
    contract = build_contract(proposal)
    proposal.input_json = {"path": "/var/log/app/evil.log"}
    with pytest.raises(ContractTampered) as exc:
        assert_contract_matches(contract, proposal)
    assert "契约" in str(exc.value)


def test_copy_rejects_missing_key(proposal):
    contract = build_contract(proposal)
    del contract["risk_level"]
    with pytest.raises(ContractTampered):
        copy_contract(contract)


def test_copy_rejects_non_dict_input(proposal):
    contract = build_contract(proposal)
    contract["input"] = "not-a-dict"
    with pytest.raises(ContractTampered):
        copy_contract(contract)


def test_digest_key_order_independent():
    a = {
        "proposal_id": 1,
        "task_id": 2,
        "tool_name": "t",
        "input": {"x": 1},
        "risk_level": "R1",
        "policy_ref": None,
    }
    b = {
        "policy_ref": None,
        "risk_level": "R1",
        "input": {"x": 1},
        "tool_name": "t",
        "task_id": 2,
        "proposal_id": 1,
    }
    assert stable_hash(a) == stable_hash(b)
