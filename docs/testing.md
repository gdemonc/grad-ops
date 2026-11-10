# 测试矩阵与验证现状

本文说明每个安全属性对应哪些测试、在什么环境跑过、以及哪些属性还没有测试。它只记录实际执行过的结果：没有在某种环境下跑过的，写成没有跑过。

最近一次运行：2026-11-09，Ubuntu 26.04.1 LTS，Go 1.27.3，`go build ./...`、`go vet ./...`、`go test ./... -count=1` 与 `go test -race ./... -count=1` 全部通过。当前共有 18 个测试包、179 个 `Test` 函数；`scripts/demo.sh` 也在同一环境以真实二进制跑通了只读闭环；另用本地 fake OpenAI provider 跑通了一次 `GRADOPS_PLANNER=model` 的创建任务 → 模型选探针 → 模型结论 → 封存流程；并用真实二进制验证了设置 `GRADOPS_API_TOKEN` 后 health=200、无 token=401、带 token=200。

## 如何运行

```bash
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./... -count=1
```

需要 shell 的测试会自动查找 `bash`，也可以用 `GRADOPS_TEST_BASH` 指定绝对路径。需要 `prlimit` 的资源限制测试在工具缺失时会 `t.Skip`，全绿不代表这些路径跑过。

注意两点：

- 测试中的 `GRADOPS_EXECUTOR_USER` 默认未设置，因此身份切换的成功路径没有覆盖；覆盖的是“同一身份可用”和“非 root 无法切换时 fail closed”。
- 资源限制测试断言 `bash` 内 `ulimit` 看到的值，因此验证的是 prlimit 是否真的把限制传给了 shell，而不是只验证参数被拼接。

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
| Shell 只读性分析：写、间接执行、动态命令名 | `command/analyze_test.go`：`TestAnalyzeAcceptsReadOnlyScripts`、`TestAnalyzeRejectsWrites`、`TestAnalyzeRejectsIndirectExecution`、`TestAnalyzeRejectsDynamicCommandNames`、`TestAnalyzeRejectsMalformedScripts` |
| 探针不能借“只读”名义写主机 | `investigation/investigation_test.go`：`TestProbeWithWriteIsRejected`、`TestObserveActionWithWriteIsRejected` |
| OpenAI 兼容客户端：请求形状、认证、超时、响应上限、明文 HTTP 限制 | `llm/client_test.go`：`TestCompleteSendsAnOpenAICompatibleRequest`、`TestCompleteRejectsOversizedResponses`、`TestCompleteHonoursTheContextDeadline`、`TestCompleteDoesNotFollowProviderRedirects`、`TestNewOpenAIRejectsUnsafeConfiguration` |
| 模型只输出结构化探针/结论，越界即拒绝 | `investigation/model_test.go`：`TestModelPlannerRejectsUnusablePlans`、`TestModelPlannerRejectsUnsafeConclusions`、`TestModelPlannerRejectsBadCitationsAndEmptyConclusion` |
| 两阶段模型计划器驱动真实调查，提案携带补偿动作 | `investigation/model_test.go`：`TestModelPlannerDrivesARealInvestigation`、`TestModelPlannerProposalCarriesCompensation` |
| 主程序正确装配模型客户端 | `cmd/gradops/main_test.go`：`TestBuildPlannerWiresTheModelClient` |
| 单次调查总时限覆盖探针与模型调用 | `investigation/model_test.go`：`TestInvestigationStopsAtTheTotalTimeout`；`config/config_test.go`：`TestInvestigationTimeoutDefaultsAndParses` |
| 被完全攻陷的模型仍不能直接写主机或绕过审批 | `investigation/adversarial_test.go`：`TestCompromisedModelCannotMakeAProbeWrite`、`TestCompromisedModelCannotHideAWriteBehindObserve`、`TestCompromisedModelCannotExecuteWithoutApproval`、`TestCompromisedModelCannotBypassTheResponseBound` |
| observe 契约中的写操作在执行前被拒绝 | `executor/executor_test.go`：`TestRejectsObserveContractWithWrite` |
| 验证命令不能修改系统 | `verify/verify_test.go`：`TestRejectsMutatingPostcondition` |
| 契约摘要绑定全部字段，浮点被拒 | `contract/contract_test.go`：`TestDigestIsStableAcrossMapOrder`、`TestDigestChangesWhenAnythingChanges`、`TestCanonicalJSONRejectsFloats`、`TestCanonicalVector` |
| 审批是一次性凭证，只存哈希，绑定契约 | `approval/approval_test.go`：`TestTokenIsStoredOnlyAsHash`、`TestTokenCannotBeUsedForAnotherContract`、`TestConcurrentConsumeOnlyOneWins`、`TestExpiredTokenCannotBeConsumed` |
| 执行前重算摘要与风险，篡改即拒绝 | `executor/executor_test.go`：`TestRejectsTamperedContract`、`TestRejectsRiskThatPolicyNoLongerAgreesWith`、`TestCannotExecuteTwice` |
| 不确定的副作用记为 `UNKNOWN`，不重放 | `executor/executor_test.go`：`TestTimeoutIsRecordedAsUnknown` |
| 无法切换身份时 fail closed（可选开启） | `executor/executor_test.go`：`TestRunAsMismatchIsRefusedWhenEnforced`；`config/config_test.go`：非法布尔值直接报错 |
| 配置了运行时身份后真正切换，且契约必须匹配 | `executor/executor_test.go`：`TestExecuteUsesConfiguredIdentity`、`TestConfiguredIdentityMustMatchTheContract` |
| 非 root 不能假定另一个用户；未知用户被拒绝 | `shell/runtime_test.go`：`TestRunAsCurrentUserIsAccepted`、`TestRunAsUnknownIdentityIsRejected`、`TestRunAsAnotherUserFailsClosedWithoutRoot`、`TestValidateRuntimeChecksIdentityAndToolWithoutStartingAShell` |
| Linux 资源限制真正传到 shell | `shell/runtime_test.go`：`TestResourceLimitsAreApplied`（断言 `ulimit -v/-t/-u/-f/-n`） |
| 资源限制配置错误时 fail closed | `shell/runtime_test.go`：`TestLimitsRequireAnExecutablePrlimit`、`TestLimitsRejectRelativePrlimit`、`TestLimitsRejectNegativeValues`；`config/config_test.go`：默认值、逐项关闭、负值拒绝 |
| 独立验证不复用调查证据，失败进入回滚 | `verify/verify_test.go`：`TestVerificationPassesAndSealsTheTask`、`TestVerificationFailureFailsTheTask`；`executor/recovery_test.go`：`TestRecoveryAppliesTheCompensation`、`TestRecoveryFailureIsRecordedAsSuch` |
| Shell 边界：超时、截断、预算、进程组、环境隔离 | `shell/session_test.go`：`TestRunTimesOutAndReportsIt`、`TestRunTruncatesLargeOutput`、`TestRunEnforcesCallBudget`、`TestEnvironmentIsNotInherited` |
| 常驻会话：状态保留、超时置死、不静默重启 | `shell/console_test.go`：`TestConsoleKeepsStateBetweenCalls`、`TestConsoleTimeoutKillsTheShell`、`TestConsoleReportsExitedShell` |
| 探针共享会话状态（`GRADOPS_PROBE_CONSOLE` 开关，默认关闭） | `investigation/investigation_test.go`：`TestPersistentProbesShareState`、`TestIndependentProbesDoNotShareState` |
| HTTP 层不绕过上述规则 | `api/actions_test.go`、`api/read_test.go`：`TestProposalLifecycleOverHTTP`、`TestUnknownProposalIsNotFound`、`TestRejectionClosesTheTask`、`TestSafetyReportAdmitsTheMissingRuntime` |
| API Bearer 认证：健康检查开放、其余路由拒绝缺失/错误 token | `api/auth_test.go`：`TestAPIHealthChecksDoNotRequireAToken`、`TestAPIRequiresTheConfiguredBearerToken`、`TestAPIWithoutATokenRemainsOpen` |
| 无 token 时禁止非回环监听 | `config/config_test.go`：`TestNonLoopbackRequiresAnAPIToken`、`TestAPITokenAllowsNonLoopback`、`TestShortAPITokenIsRejected`、`TestLocalhostHostnameIsAllowedWithoutAToken` |
| 整条闭环可用 | `e2e/loop_test.go`：`TestFullLoop` |

## 没有测试覆盖的部分

这些不是“以后再说”的清单，而是当前测试结果不能证明的东西：

- **uid 切换的成功路径**：只有 root 且目标账号存在时才会发生。本机以普通用户运行，覆盖的是拒绝路径；不能据此声称“切换已实测可用”。
- **namespace、cgroup、systemd**：没有实现，也没有测试。当前只有进程组、超时、输出上限、环境白名单和 prlimit 资源限制。
- **文件系统与网络隔离**：没有 Landlock、Bubblewrap、seccomp、只读挂载或网络策略。
- **真实 `/var/log` 文件边界**：路径只在策略层做词法规整；没有 symlink、TOCTOU 或 `openat2` 测试。
- **AST 分析的完整性**：分析器只识别已知命令和已知间接执行形式；未知命令、运行时展开、外部脚本和复杂数据流仍可能写文件。它被明确当作辅助校验，不是沙箱。
- **提示注入**：已有模型客户端和两阶段计划器，但没有系统性的提示注入对抗测试；任务文本和观测仍被当作提示中的不可信数据，真正的边界是只读分析、Policy、Approval 和 Runtime。
- **真实 LLM provider**：客户端只用本地 `httptest` 和函数替身验证过，没有用真实 API key 跑过任何 provider。
- **总耗时预算**：调查阶段已有 `GRADOPS_INVESTIGATION_TIMEOUT` 总时限（默认 5 分钟），覆盖探针与模型调用；执行、审批等待和人工复核没有任务级总时限。
- **故障注入**：进程崩溃、磁盘写满、目标文件并发变化、验证超时都没有模拟。
- **多进程并发**：单连接 SQLite 与单进程 Worker 是设计前提；多实例同时运行没有测试。
- **API 细粒度授权**：只有可选的单一 Bearer token，没有用户、角色、任务级权限或多租户隔离；未设置 token 时同机进程仍可调用 API。
- **Linux CI**：以上结果来自一台开发机的本地运行，不是 CI 产物。Ubuntu LTS 的版本支持仍需 CI 固化。
- **模型行为**：`GRADOPS_PLANNER` 默认为 `stub`；模型计划器的测试只证明 schema/上限/接线成立，不证明某个真实模型会稳定遵守提示。
- **前端**：本次在 Ubuntu 上重新执行 `npm ci && npm run build`（含 `vue-tsc`）并通过；dev 代理与真实后端的联调未重新运行。

## 结论怎么读

`-race` 通过只说明当前测试覆盖的并发路径没有数据竞争；它不代表 Linux 上的 namespace/cgroup 隔离成立，也不代表审计链能抵抗拥有数据库写权限的人。与实现范围的完整说明见 [README](../README.md#当前实现状态)。
