# 时序图：验证失败回滚（ROLLED_BACK）

```mermaid
sequenceDiagram
    autonumber
    actor U as 运维人员
    participant API as FastAPI /api
    participant W as Worker
    participant E as Executor
    participant V as Verification
    participant FS as 目标路径文件系统
    participant A as AuditLog

    U->>API: POST /api/approvals/{id}/approve 批准轮转日志提案
    API-->>W: 任务置 EXECUTING 并恢复入队
    W->>E: execute_approved rotate_log_file
    E->>FS: 将 path 重命名为 path.1
    E-->>A: 追加 executor.executed，落库 ExecutionRecord
    W-->>A: 任务置 VERIFYING，追加 worker.verifying
    W->>V: verify_recovery 逐项检查 postconditions
    V->>FS: 重新回读目标路径（不复用调查证据）
    FS-->>V: path 仍然存在，path_exists 检查失败
    V-->>A: 追加 verification.completed passed 为假
    V->>V: rollback_backup 还原 rotate 副本
    V->>FS: 将 path.1 替换回原 path
    V-->>A: 追加 verification.rollback 还原明细
    V-->>W: 任务置 ROLLED_BACK，summary 为验证未通过已回滚
    W-->>U: GET /api/tasks/{id} 显示验证记录 passed 为假
```

- 触发条件是**验证项失败而非执行失败**：`verify_recovery` 读取提案的 `postconditions_json`，任一检查（如 `path_exists`）不通过即 `passed=False`，任务从 `VERIFYING` 转 `ROLLED_BACK`。
- 回滚动作由 `rollback_backup` 按工具类型决定：`rotate_log_file` 把 `<path>.1` 通过 `os.replace` 还原为原文件；`backup_log_file` 属于增量备份，标注「无需回滚」；其他工具记录「无需回滚」。
- 回滚本身也会写审计（`verification.rollback`），确保「失败 → 恢复」同样可追溯；`ExecutionRecord` 仍保留，便于对照执行与验证两个阶段的结论。
- 验证阶段的所有检查都是**新的回读**：不携带调查阶段的 `evidence_id`，否则直接抛 `VerificationIndependenceError`，因此失败结论无法被调查证据「洗白」。
- 终态 `ROLLED_BACK` 属于终态集合（与 `SEALED`、`REJECTED`、`FAILED` 并列），队列不会再为其生成作业；若验证通过则任务转 `SEALED`，对照组见 `docs/sequence-approval.md`。
