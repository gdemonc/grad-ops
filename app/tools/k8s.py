from __future__ import annotations

import json
import re

from app import platform
from app.config import settings
from app.enums import RiskLevel
from app.tools.base import ToolError, ToolSpec

_KUBECTL_TIMEOUT = 20
_NAMESPACE_RE = re.compile(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")
_POD_RE = re.compile(r"^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$")


def _validate_namespace(value: object) -> str:
    if not isinstance(value, str) or len(value) > 63 or not _NAMESPACE_RE.fullmatch(value):
        raise ToolError(f"invalid namespace: {value!r}")
    if value not in settings.k8s_namespaces:
        raise ToolError(f"namespace not allowed: {value!r}")
    return value


def _validate_pod(value: object) -> str:
    if not isinstance(value, str) or len(value) > 253 or not _POD_RE.fullmatch(value):
        raise ToolError(f"invalid pod name: {value!r}")
    return value


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


def _short_error(stderr: str) -> str:
    lines = [line.strip() for line in stderr.splitlines() if line.strip()]
    if not lines:
        return "unknown error"
    message = lines[-1]
    return message if len(message) <= 300 else message[:297] + "..."


def _parse_pods_json(text: str) -> list[dict]:
    try:
        payload = json.loads(text)
    except json.JSONDecodeError:
        return []
    items = payload.get("items") if isinstance(payload, dict) else None
    if not isinstance(items, list):
        return []
    rows: list[dict] = []
    for item in items:
        if not isinstance(item, dict):
            continue
        metadata = item.get("metadata") if isinstance(item.get("metadata"), dict) else {}
        status = item.get("status") if isinstance(item.get("status"), dict) else {}
        spec = item.get("spec") if isinstance(item.get("spec"), dict) else {}
        containers = status.get("containerStatuses")
        containers = [c for c in containers if isinstance(c, dict)] if isinstance(containers, list) else []
        ready = sum(1 for c in containers if c.get("ready"))
        restarts = 0
        for container in containers:
            try:
                restarts += int(container.get("restartCount") or 0)
            except (TypeError, ValueError):
                continue
        rows.append(
            {
                "name": str(metadata.get("name") or ""),
                "namespace": str(metadata.get("namespace") or ""),
                "phase": str(status.get("phase") or "Unknown"),
                "ready": f"{ready}/{len(containers)}",
                "restarts": restarts,
                "node": str(spec.get("nodeName") or ""),
            }
        )
    return rows


def list_pods(params: dict) -> dict:
    namespace = _validate_namespace(params.get("namespace", "default"))
    argv = ["kubectl", "get", "pods", "-n", namespace, "-o", "json"]
    result = platform.run_command(argv, timeout=_KUBECTL_TIMEOUT)
    if result.timed_out:
        return _fail(f"kubectl get pods timed out in {namespace}", {"namespace": namespace, "pods": []})
    if result.exit_code == 127:
        return _fail(
            f"kubectl unavailable: {_short_error(result.stderr)}",
            {"namespace": namespace, "pods": []},
        )
    pods = _parse_pods_json(result.stdout)
    if result.exit_code != 0:
        return _fail(
            f"kubectl get pods failed in {namespace}: {_short_error(result.stderr)}",
            {"namespace": namespace, "pods": pods},
        )
    return _ok(f"{len(pods)} pods in {namespace}", {"namespace": namespace, "pods": pods})


def get_pod_logs(params: dict) -> dict:
    namespace = _validate_namespace(params.get("namespace", "default"))
    pod = _validate_pod(params.get("pod"))
    lines = _validate_lines(params.get("lines", 50))
    argv = ["kubectl", "logs", "-n", namespace, pod, f"--tail={lines}"]
    result = platform.run_command(argv, timeout=_KUBECTL_TIMEOUT)
    if result.timed_out:
        return _fail(
            f"kubectl logs timed out for {pod}",
            {"namespace": namespace, "pod": pod, "lines": []},
        )
    if result.exit_code == 127:
        return _fail(
            f"kubectl unavailable: {_short_error(result.stderr)}",
            {"namespace": namespace, "pod": pod, "lines": []},
        )
    if result.exit_code != 0:
        return _fail(
            f"kubectl logs failed for {pod}: {_short_error(result.stderr)}",
            {"namespace": namespace, "pod": pod, "lines": []},
        )
    entries = result.stdout.splitlines()[-lines:]
    return _ok(
        f"{len(entries)} log lines for {pod}",
        {"namespace": namespace, "pod": pod, "lines": entries},
    )


_LIST_PODS = ToolSpec(
    name="list_pods",
    description="List Kubernetes pods in an allowed namespace with phase and restart counts.",
    parameters={
        "type": "object",
        "properties": {
            "namespace": {"type": "string", "default": "default"},
        },
        "additionalProperties": False,
    },
    risk_level=RiskLevel.R0,
    has_side_effect=False,
    handler=list_pods,
)

_GET_POD_LOGS = ToolSpec(
    name="get_pod_logs",
    description="Read the last N log lines of a Kubernetes pod in an allowed namespace.",
    parameters={
        "type": "object",
        "properties": {
            "namespace": {"type": "string", "default": "default"},
            "pod": {"type": "string", "pattern": "^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"},
            "lines": {"type": "integer", "minimum": 1, "default": 50},
        },
        "required": ["pod"],
        "additionalProperties": False,
    },
    risk_level=RiskLevel.R0,
    has_side_effect=False,
    handler=get_pod_logs,
)

K8S_SPECS = [_LIST_PODS, _GET_POD_LOGS]
