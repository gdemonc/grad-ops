# Go + Linux 核心重构实施计划

> 目标：在 `grad-ops` 中建立 Go 版核心控制平面。Linux 是唯一支持平台。旧 Python 版本保留为行为参考，不要求与新实现逐文件对应。
> 原则：先完成可测试的单机安全闭环，再考虑多主机。不要在第一阶段同时引入远程 Agent。

## Phase 0：规格冻结

- [ ] 将 `docs/specs/2026-10-26-go-linux-core-spec.md` 作为需求基线。
- [ ] 确定首期 Ubuntu LTS 版本与支持的 Go 版本。
- [ ] 冻结任务状态机、错误分类、R0-R4 风险规则、结构化动态命令契约字段。\n- [ ] 评审任务生命周期与故障恢复设计：\`docs/design/task-lifecycle-and-recovery.md\`。\n- [ ] 评审 Tool 层安全与 Adapter 抽象：\`docs/design/tool-layer-and-adapters.md\`。\n- [ ] 评审敏感数据与日志安全要求：\`docs/design/data-security-and-logging.md\`。
- [ ] 定义规范化 JSON 和 SHA-256 摘要测试向量。
- [ ] 定义 API request/response DTO 与数据库迁移策略。
- [ ] 将旧文档中 Windows/PowerShell/跨平台要求标为历史设计或删除。

**验收：** 文档不再把 Python 主控或 Windows 适配作为新版本的目标；每个首期需求都有测试验收条件。

## Phase 1：Go 项目骨架与持久化

- [ ] 创建 `go.mod` 与 `cmd/gradops/main.go`。
- [ ] 配置、日志、HTTP server、优雅关闭、健康检查。
- [ ] SQLite 迁移、Repository 和事务封装。
- [ ] 建立 task、job、evidence、proposal、approval、execution、verification、audit 的基础模型。
- [ ] 完整持久化 Task History：请求、状态迁移、工具调用元数据、Evidence 引用、审批、执行、验证和审计事件。
- [ ] 状态机函数与状态迁移单测。
- [ ] Worker 任务认领、租约、超时和恢复策略；按任务生命周期设计处理 UNKNOWN，禁止不确定副作用自动重放。
- [ ] Linux CI 执行 `go test ./...`、`go vet ./...`。

**验收：** 空项目可在 Ubuntu 启动；创建任务、持久化、重启恢复与审计写入均有集成测试；任务时间线可回看关键步骤及其来源。

## Phase 2：Linux 只读工具与调查

- [ ] 实现结构化动态命令提案：可执行文件标识、argv、工作目录、环境白名单、目标资源、运行身份、超时和输出预算。
- [ ] 实现 Linux Command Adapter，使用可信可执行文件与参数数组调用 `exec.CommandContext`，不经过 Shell 解释器。
- [ ] 不为每一种运维动作预先封装业务工具；服务端维护可执行能力、参数语义和资源范围约束。
- [ ] 实现命令解析/规范化、只读语义判定、R0-R4 确定性风险策略及 fail-closed 规则。
- [ ] 命令超时、输出上限、环境变量、调用轮次和资源预算测试。
- [ ] 实现统一 Command/Tool Adapter 接口；不实现 MCP Client。
- [ ] OpenAI 兼容 LLM client，支持 mock provider。
- [ ] 调查轮次、工具调用次数、总耗时和输出 token 预算。
- [ ] Grounding 只接受真实 evidence ID。
- [ ] 对提示注入、未知工具、畸形 JSON 和预算耗尽编写对抗测试。

**验收：** Agent 能根据调查目标动态组合多条 R0 只读命令，依据每步真实结果继续规划；调查路径不能触发任何副作用。

## Phase 3：策略、契约与审批

- [ ] 基于可执行文件、命令/参数语义、目标资源、运行身份和环境的确定性 R0-R4 风险计算。
- [ ] 动作契约规范化、摘要计算和执行前比对。
- [ ] 审批凭证只存 hash，设置短时效并绑定具体契约。
- [ ] 原子消费凭证，测试重复提交和并发双消费。
- [ ] 未知工具、未知参数、策略缺失、越界资源均 fail-closed。
- [ ] R3 受限身份与 R4 禁止动作策略测试。
- [ ] 确保 policy 包不依赖 LLM 包。

**验收：** LLM 自报风险等级不能降低实际风险；篡改参数或契约、重放审批凭证均不能触发副作用。

## Phase 4：受限执行、独立验证与恢复

- [ ] 实现唯一副作用执行入口。
- [ ] Linux 文件操作加入 symlink、TOCTOU 和路径边界防护。
- [ ] 首期仅启用经过评审的受控副作用命令子集，例如日志备份与轮转；默认不开放任意 Shell。
- [ ] 执行结果和实际参数摘要写入 execution record。
- [ ] 验证阶段使用新的只读观测，禁止复用调查证据。
- [ ] 显式定义每种副作用的恢复动作和恢复后验证。
- [ ] 验证失败与恢复失败分别进入 `ROLLED_BACK` / `RECOVERY_FAILED`。
- [ ] 故障注入：执行中进程崩溃、磁盘写入失败、目标文件变化、验证超时。

**验收：** 每个副作用都能追溯“授权了什么、实际做了什么、验证了什么、是否恢复”；不确定动作状态时不能盲目重试。

## Phase 5：审计、前端联调与部署

- [ ] 追加式审计事件与 SHA-256 链校验。
- [ ] 修改、删除、重排事件的篡改检测测试。
- [ ] Vue 3 前端对接 Go REST API。
- [ ] Linux systemd unit、专用非 root 账号、配置与数据库权限。
- [ ] README 更新为 Go/Linux 安装、运行、测试和限制说明。
- [ ] 测试报告区分 Python 历史基线与 Go 实测结果。
- [ ] 生成测试矩阵、性能测量与安全限制说明。

**验收：** 在干净的 Ubuntu 环境中可按文档完成部署；全套测试结果可复现。

## 后续阶段：Memory 与 Knowledge

Memory/Knowledge 的边界和数据设计见 `docs/design/memory-and-knowledge.md`。这部分不应阻塞首期单机闭环。

- [ ] 首期只允许简单、来源明确的记忆记录；Task History 是主要持久化事实来源。\n- [ ] Task History 稳定后，再实现结构化 Memory：来源、时间、适用范围、有效期和状态。
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

- [ ] Go 代码可构建，测试在 Linux CI 通过。
- [ ] 无 Windows 适配代码或 Windows 专属启动步骤。
- [ ] 所有副作用通过唯一 Executor 和确定性策略。
- [ ] 关键安全属性均有自动化测试，不只依赖文档承诺。
- [ ] README、架构、规格、威胁模型、测试报告与实际实现一致。
