# Grad-Ops 架构（Go + Linux）

> 当前目标架构：Go 后端、Linux 唯一支持平台、SQLite 持久化、Vue 3 前端沿用。  
> 本文取代早期 Python 主控架构作为新实现的设计基线；旧实现仅用于行为参考和历史测试基线。

## 1. 系统定位

Grad-Ops 是一个证据约束型 Linux 运维 Agent 与控制平面。Agent 可以根据自然语言目标动态规划和组合 Linux 命令，通过实时 Evidence 逐步调查；它不是不受限制的 Shell Agent。系统通过确定性风险策略、必要的人工审批、受控执行、独立验证与审计保证动作可解释、可拒绝、可追踪。

## 2. 逻辑架构

```text
Vue 3 工作台
    │ HTTPS / JSON
    ▼
Go API ─────── Auth / Validation / Rate limits
    │
    ▼
Task Service + Durable Worker
    ├── Investigation → LLM Client → Read-only Tool Registry
    ├── Evidence Store → Grounding
    ├── Proposal → Action Contract → Deterministic Policy
    ├── Approval Service
    ├── Executor → Linux Side-effect Tools
    ├── Independent Verifier → Recovery
    └── Audit Chain
             │
             ▼
          SQLite
```

API 不直接执行系统操作。LLM 只参与有限调查和提出结构化建议，不参与授权决策。所有副作用只能从 Executor 唯一入口触发。

### 2.1 模块边界补充

LLM 是规划与理解模块，不是控制中心：它可以根据任务动态生成结构化 Linux 命令提案，并依据真实执行结果决定下一步；它不持有执行权限，也不能授权动作、决定风险等级或直接执行命令。命令解析、规范化、Policy、Approval 与 Executor 均由确定性代码负责。模型 Provider 可替换，不能改变核心安全执行链。

Tool 层统一抽象 Linux 本地工具与未来外部 Adapter。首期只实现本地 Linux 工具；MCP/Kubernetes 作为后续 Adapter，不进入首期运行依赖。详见 [Tool 层安全与 Adapter 扩展设计](design/tool-layer-and-adapters.md)。

## 3. Go 包职责与依赖规则

参见 `docs/specs/2026-10-26-go-linux-core-spec.md` 的目录结构。核心约束：

- `domain` 只定义领域对象、枚举、错误和状态迁移。
- `policy` 是确定性纯逻辑，不导入 `llm`、`api`、`executor`。
- `investigation` 只能使用只读工具接口；副作用工具不应出现在模型可见的工具列表中。
- `executor` 是唯一副作用入口，执行前重新检查契约、授权、身份、参数和资源边界。
- `linux` 实现系统工具；它不决定业务审批，也不直接修改任务状态。
- `store` 负责 SQLite 实现；业务服务依赖窄接口，数据库事务边界由调用方明确控制。
- `verifier` 只读取执行后状态，不能复用调查阶段证据作为验证结论。
- `audit` 追加事件并验证哈希链。审计完整性不等同于对主机 root 或数据库管理员的防篡改保证。

## 4. Linux 执行模型

支持平台仅为 Linux。首期明确选择 Ubuntu LTS 作为开发与 CI 基线，其他发行版仅在工具行为验证后加入支持列表。

Agent 可以动态生成命令，但不能提交可直接执行的 Shell 字符串。命令先转换为结构化契约：可信可执行文件标识、`argv` 数组、工作目录、受控环境、目标资源、运行身份、超时与输出预算。执行使用 `exec.CommandContext` 和独立参数数组，不经过 Shell 解释器。Policy 根据实际命令语义和参数计算 R0-R4。每个工具定义：

- 固定工具名与可执行文件；
- 输入参数 schema 与允许值；
- 只读/副作用标记、风险基线；
- 超时、输出字节上限和环境变量；
- 输出解析器、错误分类和测试夹具。

文件副作用必须对实际使用的文件对象实施边界检查，而不是只对字符串路径做检查。对符号链接、路径替换和 TOCTOU 风险，采用 dirfd/openat 系列安全原语；如果安全原语无法满足约束，拒绝执行。

## 5. 任务状态与事务\n\n状态转换、UNKNOWN 结果、Worker 租约与崩溃恢复的详细规划见 [任务生命周期与故障恢复设计](design/task-lifecycle-and-recovery.md)。

任务状态机、审批消费、队列认领和审计写入必须有单一实现入口。状态变化与对应审计事件应尽可能在同一 SQLite 事务中提交。队列使用租约和明确的重试策略；发生崩溃时，先恢复任务状态并判断副作用是否已经发生，不可简单地重复执行不具幂等性的动作。

首期采用单机 SQLite。多实例 Worker、跨主机分布式协调和高可用均不是首期目标。

## 6. 安全边界

安全控制按层叠加：

1. API 身份认证与输入校验；
2. 只读调查工具注册表和调用预算；
3. Grounding 证据绑定；
4. 确定性风险与策略判定；
5. 动作契约摘要校验；
6. 一次性审批凭证；
7. Linux 运行身份、路径和参数边界；
8. 独立验证与显式恢复；
9. 追加式审计与链完整性校验。

任一安全检查无法完成时采用 fail-closed：拒绝动作，并留下可诊断事件。

## 7. 前端与 API

现有 Vue 3 前端继续使用 REST/JSON。API DTO 与领域模型分离，避免将数据库结构直接暴露给前端。主要资源包括：

- `/api/tasks`：创建、查询任务；
- `/api/tasks/{id}/timeline`：审计时间线；
- `/api/approvals/{id}/approve` 与 `/reject`：审批操作；
- `/api/health`：健康状态。

具体路径与响应 schema 以实现前的 API 契约文档为准。API 不提供任意命令执行端点。

## 8. 部署\n\n敏感数据分类、脱敏、日志/Evidence/History/Audit 分工、保留与清理要求见 [敏感数据与日志安全设计](design/data-security-and-logging.md)。

- 单个 Go 服务二进制，配置从环境变量或受权限保护的配置文件读取。
- SQLite 数据库和审计相关文件位于服务专用目录，权限最小化。
- systemd 管理服务；服务默认使用专用非 root 账号。
- 日志不输出密钥、审批原始令牌或未经处理的敏感日志内容。
- 提供 Linux shell 脚本；不再提供 PowerShell 和 Windows 启动流程。

## 8.1 Memory、Knowledge 与实时 Evidence

三者职责必须分开，不能把历史记忆当作当前系统状态，也不能把静态文档当作实时观测。

- **Task History（任务历史）**：记录任务请求、调查步骤、工具调用、Evidence 引用、审批、执行、验证和审计事件。它是可追溯的事实记录，不等同于模型自由生成的摘要。
- **Memory（记忆）**：从有来源的历史记录中整理出可复用的结构化信息，例如用户明确表达的运维偏好、主机环境事实、历史任务结论和经过验证的排障经验。每条记忆应保留来源、时间、置信度和适用范围；可能变化的环境信息应设置有效期或重新验证标记。
- **Knowledge（知识库）**：人工维护的 Markdown 运维手册、Runbook、故障处理规范和项目文档。它提供通用操作知识，不声称描述主机此刻的状态。
- **Live Evidence（实时证据）**：由当前任务的只读工具实时采集，例如当前磁盘空间、进程、服务状态和日志。凡是涉及“现在”的判断，都必须以本次任务的实时 Evidence 为依据。

### 首期实现边界

首期先把 Task History 做完整：任务记录、工具调用结果、Evidence 与审批/执行/验证事件均持久化到 SQLite，并能在任务详情中回看。Memory 与 Knowledge 先定义数据边界和接口，不让它们阻塞单机闭环。

后续增加轻量 Memory 时，优先使用 SQLite 结构化表和关键词检索；Knowledge 先以仓库内或配置目录中的 Markdown 文档为来源，按标题、标签和关键词检索。暂不引入 Embedding 服务或独立向量数据库。记忆候选应来自明确的历史来源，经规则校验或用户确认后再保存，不允许模型把猜测直接写成事实。

任何 Memory/Knowledge 内容都不能直接授权操作，也不能替代当前 Evidence。检索到的内容只能作为上下文，仍需经过工具观测、Grounding、Policy、Approval 和 Executor 的既定安全流程。

## 9. 暂不实现的扩展

- 多主机 Agent、mTLS 注册与远程命令协议；
- 主控高可用、分布式数据库、分布式事务；
- Kubernetes Operator；
- 自动升级；
- 任意 Shell、任意脚本和任意 sudo；
- Windows/macOS 适配。
