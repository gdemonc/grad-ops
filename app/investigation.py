from __future__ import annotations

import time
from dataclasses import dataclass

from sqlalchemy.orm import Session

from app.audit import AuditLog, stable_hash
from app.config import settings
from app.llm.client import LLMClient
from app.models import Evidence, Task
from app.tools.base import ToolError
from app.tools.registry import ToolRegistry, build_registry

DEFAULT_TIMEOUT_SECONDS = 120

SYSTEM_PROMPT = (
    "你是运维调查员。你的职责是通过只读观测工具收集证据，"
    "在证据充分后给出根因结论与可选的处置提议。\n"
    "可用工具（全部为只读观测，严禁请求任何有副作用的工具）：\n"
    "{tool_list}\n"
    "响应规则：\n"
    "1. 只输出一个 JSON 对象，不要输出任何其他文本或 Markdown 代码块。\n"
    "2. 需要继续取证时输出："
    '{"action":"tool_call","tool":"<工具名>","params":{...}}。\n'
    "3. 得出结论时输出："
    '{"action":"conclude","root_cause":"...","claims":[{"statement":"...",'
    '"evidence_ids":[<证据id>]}],"confidence":<0到1>,"proposed_action":'
    '{"tool":"...","input":{...},"risk_level":"R0"|"R1"|"R2"|"R3"|"R4",'
    '"rationale":"...","policy_ref":null|"...","preconditions":'
    '[{"evidence_id":1}],"postconditions":[...],"rollback":{...}}|null}。\n'
    "4. claims 中的 evidence_ids 必须引用下方证据列表中真实存在的编号，不得虚构证据。\n"
    "5. proposed_action 可为 null；若给出，risk_level 必须是 R0 到 R4 之一。\n"
)

SCHEMA_HINT = (
    '{"action":"tool_call","tool":str,"params":{...}} 或 '
    '{"action":"conclude","root_cause":str,"claims":[{"statement":str,'
    '"evidence_ids":[int]}],"confidence":0-1,"proposed_action":{...}|null}'
)


@dataclass
class InvestigationResult:
    task_id: int
    evidence_ids: list[int]
    tool_call_names: list[str]
    conclusions: dict
    truncated: bool
    iterations: int


class InvestigationEngine:
    def __init__(
        self,
        session: Session,
        llm: LLMClient,
        registry: ToolRegistry | None = None,
        *,
        max_iterations: int | None = None,
        max_tool_calls: int | None = None,
        clock=None,
        timeout_seconds: float | None = None,
    ) -> None:
        self.session = session
        self.llm = llm
        self.registry = registry if registry is not None else build_registry()
        self.max_iterations = (
            max_iterations if max_iterations is not None else settings.investigation_max_iterations
        )
        self.max_tool_calls = (
            max_tool_calls if max_tool_calls is not None else settings.investigation_max_tool_calls
        )
        self.clock = clock if clock is not None else time.monotonic
        self.timeout_seconds = (
            timeout_seconds if timeout_seconds is not None else DEFAULT_TIMEOUT_SECONDS
        )

    def run(self, task: Task) -> InvestigationResult:
        audit = AuditLog(self.session)
        trace_id = task.trace_id
        audit.append(
            trace_id,
            "investigation",
            "investigation.start",
            f"开始调查任务 {task.id}",
            {"request_text": task.request_text},
        )
        evidence_ids: list[int] = []
        evidence_entries: list[tuple[int, str]] = []
        tool_call_names: list[str] = []
        conclusions: dict = {}
        truncated = False
        concluded = False
        iterations = 0
        tool_call_count = 0
        start = self.clock()
        system = self._system_prompt()
        for _ in range(self.max_iterations):
            if tool_call_count >= self.max_tool_calls:
                truncated = True
                break
            if self.clock() - start > self.timeout_seconds:
                truncated = True
                break
            iterations += 1
            response = self.llm.complete_json(
                system=system,
                user=self._user_prompt(task, evidence_entries),
                schema_hint=SCHEMA_HINT,
            )
            action = response.get("action") if isinstance(response, dict) else None
            if action == "tool_call":
                outcome = self._handle_tool_call(
                    audit, trace_id, task, response, evidence_ids, evidence_entries, tool_call_names
                )
                if outcome == "abort":
                    conclusions = {
                        "root_cause": "调查中止：模型请求了非只读工具",
                        "claims": [],
                        "confidence": 0.0,
                        "proposed_action": None,
                    }
                    truncated = True
                    break
                tool_call_count += 1
                continue
            if action == "conclude":
                conclusions = {
                    "root_cause": str(response.get("root_cause", "")),
                    "claims": response.get("claims")
                    if isinstance(response.get("claims"), list)
                    else [],
                    "confidence": response.get("confidence", 0.0),
                    "proposed_action": response.get("proposed_action"),
                }
                concluded = True
                audit.append(
                    trace_id,
                    "investigation",
                    "investigation.conclude",
                    f"调查得出结论：{conclusions['root_cause']}",
                    dict(conclusions),
                )
                break
            audit.append(
                trace_id,
                "investigation",
                "investigation.invalid_response",
                f"模型返回了无效响应：action={action!r}",
                {"response": response if isinstance(response, dict) else {}},
            )
            conclusions = {
                "root_cause": "调查中止：模型返回了无效响应",
                "claims": [],
                "confidence": 0.0,
                "proposed_action": None,
            }
            truncated = True
            break
        if not concluded:
            truncated = True
            if not conclusions:
                conclusions = {
                    "root_cause": "调查未在限制内收敛",
                    "claims": [],
                    "confidence": 0.0,
                    "proposed_action": None,
                }
        audit.append(
            trace_id,
            "investigation",
            "investigation.done",
            (
                f"调查结束：iterations={iterations} "
                f"tool_calls={len(tool_call_names)} truncated={truncated}"
            ),
            {
                "iterations": iterations,
                "tool_calls": len(tool_call_names),
                "evidence_count": len(evidence_ids),
                "truncated": truncated,
            },
        )
        self.session.flush()
        return InvestigationResult(
            task_id=task.id,
            evidence_ids=evidence_ids,
            tool_call_names=tool_call_names,
            conclusions=conclusions,
            truncated=truncated,
            iterations=iterations,
        )

    def _handle_tool_call(
        self,
        audit: AuditLog,
        trace_id: str,
        task: Task,
        response: dict,
        evidence_ids: list[int],
        evidence_entries: list[tuple[int, str]],
        tool_call_names: list[str],
    ) -> str:
        tool_name = response.get("tool")
        params = response.get("params")
        if not isinstance(params, dict):
            params = {}
        spec = None
        if isinstance(tool_name, str):
            try:
                spec = self.registry.get(tool_name)
            except ToolError:
                spec = None
        if spec is None or spec.has_side_effect:
            audit.append(
                trace_id,
                "investigation",
                "investigation.invalid_tool_call",
                f"拒绝非只读或未知工具调用：{tool_name!r}",
                {
                    "tool": tool_name,
                    "known": spec is not None,
                    "has_side_effect": bool(spec is not None and spec.has_side_effect),
                },
            )
            return "abort"
        try:
            result = spec.handler(params)
        except Exception as exc:  # noqa: BLE001
            result = {"ok": False, "error": str(exc)}
        if not isinstance(result, dict):
            result = {"ok": False, "error": "tool returned non-dict result"}
        ok = bool(result.get("ok"))
        summary = f"{spec.name}: ok={ok}"
        result_summary = result.get("summary")
        if isinstance(result_summary, str) and result_summary:
            summary = f"{spec.name}: ok={ok} {result_summary}"
        evidence = Evidence(
            task_id=task.id,
            tool_call_id=None,
            kind="observation",
            summary=summary,
            payload_json={"tool": spec.name, "params": params, "result": result},
            content_hash=stable_hash(result),
        )
        self.session.add(evidence)
        self.session.flush()
        evidence_ids.append(evidence.id)
        evidence_entries.append((evidence.id, summary))
        tool_call_names.append(spec.name)
        audit.append(
            trace_id,
            "investigation",
            "investigation.tool_call",
            f"工具 {spec.name} 执行完成 ok={ok}",
            {"tool": spec.name, "ok": ok, "evidence_id": evidence.id},
        )
        return "ok"

    def _system_prompt(self) -> str:
        tool_lines = [f"- {spec.name}: {spec.description}" for spec in self.registry.all()]
        return SYSTEM_PROMPT.replace("{tool_list}", "\n".join(tool_lines))

    @staticmethod
    def _user_prompt(task: Task, evidence_entries: list[tuple[int, str]]) -> str:
        lines = [f"任务请求：{task.request_text}", ""]
        if evidence_entries:
            lines.append("已收集证据：")
            for index, (evidence_id, summary) in enumerate(evidence_entries, start=1):
                lines.append(f"{index}. evidence_id={evidence_id} {summary}")
        else:
            lines.append("已收集证据：（无）")
        lines.append("")
        lines.append("请给出下一步动作（tool_call）或结论（conclude）。")
        return "\n".join(lines)
