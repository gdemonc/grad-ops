# 时序图：恶意请求与策略拒绝路径（REJECTED，零工具调用）

## 场景一：请求级禁止模式扫描（零工具调用直接拒绝）

```mermaid
sequenceDiagram
    autonumber
    actor U as 恶意或高危请求方
    participant API as FastAPI /api
    participant Q as TaskQueue
    participant W as Worker
    participant A as AuditLog

    U->>API: POST /api/tasks 内容为 rm -rf /
    API->>Q: enqueue 正常入队
    Q-->>A: 追加 task.enqueued
    W->>Q: claim_next 认领作业
    W->>W: scan_forbidden 命中 rm_rf_root
    W->>W: 任务状态置为 REJECTED
    W-->>A: 追加 policy.request_rejected 命中规则列表
    U->>API: GET /api/tasks/{id}
    API-->>U: status 为 REJECTED，summary 为请求命中禁止规则
    Note over W,A: 调查引擎未启动，工具调用数为 0
```

## 场景二：策略级 DENY（R4 / 工具越界 / 路径越界）

```mermaid
sequenceDiagram
    autonumber
    actor U as 运维人员
    participant LLM as LLMClient
    participant W as Worker
    participant P as PolicyController
    participant B as 路径边界 boundary
    participant A as AuditLog
    participant API as FastAPI /api

    U->>W: 请求进入调查（未命中请求级扫描）
    W->>LLM: complete_json
    LLM-->>W: propose 提案 risk_level 为 R4 或工具不在白名单
    W->>P: evaluate 提案与证据
    alt R4 或未知风险等级 或工具不在副作用白名单
        P-->>W: DENY 记录 reason 与 matched_rules
    else input 中的 path 越界
        P->>B: authorize_path 校验 input 中的 path
        B-->>P: BoundaryViolation 越界
        P-->>W: DENY matched_rules 为 path_boundary
    end
    W->>W: 提案置 DENIED_BY_POLICY，任务状态置为 REJECTED
    W-->>A: 追加 policy.denied 含 reason 与 matched_rules
    U->>API: GET /api/tasks/{id}
    API-->>U: status 为 REJECTED，summary 为策略拒绝
    Note over W,A: 后续不会创建审批，也不会进入执行
```

- 请求级扫描发生在调查**之前**：`Worker._investigate` 第一步执行 `scan_forbidden(task.request_text)`，命中即 `REJECTED`，`InvestigationEngine` 根本不启动，工具调用数为 0。
- 策略级拒绝发生在提议之后、执行之前：`PolicyController.evaluate` 按「风险等级 → 工具白名单 → 路径边界 → 禁止模式 → 前置证据 → 审批令牌」固定顺序裁决，任一 `DENY` 立即返回并携带 `matched_rules`。
- R4（危险 / 越权）、未知风险等级、非白名单副作用工具、路径属于受保护目录、前置条件引用不存在的证据，五类输入都会得到 `DENY`。
- 人工审批拒绝是第三条 `REJECTED` 入口：`POST /api/approvals/{id}/reject` 把审批置 `REJECTED`、提案置 `REJECTED`、任务置 `REJECTED`，同样不会触发执行。
- 所有拒绝均落审计（`policy.request_rejected` / `policy.denied` / 审批拒绝事件），可在时间线与 `/api/audit/{trace_id}/verify` 中核验。
