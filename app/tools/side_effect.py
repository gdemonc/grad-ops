from __future__ import annotations

import os
import time
from pathlib import Path
from typing import TYPE_CHECKING

from app.enums import RiskLevel
from app.tools.base import ToolSpec

if TYPE_CHECKING:
    from app.tools.registry import ToolRegistry


def backup_log_file(params: dict) -> dict:
    path = params.get("path")
    if not isinstance(path, str):
        return {"ok": False, "summary": "源文件不存在", "data": {}}
    source = Path(path)
    if not source.is_file():
        return {"ok": False, "summary": "源文件不存在", "data": {}}
    data = source.read_bytes()
    backup_path = f"{path}.bak.{int(time.time())}"
    Path(backup_path).write_bytes(data)
    return {
        "ok": True,
        "summary": f"已备份到 {backup_path}",
        "data": {"backup_path": backup_path, "size": len(data)},
    }


def rotate_log_file(params: dict) -> dict:
    path = params.get("path")
    if not isinstance(path, str):
        return {"ok": False, "summary": "源文件不存在", "data": {}}
    source = Path(path)
    if not source.is_file():
        return {"ok": False, "summary": "源文件不存在", "data": {}}
    rotated_to = f"{path}.1"
    os.replace(source, rotated_to)
    return {
        "ok": True,
        "summary": f"已轮转到 {rotated_to}",
        "data": {"rotated_to": rotated_to},
    }


BACKUP_LOG_FILE_SPEC = ToolSpec(
    name="backup_log_file",
    description="将日志文件复制为同目录下的 <path>.bak.<时间戳> 备份副本。",
    parameters={
        "type": "object",
        "properties": {"path": {"type": "string"}},
        "required": ["path"],
        "additionalProperties": False,
    },
    risk_level=RiskLevel.R1,
    has_side_effect=True,
    handler=backup_log_file,
)

ROTATE_LOG_FILE_SPEC = ToolSpec(
    name="rotate_log_file",
    description="轮转日志文件：将 <path> 重命名为 <path>.1，已存在的 .1 会被覆盖。",
    parameters={
        "type": "object",
        "properties": {"path": {"type": "string"}},
        "required": ["path"],
        "additionalProperties": False,
    },
    risk_level=RiskLevel.R1,
    has_side_effect=True,
    handler=rotate_log_file,
)

SIDE_EFFECT_SPECS: list[ToolSpec] = [BACKUP_LOG_FILE_SPEC, ROTATE_LOG_FILE_SPEC]


def register_side_effect_tools(registry: ToolRegistry) -> None:
    for spec in SIDE_EFFECT_SPECS:
        registry.register(spec)
