# Grad-Ops 架构（Go + Linux）

> 当前目标架构：Go 后端、Linux 唯一支持平台、SQLite 持久化、Vue 3 前端沿用。  
> 本文取代早期 Python 主控架构作为新实现的设计基线；旧实现仅用于行为参考和历史测试基线。

## 1. 系统定位

Grad-Ops 是一个证据约束型 Linux 运维 Agent 与控制平面。Agent 根据自然语言目标动态生成并组合 Linux Shell 命令和脚本，通过实时 Evidence 逐步调查。Shell 是通用执行能力；模型输出始终是不可信提议。安全边界由确定性 Policy、Approval 和 Linux Runtime 共同构成，AST 仅辅助理解命令结构、提示风险和生成审计信息。

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

当前 `investigation.ModelPlanner` 采用两阶段：第一阶段只选探针；探针经只读分析和受限 Shell 执行后，第二阶段才让模型看到截断后的观测并给出结论/动作。动作仍要经过策略、契约、审批和执行器；模型返回的 JSON 有字段、数量、字节上限和引用范围校验。

Tool 层统一抽象 Linux 本地工具与未来外部 Adapter。首期只实现本地 Linux 工具；MCP/Kubernetes 作为后续 Adapter，不进入首期运行依赖。详见 [Tool 层安全与 Adapter 扩展设计](design/tool-layer-and-adapters.md)。

## 3. Go 包职责与依赖规则

参见 `docs/specs/2026-10-26-go-linux-core-spec.md` 的目录结构。核心约束：

- `domain` 只定义领域对象、枚举、错误和状态迁移。
- `policy` 是确定性纯逻辑，不导入 `llm`、`api`、`executor`。
- `investigation` 驱动只读调查；LLM 通过通用 Shell Tool 提交命令，不持有进程执行权限。
- `shell` 管理持久 Bash Session、工作目录、受控环境和调用预算；不承担授权决策。
- `policy` 根据身份、目标资源、副作用、风险和部署策略决定允许范围及审批要求，不枚举每一种运维业务命令。
- `executor` 是唯一主机操作入口，执行前校验授权、Action Contract、运行身份和 Runtime 配置。
- `command` 可使用 Shell Parser/AST 辅助识别命令结构和间接执行风险，但解析结果不是安全证明。
- `linux` 提供 Bash Session、systemd/cgroup 等 Linux Runtime 原语；它不决定业务审批，也不直接修改任务状态。
- `store` 负责 SQLite 实现；业务服务依赖窄接口，数据库事务边界由调用方明确控制。
- `verifier` 只读取执行后状态，不能复用调查阶段证据作为验证结论。
- `audit` 追加事件并验证哈希链。审计完整性不等同于对主机 root 或数据库管理员的防篡改保证。

### 3.1 当前实现对照

规格里的目标包与当前代码的对应关系如下。"未实现"的包不会以空壳存在，也不会被其他包假装调用。

| 包 | 现状 |
| --- | --- |
| `cmd/gradops` | 组装配置、日志、迁移、租约恢复、各服务、Worker 循环与 HTTP server，负责优雅关闭 |
| `internal/config` | 环境变量读取与启动期校验，非法配置直接失败 |
| `internal/domain` | 任务状态机、R0–R4 风险、时间编解码、错误分类 |
| `internal/store` | SQLite 连接、嵌入式版本化迁移、`Tx` 事务封装 |
| `internal/audit` | 追加式 SHA-256 链与链校验 |
| `internal/worker` | 认领、租约、重试、启动恢复与执行循环 |
| `internal/shell` | 受控 Bash：超时、输出截断、调用预算、进程组回收、环境白名单、prlimit 资源限制、可选 uid/gid 切换。`Session` 每次调用一个新进程，`Console` 常驻一个进程并在调用之间保留状态；执行器用前者 |
| `internal/policy` | 确定性 R0–R4 判定与审批要求，不导入 LLM |
| `internal/command` | 基于 `mvdan.cc/sh/v3` 的辅助分析：识别写操作、间接执行与动态命令名；不承担授权或沙箱职责 |
| `internal/contract` | 规范化 JSON、脚本摘要与契约摘要 |
| `internal/approval` | 一次性审批凭证：只存哈希、绑定契约、原子消费 |
| `internal/executor` | 唯一副作用入口；执行前重算摘要并复核策略；契约内补偿回滚 |
| `internal/verify` | 执行后的独立只读观测 |
| `internal/investigation` | 探针 → 证据 → grounding；支持单阶段计划器和“探针 → 观测 → 结论/动作”的两阶段模型计划器 |
| `internal/llm` | OpenAI 兼容聊天客户端：超时、响应大小上限、回环 HTTP 限制、Bearer 认证；不记录密钥 |
| `internal/evidence` | 内容寻址的证据存储与引用校验 |
| `internal/task` | Task History 的创建、查询与时间线 |
| `internal/api` | REST 接口与错误映射 |
| `internal/e2e` | 贯穿真实组件的闭环测试 |

尚未实现：规格中的 `linux`（systemd/cgroup 隔离原语）包；当前 cgroup v2 的 per-action 边界仍在 `internal/shell` 中，namespace/文件系统/网络隔离未实现。`internal/llm` 已提供 OpenAI 兼容客户端，`investigation.ModelPlanner` 提供两阶段模型计划；默认部署仍是 `StubPlanner`，`GRADOPS_PLANNER=model` 才启用模型。执行边界目前包括进程组、超时、输出上限、环境白名单、prlimit 资源限制，以及在配置 `GRADOPS_EXECUTOR_USER` 后的 uid/gid 切换；namespace、cgroup、文件系统与网络隔离仍未实现。`command` 已作为辅助分析包存在，但只校验探针/observe/验证命令的只读性，不是授权引擎。详见 [README 的当前实现状态](../README.md#当前实现状态)。

## 4. Linux Shell 与执行模型

### 4.1 Shell 是通用工具，不是业务工具白名单

Agent 使用通用、可持续的 Bash Session 执行动态命令，支持命令组合、管道、重定向、条件连接、命令替换、变量展开、子 Shell 和多行脚本。系统不要求为“查看磁盘”“检查服务”等每种运维意图预先创建独立业务工具。

Shell Tool 负责表达与会话管理：固定 Bash 可执行文件和启动参数，管理工作目录、受控环境、会话生命周期、超时、输出截断和调用预算。它不负责授予主机权限，也不应直接以服务进程身份执行任意命令。

### 4.2 Policy 与 Approval

Policy 不试图证明任意 Shell 文本在语义上绝对安全，也不把所有 Shell 命令预先枚举成业务工具。它围绕操作的安全属性做确定性决策：主体身份、目标资源、操作类型、副作用可逆性、影响范围、任务能力、运行身份和部署策略。R0-R4 表达风险，但不能仅凭命令字符串或模型自报等级判定。

低风险只读观察可在受限身份和预算内自动执行；文件写入、服务管理、包管理、用户/权限变更等进入受控操作流程。R3 默认拒绝或要求更强身份与二次确认，R4 禁止普通审批放行。

高权限操作不得通过把整个 Agent 加入 sudo 组来实现。需要特权时，由独立的 privileged broker/helper 暴露窄范围、参数化、可审计的操作能力；Shell Session 本身不获得任意 sudo/root。

### 4.3 AST 的定位

当前 `internal/command` 使用 `mvdan.cc/sh/v3` 对 Shell 脚本做 AST 分析，识别写操作、`eval`、`bash -c`、`source`、`xargs`、`find -exec`、`awk system()` 等间接执行入口和动态命令名。探针、`observe` 动作和验证命令在运行前使用该分析做 fail-closed 校验；更完整的命令结构展示与审批信息尚未实现。

AST 不是授权引擎，也不是安全沙箱。Shell 的动态展开、外部脚本、环境变量、命令替换和运行时状态使得静态分析无法完整证明任意脚本安全。当前分析器只识别已知命令和已知间接执行形式，未知命令仍可能写文件；因此它只用于只读性校验，真正的边界仍是 Policy、Approval 和 Runtime。

### 4.4 Linux Runtime 是最终执行边界

首期以 Linux 原生机制构建轻量 Runtime，而不是先引入完整容器编排平台。当前已实现可选的 cgroup v2 子 cgroup（内存/进程/CPU 限制）、systemd unit 的 `Delegate=yes`，以及可选的 bubblewrap 沙箱（独立 mount/PID/IPC/UTS/user/network namespace、只读根和可写根绑定）。真实 systemd delegated cgroup 上验证了内存/PID/CPU 限制和超时清理；bubblewrap 沙箱已在真实 systemd 服务中完成启动探测和 worker 探针验证，默认关闭但部署示例启用。

1. API/Agent 主服务使用专用非 root 账号；Shell 执行账号与主服务身份分离。
2. 每个任务或操作会话使用独立的 systemd transient service/scope 或等效 cgroup，限制 CPU、内存、Tasks/PID、执行时间、文件大小和输出量。
3. 清理继承环境，固定 PATH、HOME、umask、工作目录和 Bash 路径；凭据不进入 Shell 环境。
4. 通过 systemd 文件系统保护、只读挂载、专用工作目录等限制文件访问；默认限制网络，仅按任务需要开放。
5. 超时或预算耗尽时终止整个进程组/cgroup，回收后台进程并记录退出状态。
6. Landlock、Bubblewrap、seccomp、AppArmor 可作为后续增强，按实际需要和兼容性测试引入，不要求首期全部叠加。

调查阶段可以访问为只读取证明确开放的主机信息；变更操作必须通过 Policy/Approval 和受控执行入口。若某项操作需要的隔离能力在目标系统不可用，应拒绝该操作或降级为只读，不得静默退回到不受限 Shell。

### 4.5 Action Contract 与审计

对需要审批或有副作用的操作，Action Contract 绑定原始提案摘要、目标资源、运行身份、环境、工作目录、Runtime 配置、资源预算、风险等级、策略版本和审批状态。若使用 AST，AST 摘要可作为辅助审计字段，但不能声称它完整代表脚本运行语义。

执行前重新检查契约、授权、目标资源和 Runtime 配置；任何关键字段变化均重新评估。执行记录保存原始命令、实际身份、环境摘要、运行边界、审批依据、退出码、信号、资源消耗和输出截断状态。执行阶段有部署级紧急上限（`GRADOPS_EXECUTION_TIMEOUT`），但结果写入使用与请求取消分离的短时 context：客户端断开不能阻止“副作用可能已经发生”的记录。执行后使用新的只读观测进行独立验证，不能只凭退出码认定成功。

同一 Bash Session 的命令组合共享相应的任务/会话预算和 Runtime 边界；不要为管道的每个阶段启动独立沙箱，否则会破坏 Shell 语义并显著增加复杂度。

## 5. 任务状态与事务

状态转换、UNKNOWN 结果、Worker 租约与崩溃恢复的详细规划见 [任务生命周期与故障恢复设计](design/task-lifecycle-and-recovery.md)。

任务状态机、审批消费、队列认领和审计写入必须有单一实现入口。状态变化与对应审计事件应尽可能在同一 SQLite 事务中提交。队列使用租约和明确的重试策略；发生崩溃时，先恢复任务状态并判断副作用是否已经发生，不可简单地重复执行不具幂等性的动作。

首期采用单机 SQLite。多实例 Worker、跨主机分布式协调和高可用均不是首期目标。

## 6. 安全边界

安全控制按层叠加：

1. API 身份认证与输入校验（Bearer token；未设置 token 时只允许回环监听）；
2. Shell Session 的受控环境、会话权限和调用预算；
3. Grounding 证据绑定；
4. 确定性 Policy 风险与权限判定；
5. Action Contract 与授权状态校验；
6. 必要时的一次性审批凭证；
7. Linux Runtime 的运行身份、文件/网络边界和资源限制；
8. 特权操作经独立 privileged broker；
9. 独立验证、显式恢复与追加式审计。

AST 分析可辅助风险提示、审批展示和审计，但不替代 Policy、Approval 或 Runtime。

任一安全检查无法完成时采用 fail-closed：拒绝动作，并留下可诊断事件。

## 7. 前端与 API

现有 Vue 3 前端继续使用 REST/JSON。API DTO 与领域模型分离，避免将数据库结构直接暴露给前端。设置 `GRADOPS_API_TOKEN` 时，除健康检查外的所有 `/api` 路由要求 Bearer token；前端可以从浏览器 `localStorage` 的 `gradops_api_token` 读取同一 token。主要资源包括：

- `/api/tasks`：创建、查询任务；
- `/api/tasks/{id}/timeline`：审计时间线；
- `/api/approvals/{id}/approve` 与 `/reject`：审批操作；
- `/api/health`：健康状态。

具体路径与响应 schema 以实现前的 API 契约文档为准。API 不提供任意命令执行端点。

## 8. 部署

敏感数据分类、脱敏、日志/Evidence/History/Audit 分工、保留与清理要求见 [敏感数据与日志安全设计](design/data-security-and-logging.md)。

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

- 未经 Policy 授权和 Runtime 边界约束的主机操作；
- 多主机 Agent、mTLS 注册与远程命令协议；
- 主控高可用、分布式数据库、分布式事务；
- Kubernetes Operator；
- 自动升级；
- 任意宿主机提权、任意 sudo，以及绕过策略或沙箱的 Shell/脚本执行；
- Windows/macOS 适配。
