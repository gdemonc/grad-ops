from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass

from app.enums import RiskLevel


@dataclass(frozen=True)
class ToolSpec:
    name: str
    description: str
    parameters: dict
    risk_level: RiskLevel
    has_side_effect: bool
    handler: Callable[[dict], dict]


class ToolError(RuntimeError):
    def __init__(self, message: str) -> None:
        super().__init__(message)
        self.message = message
