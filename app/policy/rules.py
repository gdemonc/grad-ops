from __future__ import annotations

import re

from app.enums import RiskLevel

SIDE_EFFECT_WHITELIST = {"backup_log_file", "rotate_log_file"}

OBSERVATION_TOOLS = {
    "list_listening_ports",
    "list_processes",
    "disk_usage",
    "read_service_log",
    "check_service_health",
}

RISK_DECISION = {
    RiskLevel.R0: "ALLOW_AUTO",
    RiskLevel.R1: "CONDITIONAL",
    RiskLevel.R2: "NEEDS_APPROVAL",
    RiskLevel.R3: "NEEDS_APPROVAL",
    RiskLevel.R4: "DENY",
}

FORBIDDEN_PATTERNS: list[tuple[str, str]] = [
    (
        "rm_rf_root",
        (
            r"rm\s+(-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*|-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*)"
            r"\s+(/\s|/\*|/[\s*]|/$)"
        ),
    ),
    ("mkfs", r"\bmkfs"),
    ("fork_bomb", r":\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;"),
    ("chmod_777_root", r"chmod\s+(-R\s+)?777\s+/(\s|$)"),
    ("dd_dev", r"\bdd\b.*\bof=/dev/"),
]

_COMPILED_PATTERNS = [(rule_id, re.compile(pattern)) for rule_id, pattern in FORBIDDEN_PATTERNS]


def scan_forbidden(text: str) -> list[str]:
    return [rule_id for rule_id, pattern in _COMPILED_PATTERNS if pattern.search(text)]
