from __future__ import annotations

import os
from dataclasses import asdict, dataclass, field
from pathlib import Path

from sqlalchemy.orm import Session

from app.audit import AuditLog
from app.enums import TaskStatus
from app.models import ActionProposal, ExecutionRecord, Task, VerificationRecord, utcnow


class VerificationIndependenceError(RuntimeError):
    pass


@dataclass
class VerificationCheck:
    name: str
    ok: bool
    detail: str
    tool_call_names: list[str] = field(default_factory=list)


def _check_path_exists(spec: dict) -> VerificationCheck:
    path = spec.get("path")
    if not isinstance(path, str) or not path:
        return VerificationCheck("path_exists", False, "验证项缺少 path")
    target = Path(path)
    if not target.exists():
        return VerificationCheck("path_exists", False, f"路径不存在：{path}")
    min_size = spec.get("min_size")
    if isinstance(min_size, int):
        size = target.stat().st_size
        if size < min_size:
            return VerificationCheck(
                "path_exists", False, f"文件大小 {size} 小于要求 {min_size}"
            )
        return VerificationCheck(
            "path_exists", True, f"路径存在且大小 {size} >= {min_size}"
        )
    return VerificationCheck("path_exists", True, f"路径存在：{path}")


def _check_path_missing(spec: dict) -> VerificationCheck:
    path = spec.get("path")
    if not isinstance(path, str) or not path:
        return VerificationCheck("path_missing", False, "验证项缺少 path")
    if Path(path).exists():
        return VerificationCheck("path_missing", False, f"路径仍然存在：{path}")
    return VerificationCheck("path_missing", True, f"路径已不存在：{path}")


def _evaluate_check(spec: object, execution: ExecutionRecord) -> VerificationCheck:
    if not isinstance(spec, dict):
        return VerificationCheck("unknown", False, "未知验证项类型")
    kind = spec.get("type")
    if kind == "path_exists":
        return _check_path_exists(spec)
    if kind == "path_missing":
        return _check_path_missing(spec)
    if kind == "execution_ok":
        ok = execution.ok is True
        detail = "执行成功" if ok else "执行记录标记为失败"
        return VerificationCheck("execution_ok", ok, detail)
    return VerificationCheck(str(kind), False, "未知验证项类型")


def rollback_backup(session: Session, proposal: ActionProposal) -> dict:
    input_json = proposal.input_json if isinstance(proposal.input_json, dict) else {}
    path = input_json.get("path")
    tool = proposal.tool_name
    if tool == "rotate_log_file":
        rotated = Path(f"{path}.1") if isinstance(path, str) and path else None
        if rotated is not None and rotated.is_file():
            os.replace(rotated, path)
            result = {"restored": True, "detail": f"已将 {path}.1 还原为 {path}"}
        else:
            result = {"restored": False, "detail": "轮转副本不存在，无需还原"}
    elif tool == "backup_log_file":
        result = {"restored": False, "detail": "备份为增量操作，无需回滚"}
    else:
        result = {"restored": False, "detail": f"工具 {tool} 无需回滚"}
    task = session.get(Task, proposal.task_id)
    AuditLog(session).append(
        task.trace_id if task is not None else "",
        "verification",
        "verification.rollback",
        f"回滚 {tool}：{result['detail']}",
        {"tool": tool, **result},
    )
    return result


def verify_recovery(
    session: Session,
    task: Task,
    proposal: ActionProposal,
    execution: ExecutionRecord,
    *,
    investigation_evidence_ids: list[int] | None = None,
    dev_allowed_roots: tuple[str, ...] = (),
) -> VerificationRecord:
    if investigation_evidence_ids:
        raise VerificationIndependenceError("验证不得复用调查阶段证据")
    raw_checks = proposal.postconditions_json or []
    checks = [_evaluate_check(item, execution) for item in raw_checks]
    if checks:
        failed = [check.name for check in checks if not check.ok]
        passed = not failed
        ok_count = len(checks) - len(failed)
        note = (
            f"{len(checks)}/{len(checks)} 项验证通过"
            if passed
            else f"{ok_count}/{len(checks)} 项验证通过，未通过：{'、'.join(failed)}"
        )
    else:
        passed = execution.ok is True
        note = "无验证项，依据执行结果"
    record = VerificationRecord(
        task_id=task.id,
        proposal_id=proposal.id,
        checks_json=[asdict(check) for check in checks],
        passed=passed,
        note=note,
        verified_at=utcnow(),
    )
    session.add(record)
    session.flush()
    AuditLog(session).append(
        task.trace_id,
        "verification",
        "verification.completed",
        f"独立验证{'通过' if passed else '未通过'}，共 {len(checks)} 项检查",
        {"passed": passed, "count": len(checks)},
    )
    if passed:
        task.status = TaskStatus.SEALED.value
    else:
        rollback_backup(session, proposal)
        task.status = TaskStatus.ROLLED_BACK.value
    task.updated_at = utcnow()
    session.flush()
    return record
