# 测试矩阵与验证现状

本文说明每个安全属性对应哪些测试、在什么环境跑过、以及哪些属性还没有测试。它只记录实际执行过的结果：没有在某种环境下跑过的，写成没有跑过。

最近一次运行：2026-11-10，Ubuntu 26.04.1 LTS，Go 1.27.3（最低版本 Go 1.26.0 另行验证）；GitHub Actions `Linux CI` 在 Ubuntu 24.04 上通过 build/vet/test/race 和最低版本 job（[Linux CI workflow](https://github.com/gdemonc/grad-ops/actions/workflows/linux-ci.yml)），`go build ./...`、`go vet ./...`、`go test ./... -count=1` 与 `go test -race ./... -count=1` 全部通过。当前共有 19 个测试包、233 个 `Test` 函数；per-action cgroup v2 已在真实 systemd delegated cgroup 上验证（`TestSessionRunsInADedicatedCgroup`、`TestCgroupTimeoutKillsTheGroup`、`TestCgroupCPUQuotaIsApplied`；CPU 验证使用 `50000/100000`）；bubblewrap 沙箱已在真实 systemd 服务中完成启动探测和 worker 探针 `SEALED`，`TestBubblewrapSessionHasNoHostNetworkOrWritableRoot` 通过；`scripts/demo.sh` 也在同一环境以真实二进制跑通了只读闭环；另用本地 fake OpenAI provider 跑通了一次 `GRADOPS_PLANNER=model` 的创建任务 → 模型选探针 → 模型结论 → 封存流程；并用真实二进制验证了设置 `GRADOPS_API_TOKEN` 后 health=200、无 token=401、带 token=200；2026-11-11 用真实 DeepSeek `deepseek-flash` 跑通 `TestDeepSeekAgentLoopRealProvider`（`wc`、`grep`、`tail` 三条只读观察，结论正确引用 fixture 日志）；同一配置下 systemd 服务 task 3 以 `ls`/`grep` 两条 Evidence 到达 `SEALED`，attempt=1；task 4 在 round 1 读 step1、round 2 根据结果读 step2、round 3 finish，三份审计事件都记录在同一 attempt。

## 如何运行

```bash
# 最低兼容版本
GOTOOLCHAIN=go1.26.0 go build ./... && GOTOOLCHAIN=go1.26.0 go vet ./... && GOTOOLCHAIN=go1.26.0 go test ./... -count=1

# 开发与 CI 工具链
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./... -count=1

# 真实 systemd delegated cgroup（需要 root 和目标 unit cgroup）
GRADOPS_TEST_CGROUP_ROOT=/sys/fs/cgroup/system.slice/gradops.service \
/usr/local/go/bin/go test ./internal/shell \
  -run 'TestSessionRunsInADedicatedCgroup|TestCgroupTimeoutKillsTheGroup' -v

# 或者运行完整的部署检查
sudo scripts/verify-cgroup.sh

# 多轮只读 Agent Loop（mock LLM，不访问外部 provider）
go test ./internal/investigation -run 'TestAgentLoop|TestModelPlannerAgentLoop|TestModelPlannerDecide' -v

# 真实 DeepSeek 联调（会访问网络并消耗 token；Key 不要写进仓库）
read -rsp 'DeepSeek API key: ' GRADOPS_LLM_API_KEY; echo
export GRADOPS_LLM_API_KEY
GRADOPS_TEST_REAL_LLM=1 GRADOPS_LLM_PROVIDER=deepseek \
  go test ./internal/investigation -run TestDeepSeekAgentLoopRealProvider -v

# bubblewrap 沙箱（bwrap 不可用的环境会 skip）
go test ./internal/shell -run TestBubblewrapSessionHasNoHostNetworkOrWritableRoot -v
```

需要 shell 的测试会自动查找 `bash`，也可以用 `GRADOPS_TEST_BASH` 指定绝对路径。需要 `prlimit` 的资源限制测试在工具缺失时会 `t.Skip`，全绿不代表这些路径跑过。

注意两点：

- 测试中的 `GRADOPS_EXECUTOR_USER` 默认未设置，因此身份切换的成功路径没有覆盖；覆盖的是“同一身份可用”和“非 root 无法切换时 fail closed”。
- 资源限制测试断言 `bash` 内 `ulimit` 看到的值，因此验证的是 prlimit 是否真的把限制传给了 shell，而不是只验证参数被拼接。

## Go 工具链基线

- `go.mod` 的 `go 1.26.0` 是最低兼容版本；
- 开发与 CI 使用 Go 1.27.3；
- 两套工具链都在 2026-11-10 验证通过；
- 变更规则见 [Go 版本策略](go-version-policy.md)。

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
| DeepSeek OpenAI 兼容 provider 预设与真实联调 | `config/config_test.go`：`TestDeepSeekPresetFillsTheCompatibleEndpoint`、`TestDeepSeekRequiresAnAPIKeyAndKnownProvider`、`TestDeepSeekAcceptItsDocumentedAPIKeyAlias`；`investigation/deepseek_real_test.go`：`TestDeepSeekAgentLoopRealProvider`（需要 `GRADOPS_TEST_REAL_LLM=1` 和 Key，默认 skip） |
| 多轮只读 Agent Loop：动态改变调查方向、单轮多调用、预算与协议 | `investigation/agent_test.go`：`TestAgentLoopChangesDirectionAfterAnObservation`、`TestModelPlannerAgentLoopFollowsTheObservation`、`TestAgentLoopFinalizesAfterTheObservationBudget`、`TestAgentLoopStopsAtTheRoundBudget`、`TestAgentLoopRejectsDuplicateAndNonReadOnlyCommands`、`TestAgentLoopRetryUsesANewAttempt`、`TestAgentLoopObservesRealProcessAndLogState`、`TestAgentLoopStopsOnContextCancellation`、`TestModelPlannerFinalizationObserveIsABudgetError`；`investigation/model_test.go`：`TestModelPlannerDecideParsesObserveAndFinish`、`TestModelPlannerDecideRejectsInvalidProtocol`、`TestModelPlannerDecideFinalizationOnlyAllowsFinish`、`TestModelPlannerDecideExplainsOmittedObservations`、`TestModelPlannerDecideOnlyAllowsPromptVisibleCitations`；`evidence/evidence_test.go`：`TestEvidenceIsScopedByAttempt`；`investigation/agent_context_test.go`：`TestBoundedAgentObservationsKeepsTheNewestWithinTheBudget`、`TestBoundedAgentObservationsReturnsEverythingWhenItFits`；`config/config_test.go`：`TestAgentLoopDefaultsOffAndParsesBounds`、`TestAgentLoopRequiresModelPlannerAndValidBounds` |
| 文件目标路径安全：openat2、symlink、`..`、fd 身份与 TOCTOU | `fileops/fileops_linux_test.go`：`TestOpenRootRejectsRelativePath`、`TestOpenRejectsAbsoluteAndParentPaths`、`TestOpenRejectsSymlinksEvenInsideTheRoot`、`TestOpenRejectsSymlinkEscape`、`TestReadWriteAndIdentityStayOnTheOpenedFD` |
| bubblewrap 沙箱参数、fail-closed 与真实 namespace 边界 | `shell/sandbox_args_test.go`：`TestSandboxArgsApplyTheFilesystemAndNamespaceBoundary`、`TestSandboxArgsCanShareTheNetwork`、`TestSandboxArgsRejectAWritableRoot`、`TestValidateSandboxFailsClosed`；`shell/sandbox_test.go`：`TestBubblewrapSessionHasNoHostNetworkOrWritableRoot`（真实 bwrap 上通过）；`config/config_test.go`：`TestSandboxDefaultsToNoneAndParsesOptions`、`TestSandboxRejectsUnknownModeAndRelativeBinary` |
| cgroup v2 配置校验、fail-closed 与真实 cgroup 集成 | `shell/cgroup_test.go`：`TestValidateCgroupDisabledIsANoOp`、`TestValidateCgroupRejectsInvalidLimits`、`TestValidateCgroupFailsOutsideACgroupHierarchy`、`TestSessionRunsInADedicatedCgroup`、`TestCgroupTimeoutKillsTheGroup`（真实 systemd delegated cgroup 上通过）、`TestCgroupCPUQuotaIsApplied`（真实 cgroup 上以 `50000/100000` 通过）；`config/config_test.go`：`TestRuntimeModeDefaultsToDirect`、`TestCgroupRuntimeParsesLimits`、`TestRuntimeModeRejectsUnknownValues` |
| 执行/回滚超时记为 UNKNOWN/失败但结果仍落库 | `executor/executor_test.go`：`TestExecutionCeilingIsRecordedAsUnknown`、`TestCancelledRequestStillRecordsTheUnknownOutcome`；`executor/recovery_test.go`：`TestRecoveryCeilingIsRecordedAsFailure`；`config/config_test.go`：`TestExecutionTimeoutDefaultsAndParses` |
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
| cgroup kill/close 错误可观测、启动清理陈旧子 cgroup | `shell/cgroup_error_test.go`：`TestCgroupGuardReportsKillAndRemoveFailures`；`shell/cgroup_stale_test.go`：`TestCleanupStaleCgroupsRemovesOnlyGradopsChildren` |
| 内存超限与改变 process group 的子进程回收 | `shell/cgroup_test.go`：`TestCgroupMemoryLimitIsEnforced`、`TestCgroupTimeoutKillsAnEscapedProcessGroup`（真实 delegated cgroup 上运行） |
| Safety API 摘要与实际 runtime 状态一致 | `api/safety_test.go`：`TestSafetySummaryMatchesTheEnabledRuntime` |
| 部署文件保持非 root 与收紧边界 | `scripts/check-deploy.sh`：非 root、NoNewPrivileges、受限 capability、ProtectSystem/Home、PrivateTmp、Namespaces/SUIDSGID、MemoryMax/TasksMax、systemd 254+、`systemd-analyze verify` |

## 没有测试覆盖的部分

这些不是“以后再说”的清单，而是当前测试结果不能证明的东西：

- **uid 切换的成功路径**：只有 root 且目标账号存在时才会发生。本机以普通用户运行，覆盖的是拒绝路径；不能据此声称“切换已实测可用”。
- **cgroup 控制器范围**：真实主机验证了 `memory.max`、`pids.max`、`cpu.max` 和超时清理；`cpuset`、`io` 没有配置或测试。
- **bubblewrap 的逐项边界**：真实 systemd 服务启动探测通过，worker 探针在沙箱内 `SEALED`，root-only/loopback-only 集成测试通过；没有逐项验证每个可写根、DNS/网络副作用或所有外部命令的兼容性。
- **cgroup 之外的隔离**：没有更细的 mount/network 策略；bubblewrap 默认关闭。
- **worker 路径**：cgroup 验证使用 shell 集成测试，没有单独观察 Worker 任务产生的子 cgroup。
- **DeepSeek 真实 provider**：`TestDeepSeekAgentLoopRealProvider` 和 systemd 服务任务均已在 2026-11-11 通过（`deepseek-flash`，真实 fixture 日志）；服务任务覆盖了单轮 fixture 和两轮动态 fixture，但不证明真实 Java/Nginx 502；其他 provider/model、限流、长上下文和提示注入未覆盖，CI 中仍默认 skip。
- **Agent Loop 真实模型效果**：多轮协议和动态分支由 mock `llm.Completer`/脚本决策器验证；DeepSeek `deepseek-flash` 已跑通一次真实 fixture 日志调查，但没有复现真实 Java/Nginx 502 场景，模型延迟、提示注入和长期 token 消耗仍需继续观察。`TestAgentLoopObservesRealProcessAndLogState` 读取真实 `/proc` 和日志文件，但 fixture 是 `sleep` + 临时日志，不是 Java/Nginx 502 环境。重试 attempt 有单元测试，尚未在真实 Worker 队列里制造一次失败/重试。
- **文件系统与网络隔离的剩余缺口**：Bubblewrap 的只读根、可写根绑定和默认网络隔离已实现并验证；Landlock、seccomp、更细粒度的 Action 级文件系统授权和 IP/端口/DNS allowlist 尚未实现。
- **真实 `/var/log` 文件边界**：路径只在策略层做词法规整；没有 symlink、TOCTOU 或 `openat2` 测试。
- **AST 分析的完整性**：分析器只识别已知命令和已知间接执行形式；未知命令、运行时展开、外部脚本和复杂数据流仍可能写文件。它被明确当作辅助校验，不是沙箱。
- **提示注入**：已有模型客户端和两阶段计划器，但没有系统性的提示注入对抗测试；任务文本和观测仍被当作提示中的不可信数据，真正的边界是只读分析、Policy、Approval 和 Runtime。
- **真实 LLM provider**：客户端只用本地 `httptest` 和函数替身验证过，没有用真实 API key 跑过任何 provider。
- **总耗时预算**：调查阶段有 `GRADOPS_INVESTIGATION_TIMEOUT`（默认 5 分钟），单次执行/补偿有 `GRADOPS_EXECUTION_TIMEOUT`（默认 5 分钟）；审批等待、人工复核和整条任务没有总时限。
- **故障注入**：执行/回滚超时和请求取消已有测试；进程崩溃、磁盘写满、目标文件并发变化、验证超时没有模拟。
- **多进程并发**：单连接 SQLite 与单进程 Worker 是设计前提；多实例同时运行没有测试。
- **API 细粒度授权**：只有可选的单一 Bearer token，没有用户、角色、任务级权限或多租户隔离；未设置 token 时同机进程仍可调用 API。
- **CI 主机**：GitHub Actions 使用 Ubuntu 24.04，开发机验证使用 Ubuntu 26.04.1；两者都通过，但其他发行版的工具行为仍未验证。
- **systemd unit 的逐项边界**：unit 已在真实 systemd 主机安装并运行，cgroup 内存/PID 限制和超时清理已验证；`ProtectSystem`、`PrivateTmp`、`PrivateDevices`、`CapabilityBoundingSet` 等 unit 边界的实际阻断效果尚未逐项测试；为允许 bubblewrap 创建 procfs，`ProtectKernelTunables/Modules/Logs/Clock/Hostname` 被关闭。
- **模型行为**：`GRADOPS_PLANNER` 默认为 `stub`；模型计划器的测试只证明 schema/上限/接线成立，不证明某个真实模型会稳定遵守提示。
- **前端**：本次在 Ubuntu 上重新执行 `npm ci && npm run build`（含 `vue-tsc`）并通过；dev 代理与真实后端的联调未重新运行。

## 结论怎么读

`-race` 通过只说明当前测试覆盖的并发路径没有数据竞争；它不代表 Linux 上的 namespace/cgroup 隔离成立，也不代表审计链能抵抗拥有数据库写权限的人。与实现范围的完整说明见 [README](../README.md#当前实现状态)。
