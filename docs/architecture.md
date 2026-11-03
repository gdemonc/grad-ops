# Grad-Ops 架构（Go + Linux）

> 当前目标架构：Go 后端、Linux 唯一支持平台、SQLite 持久化、Vue 3 前端沿用。  
> 本文取代早期 Python 主控架构作为新实现的设计基线；旧实现仅用于行为参考和历史测试基线。

## 1. 系统定位

Grad-Ops 是一个证据约束型 Linux 运维 Agent 与控制平面。Agent 可以根据自然语言目标动态生成并组合 Linux Shell 命令和脚本，通过实时 Evidence 逐步调查。系统提供真实 Shell 表达力，但不把模型输出视为可信代码：所有提案均经过 AST 分析、确定性风险策略、必要审批、隔离执行、独立验证与审计。

## 2. 逻辑架构

```text
Vue 3 工作台
    │ HTTPS / JSON
    ▼
Go API ─────── Auth / Validation / Rate limits
    │
    ▼
Task Service + Durable Worker
    ├── Agent Loop → LLM Client → Structured Command Proposal
    ├── Command Parser / Normalizer → Linux Command Adapter
    ├── Evidence Store → Grounding
    ├── Proposal → Action Contract → Deterministic Policy (R0-R4)
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
- `investigation` 驱动动态命令调查；LLM 可提交 Shell 提案，但不能直接运行进程。
- `executor` 是唯一执行入口，执行前重新检查 AST 契约、授权、身份、隔离配置和资源边界。
- `command` 使用 Shell Parser 解析 AST、规范化脚本并分析命令、参数、重定向和控制流。
- `linux` 提供 Linux Shell Adapter 与隔离执行原语；它不决定业务审批，也不直接修改任务状态。
- `store` 负责 SQLite 实现；业务服务依赖窄接口，数据库事务边界由调用方明确控制。
- `verifier` 只读取执行后状态，不能复用调查阶段证据作为验证结论。
- `audit` 追加事件并验证哈希链。审计完整性不等同于对主机 root 或数据库管理员的防篡改保证。

## 4. Linux 执行模型

支持平台仅为 Linux。首期明确选择 Ubuntu LTS 作为开发与 CI 基线，其他发行版仅在工具行为验证后加入支持列表。

Agent 可以提交动态 Shell 脚本提案，不要求将每一种运维动作预先封装为固定业务工具。目标能力覆盖 Bash 的常用表达式，包括任意命令组合、管道、重定向、条件控制、命令替换、变量展开、子 Shell、循环、函数和多行脚本；但语法支持按阶段开放，不能把尚未分析的语法当作安全语法。服务端使用 Shell Parser（建议 `mvdan.cc/sh`）解析 AST，并递归分析所有命令、参数、管道阶段、条件分支、重定向目标、子 Shell、命令替换和动态展开。动态生成的命令名、`eval`、`bash -c`、`source`、`xargs`、`find -exec`、`awk system()` 等间接执行路径，若无法确定第二阶段实际执行内容，不能自动判为 R0；应拒绝自动执行、要求更强隔离或转人工审批。Policy 根据完整 AST、可执行文件身份、命令/参数语义、目标资源、身份和环境计算 R0-R4。生产执行优先使用系统 Bash，以保留真实 Shell 语义；`mvdan.cc/sh` 的解释器可用于 PoC，不应未经兼容性验证直接替代 Bash。执行在专用非 root 账号及受限 Linux 执行环境中进行，设置时间、CPU、内存、PID、文件大小、输出和调用预算。AST 解析本身不是沙箱。

文件副作用必须对实际使用的文件对象实施边界检查，而不是只对字符串路径做检查。对符号链接、路径替换和 TOCTOU 风险，采用 dirfd/openat 系列安全原语；如果安全原语无法满足约束，拒绝执行。

## 5. 任务状态与事务\n\n状态转换、UNKNOWN 结果、Worker 租约与崩溃恢复的详细规划见 [任务生命周期与故障恢复设计](design/task-lifecycle-and-recovery.md)。

任务状态机、审批消费、队列认领和审计写入必须有单一实现入口。状态变化与对应审计事件应尽可能在同一 SQLite 事务中提交。队列使用租约和明确的重试策略；发生崩溃时，先恢复任务状态并判断副作用是否已经发生，不可简单地重复执行不具幂等性的动作。

首期采用单机 SQLite。多实例 Worker、跨主机分布式协调和高可用均不是首期目标。

## 6. 安全边界

安全控制按层叠加：

1. API 身份认证与输入校验；
2. 动态命令解析、可执行能力约束和调用预算；
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

- 未经 AST/语义分析、确定性策略和隔离执行的 Shell/脚本；
- 多主机 Agent、mTLS 注册与远程命令协议；
- 主控高可用、分布式数据库、分布式事务；
- Kubernetes Operator；
- 自动升级；
- 任意宿主机提权、任意 sudo，以及绕过策略或沙箱的 Shell/脚本执行；
- Windows/macOS 适配。
