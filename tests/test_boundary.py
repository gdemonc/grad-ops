from __future__ import annotations

import os

import pytest

from app.policy.boundary import BoundaryViolation, authorize_path


def test_allowed_log_prefix_windows(tmp_path):
    log_file = tmp_path / "var" / "log" / "app" / "app.log"
    log_file.parent.mkdir(parents=True)
    log_file.write_text("hello", encoding="utf-8")
    result = authorize_path(str(log_file), dev_allowed_roots=(str(tmp_path / "var" / "log"),))
    assert result == str(log_file.resolve())


def test_allowed_dev_root(tmp_path):
    log_file = tmp_path / "fixtures" / "logs" / "app.log"
    log_file.parent.mkdir(parents=True)
    log_file.write_text("hello", encoding="utf-8")
    result = authorize_path(str(log_file), dev_allowed_roots=(str(tmp_path / "fixtures" / "logs"),))
    assert result == str(log_file.resolve())


def test_reject_etc_passwd():
    with pytest.raises(BoundaryViolation) as exc_info:
        authorize_path("/etc/passwd")
    expected = "超出受限" if os.name == "nt" else "受保护"
    assert expected in str(exc_info.value)


@pytest.mark.parametrize(
    "raw",
    ["/boot/vmlinuz", "/usr/bin/tool", "/var/lib/pkg"],
    ids=["boot", "usr", "varlib"],
)
def test_reject_protected_prefixes(raw):
    with pytest.raises(BoundaryViolation) as exc_info:
        authorize_path(raw)
    expected = "超出受限" if os.name == "nt" else "受保护"
    assert expected in str(exc_info.value)


def test_reject_windows_system32():
    with pytest.raises(BoundaryViolation) as exc_info:
        authorize_path("C:\\Windows\\System32\\config\\SAM")
    assert "超出受限" in str(exc_info.value)


def test_reject_parent_traversal(tmp_path):
    raw = str(tmp_path / "logs" / ".." / "..")
    with pytest.raises(BoundaryViolation) as exc_info:
        authorize_path(raw)
    assert "上级跳转" in str(exc_info.value)


def test_reject_unconfigured_drive_path():
    with pytest.raises(BoundaryViolation) as exc_info:
        authorize_path("D:\\some\\random\\path.log")
    assert "超出受限" in str(exc_info.value)


def test_return_preserves_case(tmp_path):
    upper_dir = tmp_path / "VarLogUpper"
    upper_dir.mkdir()
    log_file = upper_dir / "app.log"
    log_file.write_text("hello", encoding="utf-8")
    result = authorize_path(str(log_file), dev_allowed_roots=(str(upper_dir),))
    assert result == str(log_file.resolve())
    assert "VarLogUpper" in result


def test_dev_root_not_in_protected(tmp_path):
    log_file = tmp_path / "mydev" / "etc" / "x.log"
    log_file.parent.mkdir(parents=True)
    log_file.write_text("hello", encoding="utf-8")
    result = authorize_path(str(log_file), dev_allowed_roots=(str(tmp_path / "mydev"),))
    assert result == str(log_file.resolve())


@pytest.mark.skipif(os.name == "nt", reason="Windows 无符号链接测试环境")
def test_reject_symlink_escape(tmp_path):
    dev_root = tmp_path / "dev"
    dev_root.mkdir()
    link = dev_root / "escape.log"
    link.symlink_to("/etc/passwd")
    with pytest.raises(BoundaryViolation) as exc_info:
        authorize_path(str(link), dev_allowed_roots=(str(dev_root),))
    assert "受保护" in str(exc_info.value)
