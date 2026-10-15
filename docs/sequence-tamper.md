# 时序图：审计篡改检测（哈希链校验）

```mermaid
sequenceDiagram
    autonumber
    participant P as 业务各阶段
    participant A as AuditLog.append
    participant DB as SQLite audit_events
    participant ATK as 攻击者 / 事后改库
    participant API as 审计校验接口
    participant V as AuditLog.verify_chain

    P->>A: append 事件 payload
    A->>A: payload_hash = H(seq, stage, event_type, message, payload)
    A->>A: event_hash = H(prev_hash, payload_hash)
    A->>DB: 插入第 n 条 记录 prev_hash 指向第 n-1 条
    Note over P,DB: 多个阶段持续追加，seq 从 1 连续递增
    ATK->>DB: UPDATE audit_events SET message = 伪造内容 WHERE seq = k
    API->>V: verify_chain(trace_id)
    V->>DB: 按 seq 升序读取全部事件
    V->>V: 逐条重算 payload_hash 与 event_hash 并比对 prev_hash
    V-->>API: valid 为假，entries 中第 k 条 payload_ok 为假
    API-->>ATK: {"valid": false, "entry_count": n, "head_hash": "..."}
    Note over V,API: 返回 entries[k-1] 精确指出被篡改的 seq
```

- 追加（`AuditLog.append`）先取同 `trace_id` 的最后一条事件，以其 `event_hash` 作为本条 `prev_hash`，`seq` 严格递增；首条事件的 `prev_hash` 为 64 个 0。
- 稳定哈希使用 `json.dumps(sort_keys=True, separators=(",", ":"))` + SHA-256，同样的内容永远得到同样的摘要，避免键序与空白导致的误报。
- 篡改模型覆盖三种：直接改 `payload_json`（`payload_ok` 变假）、改字段后重算 `payload_hash` 但不动 `event_hash`（`event_ok` 变假）、删除中间事件（`prev_ok` 链断裂）。
- `verify_chain` 从创世哈希开始整链重算，`valid` 只有在**每一条**的 `prev_ok`、`payload_ok`、`event_ok` 全部为真时才为真，并逐条给出 `stored_event_hash` 与 `expected_event_hash` 便于论文取证。
- 对外暴露为 `GET /api/audit/{trace_id}/verify`（可加 `?full=true` 查看逐条结果），前端「审计校验」页直接调用；`GET /api/tasks/{id}/timeline` 按 `seq` 展示，篡改后消息与哈希结论同时可见。
