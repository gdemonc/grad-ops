from __future__ import annotations

import ast
import re
from pathlib import Path

from app.policy.rules import SIDE_EFFECT_WHITELIST

ROOT = Path(__file__).resolve().parents[1]
APP = ROOT / "app"
TESTS = ROOT / "tests"

FORBIDDEN_IMPORT_PATTERNS = [
    re.compile(r"^\s*from\s+app\.llm\b"),
    re.compile(r"^\s*import\s+app\.llm\b"),
    re.compile(r"^\s*from\s+httpx\b"),
    re.compile(r"^\s*import\s+httpx\b"),
    re.compile(r"^\s*from\s+app\s+import\s+.*\bllm\b"),
]

INVESTIGATION_IMPORT_PATTERNS = [
    re.compile(r"^\s*from\s+app\.investigation\b"),
    re.compile(r"^\s*import\s+app\.investigation\b"),
    re.compile(r"^\s*from\s+app\s+import\s+.*\binvestigation\b"),
]


def _python_files(directory: Path) -> list[Path]:
    return sorted(p for p in directory.rglob("*.py") if "__pycache__" not in p.parts)


def _offending_lines(source: str, patterns: list[re.Pattern[str]]) -> list[str]:
    hits = []
    for line in source.splitlines():
        if any(pattern.search(line) for pattern in patterns):
            hits.append(line)
    return hits


def test_policy_has_no_llm_dependency():
    offenders: dict[str, list[str]] = {}
    for path in _python_files(APP / "policy"):
        hits = _offending_lines(path.read_text(encoding="utf-8"), FORBIDDEN_IMPORT_PATTERNS)
        if hits:
            offenders[str(path.relative_to(ROOT))] = hits
    assert not offenders, f"app/policy must stay model-free, found imports: {offenders}"


def test_verification_does_not_import_investigation():
    source = (APP / "verification.py").read_text(encoding="utf-8")
    hits = _offending_lines(source, INVESTIGATION_IMPORT_PATTERNS)
    assert not hits, f"verification must be independent of investigation, found: {hits}"


def test_executor_no_llm_or_investigation_import():
    source = (APP / "executor.py").read_text(encoding="utf-8")
    hits = _offending_lines(source, FORBIDDEN_IMPORT_PATTERNS + INVESTIGATION_IMPORT_PATTERNS)
    assert not hits, f"executor must stay deterministic, found: {hits}"


def test_executor_gates_before_handler():
    source = (APP / "executor.py").read_text(encoding="utf-8")
    tree = ast.parse(source)
    func = next(
        (node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "execute_approved"),
        None,
    )
    assert func is not None, "execute_approved not found in app/executor.py"
    body_source = ast.get_source_segment(source, func) or ""
    handler_pos = body_source.find("_run_handler(")
    assert handler_pos != -1, "execute_approved must dispatch to _run_handler"
    gate_names = re.findall(r"_gate_\w+\(", body_source)
    assert gate_names, "execute_approved must run gates"
    for gate in set(gate_names):
        gate_pos = body_source.find(gate)
        assert gate_pos < handler_pos, f"gate {gate} must run before handler dispatch"


def test_side_effect_tools_have_test_coverage():
    assert SIDE_EFFECT_WHITELIST, "SIDE_EFFECT_WHITELIST must not be empty (guard would be vacuous)"
    test_sources = {
        path.name: path.read_text(encoding="utf-8")
        for path in sorted(TESTS.glob("*.py"))
        if path.name != "__init__.py"
    }
    for tool_name in sorted(SIDE_EFFECT_WHITELIST):
        covering = [name for name, src in test_sources.items() if tool_name in src]
        assert covering, f"side-effect tool {tool_name!r} has no test coverage under tests/"
