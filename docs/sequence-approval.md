# 时序图：受控处置路径（审批 → 执行 → 独立验证 → 封存）

```mermaid
sequenceDiagram
    autonumber
    actor U as 运维人员
    participant API as FastAPI /api
    participant W as Worker
    participant P as PolicyController
    participant E as Executor
    participant V as Verification
    participant A as AuditLog

    U->>API: POST /api/tasks 提交需要处置的请求
    API-->>U: 201 task_id 与 trace_id
    W->>W: 扫描未命中，调查取证并得出 proposed_action
    W-->>A: 追加 investigation.conclude 与 grounding.claims_bound
    W->>P: evaluate 提案 R2 或 R3
    P-->>W: NEEDS_APPROVAL
    W->>W: 创建 Approval PENDING，提案置 PENDING_APPROVAL
    W-->>A: 追加 approval.requested，任务置 AWAITING_APPROVAL
    U->>API: POST /api/approvals/{id}/approve 携带 comment
    API->>API: issue_approval_token 签发一次性令牌，仅存 token_hash
    API->>W: 任务置 EXECUTING 并 enqueue_resume 恢复入队
    W->>W: _resume 找到 APPROVED 提案与审批
    W-->>A: 追加 worker.executing
    W->>E: execute_approved 提案 + 审批 + 契约
    E->>E: 契约摘要匹配后原子消费令牌为 CONSUMED
    E->>E: 身份检查 → 工具白名单 → 禁止模式 → 路径边界
    E-->>A: 追加 executor.executed，落库 ExecutionRecord
    W-->>A: 任务置 VERIFYING，追加 worker.verifying
    W->>V: verify_recovery 独立回读系统状态
    V-->>A: 追加 verification.completed passed 为真
    V-->>W: 任务置 SEALED，summary 为执行并独立验证通过
    W-->>U: GET /api/tasks/{id} 可见提案审批执行验证全链路
```

- `NEEDS_APPROVAL` 的挂起发生在策略层：`Worker._park` 创建 `PENDING` 审批记录并把任务停在 `AWAITING_APPROVAL`，此时**没有任何执行发生**。
- 批准接口签发的原始令牌不落库，`approvals.token_hash` 只保存 SHA-256；执行前由 `executor._gate_approval` 校验并把状态原子置为 `CONSUMED`，令牌一次性使用。
- 执行前六道闸门顺序固定：契约匹配 → 审批令牌状态 → 运行身份 → 副作用工具白名单 → 禁止模式 → 路径边界，任一失败即抛 `ExecutionDenied` 并写 `executor.denied`。
- 验证阶段只允许只读回读：`verify_recovery` 按 `postconditions_json` 逐项检查（`path_exists` / `path_missing` / `execution_ok`），并且拒绝传入调查阶段的 `evidence_id`（抛 `VerificationIndependenceError`）。
- 验证通过后任务才进入 `SEALED`；整个链路可在 `/api/tasks/{id}/timeline` 按审计 `seq` 逐条回放。
