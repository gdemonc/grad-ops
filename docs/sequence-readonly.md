# 时序图：只读调查路径（R0 封存）

```mermaid
sequenceDiagram
    autonumber
    actor U as 运维人员
    participant API as FastAPI /api
    participant Q as TaskQueue
    participant W as Worker
    participant LLM as LLMClient
    participant T as 只读工具注册表
    participant G as Grounding
    participant P as PolicyController
    participant A as AuditLog

    U->>API: POST /api/tasks 提交自然语言请求
    API->>Q: enqueue 创建 Task 与 TaskJob
    Q-->>A: 追加 task.enqueued
    W->>Q: claim_next 原子认领租约
    W->>W: scan_forbidden 未命中
    W-->>A: 任务置 INVESTIGATING，追加 worker.investigating
    loop 有界迭代 最多 4 轮 / 12 次工具调用
        W->>LLM: complete_json 请求下一步动作
        LLM-->>W: tool_call 或 conclude
        W->>T: 仅放行 registry 中 has_side_effect 为假的工具
        T-->>W: 只读观测结果
        W->>A: 追加 investigation.tool_call
        W-->>A: 落库 Evidence 并记录 content_hash
    end
    W->>G: bind_claims_to_evidence 过滤无据结论
    G-->>A: 追加 grounding.claims_bound
    W->>P: evaluate R0 只读处置提案
    P-->>W: ALLOW_AUTO 只读工具自动放行
    W->>W: 任务状态置为 SEALED
    W-->>A: 追加 worker.sealed
    U->>API: GET /api/tasks/{id} 与 timeline
    API-->>U: status 为 SEALED，summary 为根因
```

- 入队即写审计：`TaskQueue.enqueue` 同时创建 `Task`、`TaskJob` 并追加 `task.enqueued` 事件，之后每个状态迁移都伴随审计事件。
- 调查是**有界迭代**：轮次上限 `GRADOPS_INVESTIGATION_MAX_ITERATIONS`（默认 4）、工具调用上限 `GRADOPS_INVESTIGATION_MAX_TOOL_CALLS`（默认 12），另有 120 秒超时；触顶即强制收敛并标记 `truncated`。
- 工具侧只读防线独立于提示词：即使模型请求副作用工具，`InvestigationEngine._handle_tool_call` 也会拒绝并中止调查（`investigation.invalid_tool_call`），不产生任何副作用。
- `Grounding` 把每条结论与真实 `evidence_id` 比对，引用不存在证据或零引用的结论被丢弃并写入 `grounding.claims_bound`。
- 只读路径不产生副作用提议（`proposed_action` 为空或策略判 `ALLOW_AUTO` 且非副作用白名单工具）时，任务直接 `INVESTIGATING → SEALED`，全程零人工介入、零副作用调用。
