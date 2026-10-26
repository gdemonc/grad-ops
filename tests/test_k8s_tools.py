from __future__ import annotations

import json

import pytest

from app import platform
from app.config import settings
from app.enums import RiskLevel
from app.tools import ToolError, build_registry
from app.tools.k8s import get_pod_logs, list_pods

PODS_JSON = json.dumps(
    {
        "items": [
            {
                "metadata": {"name": "web-7d9f", "namespace": "default"},
                "spec": {"nodeName": "node-a"},
                "status": {
                    "phase": "Running",
                    "containerStatuses": [
                        {"ready": True, "restartCount": 3},
                        {"ready": False, "restartCount": 1},
                    ],
                },
            },
            {
                "metadata": {"name": "job-1", "namespace": "default"},
                "spec": {},
                "status": {"phase": "Pending"},
            },
        ]
    }
)


def _result(stdout: str = "", stderr: str = "", exit_code: int = 0) -> platform.CommandResult:
    return platform.CommandResult(
        exit_code=exit_code, stdout=stdout, stderr=stderr, timed_out=False
    )


def test_list_pods_parses_json(monkeypatch):
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout=PODS_JSON)

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = list_pods({"namespace": "default"})
    assert result["ok"] is True
    assert captured["argv"] == ["kubectl", "get", "pods", "-n", "default", "-o", "json"]
    assert result["data"]["pods"] == [
        {
            "name": "web-7d9f",
            "namespace": "default",
            "phase": "Running",
            "ready": "1/2",
            "restarts": 4,
            "node": "node-a",
        },
        {
            "name": "job-1",
            "namespace": "default",
            "phase": "Pending",
            "ready": "0/0",
            "restarts": 0,
            "node": "",
        },
    ]


def test_list_pods_defaults_to_default_namespace(monkeypatch):
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout=json.dumps({"items": []}))

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = list_pods({})
    assert result["ok"] is True
    assert captured["argv"] == ["kubectl", "get", "pods", "-n", "default", "-o", "json"]


@pytest.mark.parametrize("namespace", ["../../etc", "Default", "a b", "", "x" * 64])
def test_list_pods_rejects_invalid_namespace(monkeypatch, namespace):
    def boom(*args, **kwargs):
        raise AssertionError("run_command must not be called")

    monkeypatch.setattr(platform, "run_command", boom)
    with pytest.raises(ToolError):
        list_pods({"namespace": namespace})


def test_list_pods_rejects_namespace_outside_allowlist(monkeypatch):
    def boom(*args, **kwargs):
        raise AssertionError("run_command must not be called")

    monkeypatch.setattr(platform, "run_command", boom)
    with pytest.raises(ToolError):
        list_pods({"namespace": "kube-system"})


def test_namespace_allowlist_is_configurable(monkeypatch):
    monkeypatch.setattr(settings, "k8s_namespaces", ["prod"])
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout=json.dumps({"items": []}))

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    assert list_pods({"namespace": "prod"})["ok"] is True
    assert captured["argv"][4] == "prod"
    with pytest.raises(ToolError):
        list_pods({"namespace": "default"})


def test_list_pods_reports_missing_kubectl(monkeypatch):
    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        return _result(exit_code=127, stderr="[WinError 2] The system cannot find the file")

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = list_pods({"namespace": "default"})
    assert result["ok"] is False
    assert "kubectl unavailable" in result["summary"]
    assert result["data"]["pods"] == []


def test_list_pods_reports_cluster_error(monkeypatch):
    noisy = "E1025 retry memcache.go\nE1025 retry memcache.go\nThe connection to the server was refused"

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        return _result(exit_code=1, stderr=noisy)

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = list_pods({"namespace": "default"})
    assert result["ok"] is False
    assert result["summary"] == (
        "kubectl get pods failed in default: The connection to the server was refused"
    )


def test_list_pods_tolerates_malformed_json(monkeypatch):
    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        return _result(stdout="not json")

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = list_pods({"namespace": "default"})
    assert result["ok"] is True
    assert result["data"]["pods"] == []


def test_get_pod_logs_builds_argv_and_truncates(monkeypatch):
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout="line1\nline2\nline3\n")

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = get_pod_logs({"namespace": "default", "pod": "web-7d9f", "lines": 2})
    assert result["ok"] is True
    assert captured["argv"] == ["kubectl", "logs", "-n", "default", "web-7d9f", "--tail=2"]
    assert result["data"]["lines"] == ["line2", "line3"]


def test_get_pod_logs_clamps_lines(monkeypatch):
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout="x\n")

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    get_pod_logs({"pod": "web-7d9f", "lines": 5000})
    assert captured["argv"][-1] == "--tail=1000"


@pytest.mark.parametrize("pod", ["../../etc/passwd", "a; rm -rf /", "Web-7d9f", ""])
def test_get_pod_logs_rejects_injection(monkeypatch, pod):
    def boom(*args, **kwargs):
        raise AssertionError("run_command must not be called")

    monkeypatch.setattr(platform, "run_command", boom)
    with pytest.raises(ToolError):
        get_pod_logs({"pod": pod})


def test_k8s_tools_registered_as_readonly():
    registry = build_registry()
    for name in ("list_pods", "get_pod_logs"):
        spec = registry.get(name)
        assert spec.has_side_effect is False
        assert spec.risk_level == RiskLevel.R0
