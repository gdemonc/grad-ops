from __future__ import annotations

from app.tools.base import ToolError, ToolSpec
from app.tools.k8s import K8S_SPECS
from app.tools.observation import OBSERVATION_SPECS
from app.tools.side_effect import register_side_effect_tools


class ToolRegistry:
    def __init__(self) -> None:
        self._specs: dict[str, ToolSpec] = {}

    def register(self, spec: ToolSpec) -> None:
        if spec.name in self._specs:
            raise ToolError(f"tool already registered: {spec.name}")
        self._specs[spec.name] = spec

    def get(self, name: str) -> ToolSpec:
        spec = self._specs.get(name)
        if spec is None:
            raise ToolError(f"unknown tool: {name}")
        return spec

    def all(self) -> list[ToolSpec]:
        return list(self._specs.values())

    def observation_only(self) -> list[ToolSpec]:
        return [spec for spec in self._specs.values() if not spec.has_side_effect]

    def side_effect_only(self) -> list[ToolSpec]:
        return [spec for spec in self._specs.values() if spec.has_side_effect]

    def names(self) -> list[str]:
        return list(self._specs)


def build_registry() -> ToolRegistry:
    registry = ToolRegistry()
    for spec in OBSERVATION_SPECS + K8S_SPECS:
        registry.register(spec)
    register_side_effect_tools(registry)
    return registry
