from __future__ import annotations

from pathlib import Path, PurePosixPath

ALLOWED_PREFIXES = ("/var/log/", "/tmp/")
PROTECTED_PREFIXES = ("/etc/", "/boot/", "/usr/", "/var/lib/", "/var/log/audit/")

class BoundaryViolation(PermissionError): ...


def _normalize(path: str) -> str:
    """统一为正斜杠小写路径，仅用作前缀比较键；不用于文件操作。"""
    resolved = Path(path).resolve()
    return str(PurePosixPath(resolved)).replace("\\", "/").lower()


def authorize_path(raw: str, *, dev_allowed_roots: tuple[str, ...] = ()) -> str:
    """返回原始大小写的 resolved 路径（供文件操作使用）；越界抛 BoundaryViolation。"""
    if ".." in Path(raw).parts:
        raise BoundaryViolation("路径包含上级跳转")
    resolved = Path(raw).resolve()
    normalized = str(PurePosixPath(resolved)).replace("\\", "/").lower()
    for protected in PROTECTED_PREFIXES:
        if normalized.startswith(protected.lower()):
            raise BoundaryViolation("目标路径属于受保护目录")
    allowed = ALLOWED_PREFIXES + tuple(_normalize(r) for r in dev_allowed_roots)
    if not any(normalized.startswith(prefix.lower()) for prefix in allowed):
        raise BoundaryViolation("目标路径超出受限执行范围")
    return str(resolved)
