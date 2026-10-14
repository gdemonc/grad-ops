from __future__ import annotations

import json
import re
import time

import httpx

from app.config import settings

REQUEST_TIMEOUT_SECONDS = 30
RETRY_DELAY_SECONDS = 0.05
_FENCE_RE = re.compile(r"^```[a-zA-Z0-9_-]*\s*\n?(.*?)\n?```\s*$", re.DOTALL)


class LLMUnavailable(RuntimeError):
    pass


class LLMFormatError(RuntimeError):
    pass


def _strip_code_fences(text: str) -> str:
    match = _FENCE_RE.match(text.strip())
    return match.group(1) if match else text


class LLMClient:
    def __init__(self, mode=None, base_url=None, api_key=None, model=None, max_retries=2):
        self.mode = mode if mode is not None else settings.llm_mode
        self.base_url = base_url if base_url is not None else settings.llm_base_url
        self.api_key = api_key if api_key is not None else settings.llm_api_key
        self.model = model if model is not None else settings.llm_model
        self.max_retries = max_retries
        self._script: list[dict] | None = None

    def set_script(self, script: list[dict]) -> None:
        self._script = list(script)

    def complete_json(self, *, system: str, user: str, schema_hint: str) -> dict:
        if self.mode == "mock":
            return self._complete_mock(user)
        return self._complete_live(system=system, user=user, schema_hint=schema_hint)

    def _complete_mock(self, user: str) -> dict:
        if self._script is not None:
            if not self._script:
                raise LLMFormatError("mock script exhausted")
            return self._script.pop(0)
        return {"status": "noop", "echo": user[:200]}

    def _complete_live(self, *, system: str, user: str, schema_hint: str) -> dict:
        url = self.base_url.rstrip("/") + "/chat/completions"
        headers = {"Authorization": f"Bearer {self.api_key}"}
        payload = {
            "model": self.model,
            "temperature": 0,
            "response_format": {"type": "json_object"},
            "messages": [
                {"role": "system", "content": system},
                {"role": "user", "content": f"{user}\n\nJSON schema hint:\n{schema_hint}"},
            ],
        }
        last_kind = "unavailable"
        last_error: Exception | None = RuntimeError("no attempt made")
        for attempt in range(self.max_retries + 1):
            if attempt:
                time.sleep(RETRY_DELAY_SECONDS)
            try:
                resp = self._post(url, headers, payload)
            except httpx.HTTPError as exc:
                last_kind = "unavailable"
                last_error = exc
                continue
            if resp.status_code >= 400:
                last_kind = "unavailable"
                last_error = RuntimeError(f"LLM HTTP status {resp.status_code}")
                continue
            try:
                content = resp.json()["choices"][0]["message"]["content"]
                return json.loads(_strip_code_fences(content))
            except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
                last_kind = "format"
                last_error = exc
        if last_kind == "format":
            raise LLMFormatError(
                f"LLM 响应在 {self.max_retries + 1} 次尝试后仍不是有效 JSON"
            ) from last_error
        raise LLMUnavailable(
            f"LLM 服务在 {self.max_retries + 1} 次尝试后仍不可用"
        ) from last_error

    @staticmethod
    def _post(url: str, headers: dict, payload: dict) -> httpx.Response:
        client = httpx.Client(timeout=REQUEST_TIMEOUT_SECONDS)
        try:
            return client.post(url, headers=headers, json=payload)
        finally:
            client.close()
