# Tool 层安全与 Adapter 扩展设计

> 状态：规划文档 / 首期接口边界
> 目标：支持 Agent 动态组合 Linux 命令，同时保留统一的安全执行边界和未来 Adapter 扩展能力。

## 1. 设计原则

Tool 层是 Agent 与 Linux 执行能力之间的受控边界。Agent 可以根据任务动态规划、生成并组合命令，不要求每一种运维动作都预先封装成独立业务工具。但 LLM 输出始终是不可信提案，不是可直接执行的 Shell 文本。

动态命令必须先转换为结构化命令契约，再经过确定性校验、风险判定、授权、执行、验证和审计。动态生成不等于任意 Shell、任意脚本或任意 sudo。

## 2. 逻辑分层

```text
User Request
     |
     v
LLM Agent / Planner  <----------------------+
     |                                       |
     v                                       |
Structured Command Proposal                 |
     |                                       |
     v                                       |
Command Parser + Normalizer                  |
     |                                       |
     v                                       |
Policy (R0-R4) -> Approval (when required)  |
     |                                       |
     v                                       |
Executor -> Linux Process Boundary           |
     |                                       |
     v                                       |
Evidence / Verification / Audit ------------+

Future external Adapter (e.g. MCP) enters through the same contract and policy boundary.
```

Agent 根据任务生成下一步命令；执行结果和 Evidence 返回 Agent，由 Agent 决定继续调查、结束任务或提出变更方案。

## 3. 动态命令提案与执行定义

首期不要求把 `disk_usage`、`list_processes` 等每一种运维动作分别封装成业务工具。LLM 可以根据目标动态选择并组合 Linux 命令，例如 `df`、`ps`、`ss`、`systemctl`、`journalctl`、`du` 和受限的 `find` 调用；每条命令仍必须经过服务端执行边界。

LLM 输出结构化提案，而不是自由形式的 Shell 脚本。命令契约至少包含：

- 可执行文件标识，由服务端解析到可信的绝对路径；
- `argv` 参数数组，不接受拼接后的命令字符串；
- 工作目录、受控环境变量、运行身份；
- 目标资源范围、预期操作类型与前置条件；
- 超时、stdout/stderr 上限和调用预算；
- 预期后置条件、验证方式、幂等属性及恢复信息（适用时）。

服务端维护的是可执行能力与参数/资源约束，而不是固定业务任务目录。可执行文件、参数组合和目标资源必须经过 allowlist/规则校验；未知二进制、未知参数语义、越界路径或无法判断副作用的提案均 fail closed。

命令执行默认使用 Go 的 `exec.CommandContext` 与独立 `argv`，不经过 `sh -c`。首期不支持任意 Shell 语法、命令替换、动态脚本和任意管道/重定向；确有需要时另行设计受限 DSL，不直接开放 Shell。

执行前对命令解析和规范化，并生成不可变 Action Contract。契约绑定可执行文件身份/版本、完整参数、工作目录、环境变量白名单、目标资源、运行身份、超时、风险等级、策略版本和有效期。审批界面展示规范化后的真实执行内容与影响范围；审批后任一字段变化都必须重新评估和审批。

## 4. 调用与安全流程

1. Agent 根据用户目标和已有 Evidence 生成下一步结构化命令提案；调查结果返回后可继续规划下一步，不要求一次性生成完整脚本。
2. Command Parser 校验字段、解析可执行文件、规范化 `argv`、工作目录、环境和目标资源。
3. Policy 根据实际命令语义、参数、资源范围、运行身份和环境计算 R0-R4；不得采信模型自报等级。
4. R0 只读观测可在预算和策略允许时自动执行；R1-R3 按风险策略处理审批或拒绝；R4 直接拒绝。所有写操作必须经过 Action Contract，需审批时消费一次性凭证。
5. Executor 执行前重新校验规范化契约摘要、可执行文件身份、参数、资源边界、身份、策略版本和审批凭证。
6. 执行结果进入 Execution Record；独立 Verifier 使用新的只读观测检查后置条件。
7. 将结果和 Evidence 返回 Agent，决定继续调查、结束任务或提出新的变更提案。
8. 记录命令摘要、实际执行参数摘要、输出摘要、退出状态、结果状态和审计关联。

R0 也必须受可执行文件、参数、超时、输出和资源预算限制。未知命令、无法分类的副作用、解析失败、身份异常或策略不可用均 fail closed。审批只授权已展示且摘要绑定的具体契约，不代表允许任意命令。

## 5. Tool Interface 的抽象边界

接口表达统一的执行提案、能力约束、调用上下文和执行结果，不要求每个业务动作有一个预定义工具 ID。首期实现 Linux Command Adapter：负责将结构化命令契约映射为本地进程调用；Policy 与 Executor 不依赖具体命令调用方或 LLM Provider。

服务端仍需维护受信任的可执行文件清单/能力策略、参数约束和资源边界。这里的注册对象是“可执行能力及其安全约束”，不是固定的运维业务动作。首期不实现动态插件加载、远程工具发现或热更新。

## 6. MCP 与 Kubernetes（后续阶段）

后续可通过 MCP Adapter 接入外部工具；Kubernetes 能力可作为独立 Adapter 或 MCP Server 暴露。接入时必须：

- 对 Server 身份、连接目标和凭据进行服务端管理；
- 对工具列表和 Schema 做显式登记、版本固定与变更审查；
- 对集群、Namespace、资源类型和操作范围做 allowlist；
- 将外部工具调用映射为 Grad-Ops 自己的风险等级和 Action Contract；
- 不因 MCP Server 返回“成功”就认定操作成功，仍需独立验证；
- 对超时、断连和响应丢失保留 UNKNOWN 结果处理；
- 禁止向模型开放任意 kubectl、任意 API 请求或动态提权能力。

MCP 是一种接入协议，不是授权系统，也不是安全沙箱。

## 7. 首期验收

- LLM 可以根据任务动态生成多步结构化 Linux 命令提案，不依赖预先封装每一种业务工具。
- 命令只能通过服务端解析、规范化和执行边界运行；任意可执行文件、畸形参数、越界资源和未知语义均被拒绝。
- `argv` 中的 Shell 元字符按普通参数处理，不会被解释为 Shell 语法。
- R0-R4 由确定性 Policy 计算；模型自报风险不能改变结果。
- 审批内容与实际执行契约完全绑定；修改任一执行字段后必须拒绝或重新审批。
- 调查只允许 R0 只读命令；写操作不能绕过 Action Contract、Policy、必要审批和 Executor。
- 超时、输出上限、调用预算和并发限制生效。
- 执行结果可关联任务、Evidence、Execution、Verification 与 Audit；Agent 可依据结果继续规划。

## 8. 明确不做

首期不实现 MCP Client、Kubernetes 操作、动态工具市场、任意 Shell、任意脚本执行、任意 sudo、运行时自动信任新可执行文件或远程插件安装。首期不支持 Shell 管道、重定向、命令替换等语法；后续如确有需要，另行设计受限命令 DSL。
