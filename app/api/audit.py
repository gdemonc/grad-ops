from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, Query
from sqlalchemy.orm import Session

from app import executor
from app.audit import AuditLog
from app.config import settings
from app.db import get_session
from app.policy.boundary import ALLOWED_PREFIXES, PROTECTED_PREFIXES
from app.policy.rules import SIDE_EFFECT_WHITELIST

router = APIRouter(prefix="/api", tags=["audit"])

SessionDep = Annotated[Session, Depends(get_session)]


def _identity_guard(identity: dict, allow_root: bool) -> tuple[str, str]:
    uid = identity["uid"]
    user = identity["user"]
    if uid == 0 and not allow_root:
        return "blocked", f"当前身份 {user}/0，副作用工具已被策略锁定。"
    if uid == 0:
        return "warn", f"当前身份 {user}/0，已显式允许 root 调试执行。"
    if uid == -1:
        return "ok", f"当前身份 {user}（Windows 平台无 Unix uid，按非 root 处理），符合最小权限运行要求。"
    return "ok", f"当前身份 {user}/{uid}，符合最小权限运行要求。"


def _build_safety_report() -> dict:
    identity = executor.current_identity()
    running_as_root = identity["uid"] == 0
    allow_root = settings.allow_root_executor
    action_execution_enabled = not running_as_root or allow_root
    if running_as_root and not allow_root:
        overall_status = "blocked"
        summary = "当前服务以 root 身份运行，副作用工具已被策略锁定。"
    elif running_as_root:
        overall_status = "warn"
        summary = "当前服务允许 root 执行副作用工具，仅适合临时调试。"
    else:
        overall_status = "ok"
        summary = "当前服务使用受限身份运行，副作用工具受白名单、路径边界和审批约束。"
    identity_status, identity_detail = _identity_guard(identity, allow_root)
    allowed_tools = sorted(SIDE_EFFECT_WHITELIST)
    return {
        "overall_status": overall_status,
        "summary": summary,
        "executor": {
            "runtime_user": identity["user"],
            "runtime_uid": identity["uid"],
            "target_user": settings.executor_user,
            "allow_root_executor": allow_root,
            "action_execution_enabled": action_execution_enabled,
        },
        "boundary": {
            "allowed_tools": allowed_tools,
            "allowed_path_prefixes": list(ALLOWED_PREFIXES),
            "protected_path_prefixes": list(PROTECTED_PREFIXES),
        },
        "guards": [
            {
                "key": "runtime_identity",
                "name": "运行身份",
                "status": identity_status,
                "detail": identity_detail,
            },
            {
                "key": "tool_whitelist",
                "name": "工具白名单",
                "status": "ok",
                "detail": f"副作用执行仅开放 {'、'.join(allowed_tools)}，其余工具一律拒绝。",
            },
            {
                "key": "path_boundary",
                "name": "路径边界",
                "status": "ok",
                "detail": (
                    f"允许前缀 {'、'.join(ALLOWED_PREFIXES)}，"
                    f"{'、'.join(PROTECTED_PREFIXES)} 等受保护路径保持隔离。"
                ),
            },
            {
                "key": "approval_gate",
                "name": "审批闸门",
                "status": "ok",
                "detail": "R2/R3 处置需先生成方案，再由人工审批触发执行。",
            },
        ],
    }


@router.get("/audit/{trace_id}/verify")
def verify_trace(
    trace_id: str,
    session: SessionDep,
    full: Annotated[bool, Query()] = False,
) -> dict:
    result = AuditLog(session).verify_chain(trace_id)
    payload = {
        "trace_id": trace_id,
        "valid": result["valid"],
        "entry_count": result["entry_count"],
        "head_hash": result["head_hash"],
    }
    if full:
        payload["entries"] = result["entries"]
    return payload


@router.get("/safety/report")
def safety_report() -> dict:
    return _build_safety_report()
