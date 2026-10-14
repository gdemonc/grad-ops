from __future__ import annotations

from copy import deepcopy

from app.audit import stable_hash
from app.models import ActionProposal

CONTRACT_KEYS = {
    "proposal_id",
    "task_id",
    "tool_name",
    "input",
    "risk_level",
    "policy_ref",
    "contract_digest",
}


class ContractTampered(ValueError):
    pass


def build_contract(proposal: ActionProposal) -> dict:
    if proposal.id is None:
        raise ValueError("action proposal must be persisted before binding")
    canonical = {
        "proposal_id": proposal.id,
        "task_id": proposal.task_id,
        "tool_name": proposal.tool_name,
        "input": deepcopy(proposal.input_json),
        "risk_level": proposal.risk_level,
        "policy_ref": proposal.policy_ref,
    }
    return {**canonical, "contract_digest": stable_hash(canonical)}


def assert_contract_matches(contract: dict, proposal: ActionProposal) -> None:
    expected = build_contract(proposal)
    if (
        contract["contract_digest"] != expected["contract_digest"]
        or contract["tool_name"] != proposal.tool_name
        or contract["input"] != proposal.input_json
    ):
        raise ContractTampered("处置方案与已批准动作契约不一致")


def copy_contract(contract: dict) -> dict:
    if set(contract.keys()) != CONTRACT_KEYS or not isinstance(contract.get("input"), dict):
        raise ContractTampered("动作契约结构不完整")
    return deepcopy(contract)
