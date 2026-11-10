# Go + Linux 核心实施计划

> 目标：在 `grad-ops` 中建立 Go 核心控制平面。Linux 是唯一支持平台。本项目按文档规格从零实现 Go 版本；旧 Python 代码仅作历史参考，不作为迁移基线、兼容要求或架构模板。
> 原则：先完成可测试的单机安全闭环，再考虑多主机。不要在第一阶段同时引入远程 Agent。

## Phase 0：规格冻结

- [x] 将 `docs/specs/2026-10-26-go-linux-core-spec.md` 作为需求基线。
- [x] 确定首期 Ubuntu LTS 版本与支持的 Go 版本（Ubuntu 26.04.1 LTS 上以 Go 1.27.3 验证；`go.mod` 仍要求 Go 1.26.0 作为语言下限，CI 固化待补）。
- [x] 冻结任务状态机、错误分类、R0-R4 风险规则、Shell Session、Policy/Approval、Action Contract 与 Linux Runtime 边界。
- [x] 评审任务生命周期与故障恢复设计：`docs/design/task-lifecycle-and-recovery.md`。
- [x] 评审 Tool 层安全与 Adapter 抽象：`docs/design/tool-layer-and-adapters.md`。
- [x] 评审敏感数据与日志安全要求：`docs/design/data-security-and-logging.md`。
- [x] 定义规范化 JSON 和 SHA-256 摘要测试向量（`internal/contract` 的固定向量与跨进程重算测试）。
- [x] 定义 API request/response DTO 与数据库迁移策略（嵌入式版本化迁移 + 各包内 DTO）。
- [x] 清理与首期 Linux-only 目标冲突的 Windows/PowerShell/跨平台要求（旧核心闭环规格已标注归档；`internal/shell/control_other.go` 仅为开发机验证存根，不属于支持平台）。

**验收：** 文档明确 Go Greenfield 实现与 Linux-only 边界；Python 不构成功能兼容或架构迁移要求；每个首期需求都有测试验收条件。

## Phase 1：Go 项目骨架与持久化

- [x] 创建 `go.mod` 与 `cmd/gradops/main.go`。
- [x] 配置、日志、HTTP server、优雅关闭、健康检查。
- [x] API Bearer 认证：设置 `GRADOPS_API_TOKEN` 时所有 `/api` 路由要求 token；未设置时只允许回环监听。还没有用户/角色授权。
- [x] SQLite 迁移与事务封装（查询位于各服务包内，未引入独立 Repository 层）。
- [x] 建立 task、job、evidence、proposal、approval、execution、verification、audit 的数据库表。
- [x] 建立上述实体的 Go 领域模型（分布于各服务包：`contract.Contract`、`evidence.Record`、`approval.Grant`、`executor.Outcome`、`verify.Result`，未集中在单一 domain 包）。
- [x] 完整持久化 Task History：请求、状态迁移、工具调用元数据、Evidence 引用、审批、执行、验证和审计事件。
- [x] 状态机函数与状态迁移单测。
- [x] Worker 任务认领、租约、超时和恢复策略；按任务生命周期设计处理 UNKNOWN，禁止不确定副作用自动重放。
- [ ] Linux CI 执行 `go test ./...`、`go vet ./...`（Ubuntu 26.04.1 本机已通过，含 `-race`；尚未接入 CI）。

**验收：** 空项目可在 Ubuntu 启动；创建任务、持久化、重启恢复与审计写入均有集成测试；任务时间线可回看关键步骤及其来源。

## Phase 2：Linux 只读工具与调查

- [x] 实现通用持久 Bash Session：固定 Bash、会话生命周期、工作目录、受控环境、超时、输出截断和调用预算。（`shell.Console` 常驻一个 Bash 进程，包装脚本用哨兵行分帧、输出经临时文件捕获，超时按进程组终止并把会话标记为死亡；`shell.Session` 仍是每次调用一个新进程。执行契约默认走 Session：审批过的动作不应依赖前一个无关脚本留下的环境变量或目录。）
- [x] 实现 Policy/Approval：按主体身份、目标资源、副作用、风险和部署策略判定授权；不穷举预定义业务命令。
- [x] 引入 Shell AST Parser（`mvdan.cc/sh/v3`）作为辅助分析器：识别写操作、间接执行（`eval`/`bash -c`/`source`/`xargs`/`find -exec`/`awk system()` 等）和动态命令名；探针、`observe` 动作和验证命令在运行前 fail-closed 校验。未把它当作唯一安全边界，也未实现完整 AST 展示。
- [x] 实现 Action Contract，绑定原始脚本摘要、目标资源、主体/运行身份、隔离配置和预算；AST 摘要仅作为辅助审计字段（未包含 Shell/Runtime 版本字段）。
- [ ] 实现 Linux Runtime：专用非 root 身份、systemd transient unit/scope 或等效 cgroup、CPU/内存/PID/时间/输出限制、进程组回收。
  - 已实现：可选的 uid/gid 切换（`GRADOPS_EXECUTOR_USER`，非 root 无法切换时启动失败）、prlimit 资源限制（CPU/地址空间/进程数/文件大小/文件描述符）、进程组回收。
  - 未实现：namespace、cgroup、systemd 单元、文件系统与网络隔离。
- [x] 调查和操作分离：调查阶段开放必要只读视图；写文件、服务管理、包管理和权限变更进入 Host Operation 授权流程。
- [ ] 高权限操作经独立、窄接口、可审计的 privileged broker/helper；不把 Agent 加入 sudo 组，不向 Bash Session 暴露任意 sudo。
- [ ] 评估文件系统与网络隔离的首期可行配置；Landlock、Bubblewrap、AppArmor/seccomp 按需增强，不要求首期全部引入。
- [x] 测试超时、输出上限、环境清理、CPU/内存/PID/文件大小预算、进程组终止和调用轮次。（超时、输出上限、环境清理、调用预算、进程组终止已有测试；CPU/地址空间/进程数/文件大小/fd 由 prlimit 测试覆盖；cgroup 级预算仍未实现）
- [ ] 实现统一 Command/Tool Adapter 接口；不实现 MCP Client。
- [x] OpenAI 兼容 LLM client，支持测试替身（`llm.CompleterFunc`）；提供两阶段模型计划器，默认部署仍为 `StubPlanner`。真实 provider 尚未用密钥验证。
- [ ] 调查轮次、工具调用次数、总耗时和输出 token 预算。（部分：已限制探针数、单次 provider 超时、补全 token、shell 调用次数，并有 `GRADOPS_INVESTIGATION_TIMEOUT` 调查总时限；执行、审批等待和人工复核没有任务级总时限）
- [x] Grounding 只接受真实 evidence ID。
- [ ] 对提示注入、未知工具、畸形 JSON 和预算耗尽编写对抗测试。（部分：畸形 JSON、未知动作类型、预算耗尽已有测试；提示注入未覆盖）

**验收：** Agent 能通过通用 Bash Session 完成常见只读排查；Policy 能约束主体、目标资源和副作用；Runtime 的非 root、资源与进程边界有集成测试；AST 不被当作唯一安全保证。

## Phase 3：策略、契约与审批

- [x] 基于可执行文件、命令/参数语义、目标资源、运行身份和环境的确定性 R0-R4 风险计算（当前基于动作类型、路径、身份与正则危险模式，不是 AST）。
- [x] 动作契约规范化、摘要计算和执行前比对。
- [x] 审批凭证只存 hash，设置短时效并绑定具体契约。
- [x] 原子消费凭证，测试重复提交和并发双消费。
- [x] 未知主体、未授权资源、策略缺失、越界操作和授权状态不确定均 fail-closed。
- [x] R3 受限身份与 R4 禁止动作策略测试。
- [x] 确保 policy 包不依赖 LLM 包。

**验收：** LLM 自报风险等级不能降低实际风险；篡改参数或契约、重放审批凭证均不能触发副作用。

## Phase 4：受限执行、独立验证与恢复

- [x] 实现唯一副作用执行入口。
- [ ] Linux 文件操作加入 symlink、TOCTOU 和路径边界防护（当前仅在策略层做词法规整，符号链接在打开时未做校验）。
- [ ] 首期仅启用经过评审的受控副作用场景（例如日志备份与轮转）；所有 Shell 副作用须经过 Policy、契约、必要审批和 Runtime 边界控制，不开放任意提权。
- [x] 执行结果和实际参数摘要写入 execution record。
- [x] 验证阶段使用新的只读观测，禁止复用调查证据。
- [x] 显式定义每种副作用的恢复动作和恢复后验证（补偿动作随契约冻结，审批人一并批准）。
- [x] 验证失败与恢复失败分别进入 `ROLLED_BACK` / `RECOVERY_FAILED`。
- [ ] 故障注入：执行中进程崩溃、磁盘写入失败、目标文件变化、验证超时。（部分：超时与命令失败已覆盖；进程崩溃与磁盘故障未覆盖）

**验收：** 每个副作用都能追溯“授权了什么、实际做了什么、验证了什么、是否恢复”；不确定动作状态时不能盲目重试。

## Phase 5：审计、前端联调与部署

- [x] 追加式审计事件与 SHA-256 链校验。
- [x] 修改、删除、重排事件的篡改检测测试。
- [x] Vue 3 前端对接 Go REST API（`npm run build` 通过，dev 代理联调验证）。
- [ ] Linux systemd unit、专用非 root 账号、配置与数据库权限。
- [x] README 更新为 Go/Linux 安装、运行、测试和限制说明。
- [x] 测试报告只将 Python 测试作为历史背景；Go 测试结果必须来自 Go 实际实现与实测。
- [x] 生成测试矩阵、性能测量与安全限制说明：`docs/testing.md`（含未覆盖清单与运行环境说明）。

**验收：** 在干净的 Ubuntu 环境中可按文档完成部署；全套测试结果可复现。

## 后续阶段：Memory 与 Knowledge

Memory/Knowledge 的边界和数据设计见 `docs/design/memory-and-knowledge.md`。这部分不应阻塞首期单机闭环。

- [ ] 首期只允许简单、来源明确的记忆记录；Task History 是主要持久化事实来源。
- [ ] Task History 稳定后，再实现结构化 Memory：来源、时间、适用范围、有效期和状态。
- [ ] 为用户偏好、环境事实和已验证经验提供关键词检索。
- [ ] 增加过期识别、冲突标记、用户纠正/撤销能力。
- [ ] 建立 Markdown Runbook 知识库，支持标题、标签和关键词检索，并返回原文位置。
- [ ] 评估检索质量后，再决定是否需要 Embedding/向量检索；不预先引入向量数据库。
- [ ] 记忆候选须有可追溯来源；模型猜测不能自动成为长期记忆。

**验收：** Memory 与 Knowledge 的检索结果能显示来源和时间；过期环境信息不会被当作当前事实；二者均不能绕过实时 Evidence、Policy 和 Approval。

## 暂缓：MCP 与 Kubernetes

首期不实现 MCP Client、Kubernetes 工具或集群操作。单机闭环稳定后，才评估通过 Tool Adapter 接入外部工具。未来 Kubernetes 能力应从只读操作开始，并复用统一命令/动作契约、参数校验、策略、审批、执行、验证和审计链；不向模型开放任意 kubectl 或 Shell 命令。

## 暂缓：多主机

只有单机闭环、安全测试与恢复流程稳定后，才评估多主机。未来方案可参考旧 `docs/plans/2026-10-26-multi-host-go-agent.md` 中的 mTLS、Ed25519、TTL/nonce、Agent 本地策略和逐主机审计，但需另行冻结协议并编写跨语言测试向量。

## 完成定义

- [ ] Go 代码可构建，测试在 Linux CI 通过（Ubuntu 26.04.1 本机构建、测试与 `-race` 通过；Linux CI 尚无结果）。
- [ ] 无 Windows 适配代码或 Windows 专属启动步骤（`internal/shell/control_other.go` 是开发机存根，需在 Linux CI 落地后删除）。
- [x] 所有副作用通过唯一 Executor 和确定性策略。
- [x] 关键安全属性均有自动化测试，不只依赖文档承诺。
- [x] README、架构、规格、威胁模型、测试报告与实际实现一致（README 与实施计划已对齐；`docs/architecture.md` 仍描述目标布局而非当前包清单，差距记录在此）。
