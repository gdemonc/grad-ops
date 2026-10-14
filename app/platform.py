from __future__ import annotations

import os
import subprocess
from dataclasses import dataclass

IS_WINDOWS = os.name == "nt"


@dataclass
class CommandResult:
    exit_code: int
    stdout: str
    stderr: str
    timed_out: bool


def _as_text(value: str | bytes | None) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value


def run_command(argv: list[str], timeout: int = 10) -> CommandResult:
    try:
        completed = subprocess.run(
            argv,
            check=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=timeout,
        )
    except subprocess.TimeoutExpired as exc:
        return CommandResult(
            exit_code=-1,
            stdout=_as_text(exc.stdout),
            stderr=_as_text(exc.stderr),
            timed_out=True,
        )
    except OSError as exc:
        return CommandResult(exit_code=127, stdout="", stderr=str(exc), timed_out=False)
    return CommandResult(
        exit_code=completed.returncode,
        stdout=_as_text(completed.stdout),
        stderr=_as_text(completed.stderr),
        timed_out=False,
    )


def run_powershell(script: str, timeout: int = 10) -> CommandResult:
    if not IS_WINDOWS:
        raise RuntimeError("run_powershell is only supported on Windows")
    return run_command(
        ["powershell", "-NoProfile", "-NonInteractive", "-Command", script],
        timeout=timeout,
    )
