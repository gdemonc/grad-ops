from __future__ import annotations

import re
from pathlib import Path

from app import platform
from app.config import settings
from app.enums import RiskLevel
from app.tools.base import ToolError, ToolSpec

_SERVICE_NAME_RE = re.compile(r"[A-Za-z0-9_.@-]+")
_PID_IN_SS_RE = re.compile(r"pid=(\d+)")
_PROCESS_IN_SS_RE = re.compile(r'\(\("([^"]+)"')

_WINDOWS_SERVICE_STATES = {
    "Running",
    "Stopped",
    "Paused",
    "StartPending",
    "StopPending",
    "ContinuePending",
    "PausePending",
}


def _validate_service_name(service: object) -> str:
    if not isinstance(service, str) or not _SERVICE_NAME_RE.fullmatch(service):
        raise ToolError(f"invalid service name: {service!r}")
    return service


def _validate_lines(lines: object) -> int:
    try:
        value = int(lines)  # type: ignore[arg-type]
    except (TypeError, ValueError) as exc:
        raise ToolError(f"invalid line count: {lines!r}") from exc
    return max(1, min(value, 1000))


def _ok(summary: str, data: dict) -> dict:
    return {"ok": True, "summary": summary, "data": data}


def _fail(summary: str, data: dict) -> dict:
    return {"ok": False, "summary": summary, "data": data}


def _split_host_port(value: str) -> tuple[str, int | None]:
    if value.startswith("[") and "]" in value:
        host, separator, rest = value[1:].partition("]")
        if not separator or not rest.startswith(":"):
            return value, None
        port_text = rest[1:]
    else:
        if ":" not in value:
            return value, None
        host, port_text = value.rsplit(":", 1)
    try:
        return host, int(port_text)
    except ValueError:
        return host, None


def _parse_ss_output(text: str) -> list[dict]:
    rows: list[dict] = []
    for line in text.splitlines():
        parts = line.split(None, 5)
        if len(parts) < 5:
            continue
        address, port = _split_host_port(parts[3])
        if port is None:
            continue
        extra = parts[5] if len(parts) > 5 else ""
        pid_match = _PID_IN_SS_RE.search(extra)
        process_match = _PROCESS_IN_SS_RE.search(extra)
        rows.append(
            {
                "address": address,
                "port": port,
                "pid": int(pid_match.group(1)) if pid_match else None,
                "process": process_match.group(1) if process_match else "unknown",
            }
        )
    return rows


def _parse_windows_listeners(text: str) -> list[dict]:
    rows: list[dict] = []
    for line in text.splitlines():
        parts = line.split()
        if len(parts) != 3 or not parts[1].isdigit() or not parts[2].isdigit():
            continue
        rows.append(
            {
                "address": parts[0],
                "port": int(parts[1]),
                "pid": int(parts[2]),
                "process": "unknown",
            }
        )
    return rows


def _windows_process_names() -> dict[int, str]:
    result = platform.run_powershell("Get-Process | Select-Object Id,ProcessName")
    names: dict[int, str] = {}
    if result.timed_out or result.exit_code != 0:
        return names
    for line in result.stdout.splitlines():
        parts = line.split()
        if len(parts) == 2 and parts[0].isdigit():
            names[int(parts[0])] = parts[1]
    return names


def list_listening_ports(params: dict) -> dict:
    if platform.IS_WINDOWS:
        script = (
            "Get-NetTCPConnection -State Listen | "
            "Select-Object LocalAddress,LocalPort,OwningProcess"
        )
        result = platform.run_powershell(script)
        if result.timed_out:
            return _fail("listening port query timed out", {"ports": []})
        if result.exit_code != 0:
            return _fail(
                f"listening port query failed: {result.stderr.strip()}", {"ports": []}
            )
        rows = _parse_windows_listeners(result.stdout)
        names = _windows_process_names()
        for row in rows:
            if row["pid"] in names:
                row["process"] = names[row["pid"]]
        return _ok(f"{len(rows)} listening ports", {"ports": rows})
    result = platform.run_command(["ss", "-Hlnpt"])
    if result.timed_out:
        return _fail("ss timed out", {"ports": []})
    rows = _parse_ss_output(result.stdout)
    if result.exit_code != 0:
        return _fail(f"ss failed: {result.stderr.strip()}", {"ports": rows})
    return _ok(f"{len(rows)} listening ports", {"ports": rows})


def _parse_ps_output(text: str) -> list[dict]:
    rows: list[dict] = []
    for line in text.splitlines():
        parts = line.split()
        if len(parts) < 5:
            continue
        try:
            pid = int(parts[0])
            cpu = float(parts[-2])
            mem = float(parts[-1])
        except ValueError:
            continue
        rows.append(
            {
                "pid": pid,
                "name": " ".join(parts[2:-2]),
                "user": parts[1],
                "cpu": cpu,
                "mem": mem,
            }
        )
    return rows


def _parse_windows_processes(text: str) -> list[dict]:
    rows: list[dict] = []
    for line in text.splitlines():
        parts = line.split()
        if len(parts) < 4 or not parts[0].isdigit():
            continue
        rows.append(
            {
                "pid": int(parts[0]),
                "name": parts[1],
                "cpu": _to_float(parts[2]),
                "mem": _to_float(parts[3]),
            }
        )
    return rows


def _to_float(value: str) -> float | None:
    try:
        return float(value)
    except ValueError:
        return None


def list_processes(params: dict) -> dict:
    if platform.IS_WINDOWS:
        script = (
            "Get-Process | Sort-Object CPU -Descending | "
            "Select-Object -First 20 Id,ProcessName,CPU,WorkingSet"
        )
        result = platform.run_powershell(script)
        if result.timed_out:
            return _fail("process query timed out", {"processes": []})
        if result.exit_code != 0:
            return _fail(
                f"process query failed: {result.stderr.strip()}", {"processes": []}
            )
        rows = _parse_windows_processes(result.stdout)
        return _ok(f"{len(rows)} processes", {"processes": rows})
    result = platform.run_command(["ps", "-eo", "pid,user,comm,%cpu,%mem", "--sort=-%cpu"])
    if result.timed_out:
        return _fail("ps timed out", {"processes": []})
    rows = _parse_ps_output(result.stdout)
    if result.exit_code != 0:
        return _fail(f"ps failed: {result.stderr.strip()}", {"processes": rows})
    return _ok(f"{len(rows)} processes", {"processes": rows})


def _parse_df_output(text: str) -> list[dict]:
    rows: list[dict] = []
    for line in text.splitlines():
        if line.startswith("Filesystem"):
            continue
        parts = line.split(None, 5)
        if len(parts) < 6:
            continue
        pct_text = parts[4]
        pct = int(pct_text[:-1]) if pct_text.endswith("%") and pct_text[:-1].isdigit() else None
        rows.append(
            {
                "mount": parts[5],
                "total": parts[1],
                "used": parts[2],
                "avail": parts[3],
                "pct": pct,
            }
        )
    return rows


def _parse_windows_drives(text: str) -> list[dict]:
    rows: list[dict] = []
    for line in text.splitlines():
        parts = line.split()
        if len(parts) != 5 or parts[3] != "FileSystem":
            continue
        used = _to_float(parts[1])
        avail = _to_float(parts[2])
        if used is None or avail is None:
            continue
        rows.append(
            {
                "mount": parts[4],
                "total": str(round(used + avail, 2)),
                "used": parts[1],
                "avail": parts[2],
                "pct": None,
            }
        )
    return rows


def disk_usage(params: dict) -> dict:
    if platform.IS_WINDOWS:
        result = platform.run_powershell("Get-PSDrive -PSProvider FileSystem")
        if result.timed_out:
            return _fail("disk usage query timed out", {"disks": []})
        if result.exit_code != 0:
            return _fail(
                f"disk usage query failed: {result.stderr.strip()}", {"disks": []}
            )
        rows = _parse_windows_drives(result.stdout)
        return _ok(f"{len(rows)} mounts", {"disks": rows})
    result = platform.run_command(["df", "-hP"])
    if result.timed_out:
        return _fail("df timed out", {"disks": []})
    rows = _parse_df_output(result.stdout)
    if result.exit_code != 0:
        return _fail(f"df failed: {result.stderr.strip()}", {"disks": rows})
    return _ok(f"{len(rows)} mounts", {"disks": rows})


def _journal_command(service: str, lines: int) -> list[str]:
    return ["journalctl", "-u", service, "-n", str(lines), "--no-pager"]


def _read_service_log_windows(service: str, lines: int) -> dict:
    command = _journal_command(service, lines)
    command_text = " ".join(command)
    mapping = getattr(settings, "service_log_files", None)
    path_text = mapping.get(service) if isinstance(mapping, dict) else None
    if not path_text:
        return _fail(
            "journal unavailable on Windows: no log file mapping configured for "
            f"'{service}'; run on Linux: {command_text}",
            {"service": service, "lines": [], "command": command_text},
        )
    path = Path(path_text)
    if not path.is_file():
        return _fail(
            f"mapped log file for '{service}' not found: {path}",
            {"service": service, "lines": [], "command": command_text},
        )
    try:
        content = path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        return _fail(
            f"cannot read log file for '{service}': {exc}",
            {"service": service, "lines": [], "command": command_text},
        )
    entries = content.splitlines()[-lines:]
    return _ok(f"{len(entries)} log lines for {service}", {"service": service, "lines": entries})


def read_service_log(params: dict) -> dict:
    service = _validate_service_name(params.get("service"))
    lines = _validate_lines(params.get("lines", 50))
    if platform.IS_WINDOWS:
        return _read_service_log_windows(service, lines)
    result = platform.run_command(_journal_command(service, lines))
    if result.timed_out:
        return _fail(f"journalctl timed out for {service}", {"service": service, "lines": []})
    if result.exit_code != 0:
        return _fail(
            f"journalctl failed for {service}: {result.stderr.strip()}",
            {"service": service, "lines": []},
        )
    entries = result.stdout.splitlines()[-lines:]
    return _ok(f"{len(entries)} log lines for {service}", {"service": service, "lines": entries})


def _parse_windows_service_status(text: str) -> str:
    for line in text.splitlines():
        parts = line.split()
        if parts and parts[0] in _WINDOWS_SERVICE_STATES:
            return parts[0]
    return "not_found"


def check_service_health(params: dict) -> dict:
    service = _validate_service_name(params.get("service"))
    if platform.IS_WINDOWS:
        result = platform.run_powershell(f"Get-Service -Name '{service}'")
        if result.timed_out:
            return _fail(
                f"Get-Service timed out for {service}",
                {"service": service, "status": "unknown", "healthy": False},
            )
        if result.exit_code == 127:
            return _fail(
                f"powershell unavailable: {result.stderr.strip()}",
                {"service": service, "status": "unknown", "healthy": False},
            )
        status = _parse_windows_service_status(result.stdout)
        healthy = status == "Running"
        return _ok(
            f"{service}: {status}",
            {"service": service, "status": status, "healthy": healthy},
        )
    result = platform.run_command(["systemctl", "is-active", service])
    if result.timed_out:
        return _fail(
            f"systemctl timed out for {service}",
            {"service": service, "status": "unknown", "healthy": False},
        )
    status = result.stdout.strip().splitlines()[0].strip() if result.stdout.strip() else ""
    if not status and result.exit_code != 0:
        return _fail(
            f"systemctl failed for {service}: {result.stderr.strip()}",
            {"service": service, "status": "error", "healthy": False},
        )
    status = status or "unknown"
    healthy = status == "active"
    return _ok(f"{service}: {status}", {"service": service, "status": status, "healthy": healthy})


_LIST_LISTENING_PORTS = ToolSpec(
    name="list_listening_ports",
    description="List TCP listening sockets with owning process attribution.",
    parameters={"type": "object", "properties": {}, "additionalProperties": False},
    risk_level=RiskLevel.R0,
    has_side_effect=False,
    handler=list_listening_ports,
)

_LIST_PROCESSES = ToolSpec(
    name="list_processes",
    description="List running processes ordered by CPU usage.",
    parameters={"type": "object", "properties": {}, "additionalProperties": False},
    risk_level=RiskLevel.R0,
    has_side_effect=False,
    handler=list_processes,
)

_DISK_USAGE = ToolSpec(
    name="disk_usage",
    description="Show filesystem capacity and usage for all mounts.",
    parameters={"type": "object", "properties": {}, "additionalProperties": False},
    risk_level=RiskLevel.R0,
    has_side_effect=False,
    handler=disk_usage,
)

_READ_SERVICE_LOG = ToolSpec(
    name="read_service_log",
    description="Read the last N journal lines of a service.",
    parameters={
        "type": "object",
        "properties": {
            "service": {"type": "string", "pattern": "^[A-Za-z0-9_.@-]+$"},
            "lines": {"type": "integer", "minimum": 1, "default": 50},
        },
        "required": ["service"],
        "additionalProperties": False,
    },
    risk_level=RiskLevel.R0,
    has_side_effect=False,
    handler=read_service_log,
)

_CHECK_SERVICE_HEALTH = ToolSpec(
    name="check_service_health",
    description="Check whether a service is currently active.",
    parameters={
        "type": "object",
        "properties": {"service": {"type": "string", "pattern": "^[A-Za-z0-9_.@-]+$"}},
        "required": ["service"],
        "additionalProperties": False,
    },
    risk_level=RiskLevel.R0,
    has_side_effect=False,
    handler=check_service_health,
)

OBSERVATION_SPECS = [
    _LIST_LISTENING_PORTS,
    _LIST_PROCESSES,
    _DISK_USAGE,
    _READ_SERVICE_LOG,
    _CHECK_SERVICE_HEALTH,
]
