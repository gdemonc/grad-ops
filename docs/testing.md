# 测试矩阵与验证现状

本文说明每个安全属性对应哪些测试、在什么环境跑过、以及哪些属性还没有测试。它只记录实际执行过的结果：没有在 Linux 上跑过的，写成没有在 Linux 上跑过。

最近一次运行：2026-11-07，Windows 11 + Git Bash 3.5.7，Go 1.26.1，`go test ./... -count=1` 全部通过，16 个测试包、124 个测试函数，约 17 秒。

## 如何运行

```bash
go build ./... && go vet ./... && go test ./...
```

开发机（Windows）需要显式指定 shell 与工作目录，否则需要 Bash 的测试会自跳过：

```powershell
$env:GRADOPS_TEST_BASH = 'C:\Program Files\Git\bin\bash.exe'
$env:GRADOPS_WORK_DIR = (Resolve-Path .\data).Path
go test ./... -count=1
```

注意两点：

- **`-race` 在本机不可用**（进程启动即 `exit status 0xc0000139`），因此并发相关结论来自 `MaxOpenConns(1)`、单条 SQL 的原子消费等设计约束与测试断言，而不是 race detector。
- 未设置 `GRADOPS_TEST_BASH` 时，`internal/shell`、`internal/executor`、`internal/e2e` 中依赖 Bash 的用例会自跳过，全绿并不代表这些路径跑过。

## 安全属性 → 测试

| 属性 | 测试 |
| --- | --- |
| 状态机只允许文档化的迁移 | `domain/status_test.go`：`TestCanTransitionAllowsTheDocumentedPath`、`TestCanTransitionRejectsSkippedStages`、`TestCanTransitionRejectsUnknownStatuses`、`TestTerminalStatuses` |
| 租约过期不等于工作完成；只读重试、副作用不重放 | `worker/worker_test.go`：`TestRecoverRequeuesReadOnlyLeaseAndResumes`、`TestRecoverAbandonsUncertainSideEffectJob`、`TestRecoverFailsJobWhenAttemptsAreExhausted`、`TestClaimIsExclusive` |
| 审计链可检测篡改、删除与重排 | `audit/audit_test.go`：`TestVerifyDetectsModifiedPayload`、`TestVerifyDetectsRemovedEventInTheMiddle`、`TestVerifyDetectsReorderedEvents`、`TestTriggersRefuseUpdateAndDelete` |
| 链尾截断在本机不可检测（诚实记录的限制） | `audit/audit_test.go`：`TestTailTruncationIsNotDetectedLocally` |
| 证据内容寻址、引用必须存在 | `evidence/evidence_test.go`：`TestEditedEvidenceIsDetected`、`TestVerifyCitations` |
| 结论必须引用证据 | `investigation/investigation_test.go`：`TestUngroundedConclusionIsRejected` |
| 策略确定性、不读模型自报风险 | `policy/policy_test.go`：`TestEvaluationIsDeterministic`、`TestDeniedRequests`、`TestIrreversibleChangeIsRaisedToR2` |
| 契约摘要绑定全部字段，浮点被拒 | `contract/contract_test.go`：`TestDigestIsStableAcrossMapOrder`、`TestDigestChangesWhenAnythingChanges`、`TestCanonicalJSONRejectsFloats`、`TestCanonicalVector` |
| 审批是一次性凭证，只存哈希，绑定契约 | `approval/approval_test.go`：`TestTokenIsStoredOnlyAsHash`、`TestTokenCannotBeUsedForAnotherContract`、`TestConcurrentConsumeOnlyOneWins`、`TestExpiredTokenCannotBeConsumed` |
| 执行前重算摘要与风险，篡改即拒绝 | `executor/executor_test.go`：`TestRejectsTamperedContract`、`TestRejectsRiskThatPolicyNoLongerAgreesWith`、`TestCannotExecuteTwice` |
| 不确定的副作用记为 `UNKNOWN`，不重放 | `executor/executor_test.go`：`TestTimeoutIsRecordedAsUnknown` |
| 无法切换身份时 fail closed（可选开启） | `executor/executor_test.go`：`TestRunAsMismatchIsRefusedWhenEnforced`；`config/config_test.go`：非法布尔值直接报错 |
| 独立验证不复用调查证据，失败进入回滚 | `verify/verify_test.go`：`TestVerificationPassesAndSealsTheTask`、`TestVerificationFailureFailsTheTask`；`executor/recovery_test.go`：`TestRecoveryAppliesTheCompensation`、`TestRecoveryFailureIsRecordedAsSuch` |
| Shell 边界：超时、截断、预算、进程组、环境隔离 | `shell/session_test.go`：`TestRunTimesOutAndReportsIt`、`TestRunTruncatesLargeOutput`、`TestRunEnforcesCallBudget`、`TestEnvironmentIsNotInherited` |
| 常驻会话：状态保留、超时置死、不静默重启 | `shell/console_test.go`：`TestConsoleKeepsStateBetweenCalls`、`TestConsoleTimeoutKillsTheShell`、`TestConsoleReportsExitedShell` |
| 探针共享会话状态（`GRADOPS_PROBE_CONSOLE` 开关，默认关闭） | `investigation/investigation_test.go`：`TestPersistentProbesShareState`、`TestIndependentProbesDoNotShareState` |
| HTTP 层不绕过上述规则 | `api/actions_test.go`、`api/read_test.go`：`TestProposalLifecycleOverHTTP`、`TestUnknownProposalIsNotFound`、`TestRejectionClosesTheTask`、`TestSafetyReportAdmitsTheMissingRuntime` |
| 整条闭环可用 | `e2e/loop_test.go`：`TestFullLoop` |

## 没有测试覆盖的部分

这些不是"以后再说"的清单，而是当前测试结果不能证明的东西：

- **Linux 专属行为**：uid 切换、namespace、cgroup、systemd 单元、真实 `/var/log` 路径、`PGID` 终止语义。本机只有 Git Bash，`GOOS=linux` 只证明能编译。
- **提示注入**：没有 LLM 客户端，因此也没有对抗样本测试。
- **symlink 与 TOCTOU**：路径边界只在策略层做词法规整，打开文件时没有校验符号链接。
- **故障注入**：进程崩溃、磁盘写满、目标文件并发变化、验证超时都没有模拟。
- **多进程并发**：单连接 SQLite 与单进程 Worker 是设计前提；多实例同时运行没有测试。

## 结论怎么读

通过只代表表中这些断言成立。它不代表 Linux 上的隔离成立，也不代表审计链能抵抗拥有数据库写权限的人。与实现范围的完整说明见 [README](../README.md#当前实现状态)。
