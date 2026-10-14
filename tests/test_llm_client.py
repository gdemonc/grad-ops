from __future__ import annotations

import httpx
import pytest

from app.llm import LLMClient, LLMFormatError, LLMUnavailable


class _FakeResponse:
    def __init__(self, content: str, status_code: int = 200):
        self.status_code = status_code
        self._content = content

    def json(self) -> dict:
        return {"choices": [{"message": {"content": self._content}}]}


def _install_stub(monkeypatch, handler):
    calls = {"count": 0}

    class StubClient:
        def __init__(self, *args, **kwargs):
            pass

        def post(self, url, headers=None, json=None):
            calls["count"] += 1
            result = handler(calls["count"])
            if isinstance(result, Exception):
                raise result
            return result

        def close(self):
            pass

    monkeypatch.setattr("app.llm.client.httpx.Client", StubClient)
    return calls


def _forbid_network(monkeypatch):
    def _fail(*args, **kwargs):
        raise AssertionError("network attempted in mock mode")

    monkeypatch.setattr("app.llm.client.httpx.Client", _fail)


def _live_client(max_retries: int = 2) -> LLMClient:
    return LLMClient(
        mode="live",
        base_url="http://stub.local/v1",
        api_key="test-key",
        model="test-model",
        max_retries=max_retries,
    )


def test_mock_returns_dict(monkeypatch):
    _forbid_network(monkeypatch)
    client = LLMClient(mode="mock")
    result = client.complete_json(system="sys", user="hello world", schema_hint="{}")
    assert isinstance(result, dict)
    assert result["status"] == "noop"
    assert result["echo"] == "hello world"


def test_mock_script_queue():
    client = LLMClient(mode="mock")
    client.set_script([{"a": 1}, {"b": 2}])
    assert client.complete_json(system="s", user="u", schema_hint="{}") == {"a": 1}
    assert client.complete_json(system="s", user="u", schema_hint="{}") == {"b": 2}
    with pytest.raises(LLMFormatError, match="mock script exhausted"):
        client.complete_json(system="s", user="u", schema_hint="{}")


def test_live_parses_content(monkeypatch):
    calls = _install_stub(monkeypatch, lambda n: _FakeResponse('{"ok": true}'))
    result = _live_client().complete_json(system="s", user="u", schema_hint="{}")
    assert result == {"ok": True}
    assert calls["count"] == 1


def test_live_strips_code_fences(monkeypatch):
    _install_stub(monkeypatch, lambda n: _FakeResponse('```json\n{"x":1}\n```'))
    result = _live_client().complete_json(system="s", user="u", schema_hint="{}")
    assert result == {"x": 1}


def test_live_timeout_retries_then_unavailable(monkeypatch):
    calls = _install_stub(monkeypatch, lambda n: httpx.ConnectTimeout("boom"))
    client = _live_client(max_retries=2)
    with pytest.raises(LLMUnavailable):
        client.complete_json(system="s", user="u", schema_hint="{}")
    assert calls["count"] == 3


def test_live_bad_json_retries_then_format_error(monkeypatch):
    calls = _install_stub(monkeypatch, lambda n: _FakeResponse("definitely not json"))
    client = _live_client(max_retries=2)
    with pytest.raises(LLMFormatError):
        client.complete_json(system="s", user="u", schema_hint="{}")
    assert calls["count"] == 3
