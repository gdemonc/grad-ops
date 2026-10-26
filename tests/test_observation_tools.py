from __future__ import annotations

import pytest

from app import platform
from app.enums import RiskLevel
from app.tools import ToolError, build_registry
from app.tools.observation import (
    check_service_health,
    disk_usage,
    list_listening_ports,
    list_processes,
    read_service_log,
)

SS_OUTPUT = (
    'LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=123,fd=3))\n'
    "LISTEN 0 128 *:8080 *:*\n"
)

PS_OUTPUT = (
    "    PID USER     COMMAND         %CPU %MEM\n"
    "   1024 root     python3          2.5  4.1\n"
    "    512 www-data nginx            0.0  1.2\n"
)

DF_OUTPUT = (
    "Filesystem     Size  Used Avail Use% Mounted on\n"
    "/dev/vda1       40G   12G   26G  32% /\n"
    "tmpfs          1.9G     0  1.9G   0% /dev/shm\n"
)


def _result(stdout: str = "", stderr: str = "", exit_code: int = 0) -> platform.CommandResult:
    return platform.CommandResult(
        exit_code=exit_code, stdout=stdout, stderr=stderr, timed_out=False
    )


def test_list_listening_ports_parses_ss_output(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout=SS_OUTPUT)

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = list_listening_ports({})
    assert result["ok"] is True
    assert captured["argv"] == ["ss", "-Hlnpt"]
    assert result["data"]["ports"] == [
        {"address": "0.0.0.0", "port": 22, "pid": 123, "process": "sshd"},
        {"address": "*", "port": 8080, "pid": None, "process": "unknown"},
    ]


def test_list_processes_parses_ps_output(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout=PS_OUTPUT)

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = list_processes({})
    assert result["ok"] is True
    assert captured["argv"] == ["ps", "-eo", "pid,user,comm,%cpu,%mem", "--sort=-%cpu"]
    assert result["data"]["processes"] == [
        {"pid": 1024, "name": "python3", "user": "root", "cpu": 2.5, "mem": 4.1},
        {"pid": 512, "name": "nginx", "user": "www-data", "cpu": 0.0, "mem": 1.2},
    ]


def test_disk_usage_parses_df_output(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout=DF_OUTPUT)

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = disk_usage({})
    assert result["ok"] is True
    assert captured["argv"] == ["df", "-hP"]
    assert result["data"]["disks"] == [
        {"mount": "/", "total": "40G", "used": "12G", "avail": "26G", "pct": 32},
        {"mount": "/dev/shm", "total": "1.9G", "used": "0", "avail": "1.9G", "pct": 0},
    ]


def test_read_service_log_linux(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)
    captured: dict = {}

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        captured["argv"] = argv
        return _result(stdout="line1\nline2\nline3\n")

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = read_service_log({"service": "nginx.service", "lines": 2})
    assert result["ok"] is True
    assert captured["argv"] == ["journalctl", "-u", "nginx.service", "-n", "2", "--no-pager"]
    assert result["data"]["service"] == "nginx.service"
    assert result["data"]["lines"] == ["line2", "line3"]


@pytest.mark.parametrize("service", ["../../etc/passwd", "a; rm -rf /"])
def test_read_service_log_rejects_injection(monkeypatch, service):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)

    def boom(*args, **kwargs):
        raise AssertionError("run_command must not be called")

    monkeypatch.setattr(platform, "run_command", boom)
    with pytest.raises(ToolError):
        read_service_log({"service": service})


def test_check_service_health_linux(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        assert argv == ["systemctl", "is-active", "sshd"]
        return _result(stdout="active\n")

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = check_service_health({"service": "sshd"})
    assert result["ok"] is True
    assert result["data"] == {"service": "sshd", "status": "active", "healthy": True}


@pytest.mark.parametrize("service", ["../../etc/passwd", "a; rm -rf /"])
def test_check_service_health_rejects_injection(monkeypatch, service):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)

    def boom(*args, **kwargs):
        raise AssertionError("run_command must not be called")

    monkeypatch.setattr(platform, "run_command", boom)
    with pytest.raises(ToolError):
        check_service_health({"service": service})


def test_registry_observation_and_side_effect_tools():
    registry = build_registry()
    tools = registry.observation_only()
    assert len(tools) == 7
    assert all(tool.has_side_effect is False for tool in tools)
    assert all(tool.risk_level == RiskLevel.R0 for tool in tools)
    assert set(registry.names()) == {
        "list_listening_ports",
        "list_processes",
        "disk_usage",
        "read_service_log",
        "check_service_health",
        "list_pods",
        "get_pod_logs",
        "backup_log_file",
        "rotate_log_file",
    }
    assert len(registry.observation_only()) == 7
    assert {spec.name for spec in registry.side_effect_only()} == {
        "backup_log_file",
        "rotate_log_file",
    }
    assert all(spec.has_side_effect for spec in registry.side_effect_only())


def test_run_command_timeout():
    result = platform.run_command(["python", "-c", "import time; time.sleep(5)"], timeout=1)
    assert result.timed_out is True
    assert isinstance(result.stdout, str)
    assert isinstance(result.stderr, str)


def test_read_service_log_windows_unavailable(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", True)

    def boom(*args, **kwargs):
        raise AssertionError("commands must not be called")

    monkeypatch.setattr(platform, "run_command", boom)
    monkeypatch.setattr(platform, "run_powershell", boom)
    result = read_service_log({"service": "nginx"})
    assert result["ok"] is False
    assert "Windows" in result["summary"]
    assert "journalctl -u nginx -n 50 --no-pager" in result["summary"]
    assert result["data"]["command"] == "journalctl -u nginx -n 50 --no-pager"
    assert result["data"]["lines"] == []


def test_check_service_health_windows_not_found(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", True)

    def fake_run_command(argv: list[str], timeout: int = 10) -> platform.CommandResult:
        assert argv[:1] == ["powershell"]
        return _result(exit_code=1, stderr="Cannot find any service with the name")

    monkeypatch.setattr(platform, "run_command", fake_run_command)
    result = check_service_health({"service": "no-such-service"})
    assert result["ok"] is True
    assert result["data"] == {
        "service": "no-such-service",
        "status": "not_found",
        "healthy": False,
    }


def test_run_powershell_requires_windows(monkeypatch):
    monkeypatch.setattr(platform, "IS_WINDOWS", False)
    with pytest.raises(RuntimeError):
        platform.run_powershell("Get-Service")
