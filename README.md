# grad-ops

证据约束型自主运维控制平面 · Go + Linux

> **项目实现方式：** Greenfield Go implementation（Go 全新实现）。  
> **目标架构：** Go 后端 + Linux-only 工具层 + SQLite + 沿用 Vue 3 前端。  
> **实现状态：** 单机闭环的机制已实现并有测试覆盖（队列 → 调查 → 证据 → 策略 → 契约 → 审批 → 执行 → 独立验证 → 补偿回滚 → 审计），控制台已对接 Go 接口；LLM 接入与 Linux Runtime 隔离（uid/namespace/cgroup）尚未实现。详见[当前实现状态](#当前实现状态)。

本仓库采用**以文档为规格的全新 Go 实现（Greenfield Implementation）**：`docs/` 中经确认的核心规格、架构、安全设计和实施计划是需求与设计事实来源。仓库中任何现存 Python 代码均为 legacy prototype / historical reference，仅供理解历史背景；它不是 Go 实现的源代码基线，不构成功能兼容、架构复刻或逐文件迁移要求。

Grad-Ops 是具备通用 Shell 能力、但不把主机权限交给模型的 Linux 运维 Agent 与控制平面。Agent 可动态组合 Bash 命令、脚本、管道、重定向和命令替换；Shell 是通用执行接口，不是预定义运维动作菜单。命令经策略授权后进入受限 Linux Runtime 执行：

`自然语言请求 → 只读取证 → 证据绑定 → 确定性策略 → 动作契约 → 人工审批 → 受限执行 → 独立验证 → 审计`

## 设计原则

- **LLM 不可信**：模型可动态提出 Shell 命令和脚本，但不能直接执行、授予权限或自行降低风险。
- **证据约束**：事实性结论必须引用当前任务中真实存在的 evidence ID。
- **分层安全控制**：Policy 判断操作权限与风险，Approval 处理高风险授权，Linux Runtime 施加最终资源和权限边界；AST 用于辅助分析与审计，不作为唯一安全边界。
- **副作用唯一入口**：所有写操作经过 Executor、契约校验、资源边界和审批闸门。
- **独立验证**：执行后重新观测状态，不复用调查阶段的证据来证明执行成功。
- **Linux-only**：不支持 Windows/macOS，不保留跨平台适配层。
- **可追溯**：任务、审批、执行、验证与恢复均形成审计事件；哈希链的信任边界在威胁模型中明确说明。

## 文档索引

| 文档 | 用途 |
| --- | --- |
| [Go + Linux 核心规格](docs/specs/2026-10-26-go-linux-core-spec.md) | 目标、边界、职责、状态机、数据模型与验收条件 |
| [架构设计](docs/architecture.md) | Go 包职责、依赖规则、Memory/Knowledge、Linux 执行模型和部署边界 |
| [记忆与知识库设计](docs/design/memory-and-knowledge.md) | Task History、Memory、Knowledge 与实时 Evidence 的边界及分阶段实现方案 |
| [威胁模型](docs/threat-model.md) | 提示注入、Shell 动态执行、越权、审批重放、文件系统竞态等威胁 |
| [Tool 层与动态 Shell 设计](docs/design/tool-layer-and-adapters.md) | Shell AST、能力策略、动作契约、隔离执行与未来 Adapter 边界 |
| [Go + Linux 实施计划](docs/plans/2026-10-26-go-linux-implementation-plan.md) | 分阶段实施与每阶段验收标准 |
| [多主机 Go Agent 旧方案](docs/plans/2026-10-26-multi-host-go-agent.md) | 未来扩展参考；首期不实现 |
| [核心闭环旧实施计划](docs/plans/2026-10-14-grad-ops-core-loop.md) | Python 版本的历史实现参考 |
| [测试报告](docs/test-report.md) | 旧 Python 版本的历史测试基线，不等同于 Go 版测试结果 |

## 目标技术栈

- Go 后端，REST/JSON API 与持久化 Worker。
- SQLite，使用显式迁移、事务和任务租约。
- 动态 Linux Shell 执行层：以持久 Bash Session 提供通用 Shell 能力；Policy/Approval 控制主机操作权限，受限 Runtime 提供 OS 级隔离、资源预算和进程回收；AST 作为辅助分析手段。
- OpenAI 兼容 LLM API，并提供 mock 模式用于离线测试。
- 现有 Vue 3 + Vite + Arco Design 前端沿用，通过 REST API 接入 Go 后端。
- Ubuntu LTS 作为首期开发和 CI 基线；其他发行版须经工具行为测试后再声明支持。

## 首期明确不做

- 多主机扇出、远程 Agent、Agent 自动升级；
- 未经 Policy 授权与 Runtime 边界约束的主机操作；任意宿主机提权、任意 sudo 和不受控文件系统/网络访问；
- Windows/macOS 适配；
- MCP、Kubernetes、外部工具平台、分布式事务和主控高可用。
- 向量数据库、Embedding 服务和复杂的长期记忆自动提取。

Shell、Policy、Approval 与 Linux Runtime 的职责边界见 [Tool 层安全与 Adapter 扩展设计](docs/design/tool-layer-and-adapters.md)。

## 实施入口

从 [Go + Linux 实施计划](docs/plans/2026-10-26-go-linux-implementation-plan.md) 的 Phase 0 开始。按规格定义的目标能力从零设计并实现 Go 系统；不要把 Python 模块逐文件翻译成 Go，也不要求兼容或复刻旧 Python 架构。先冻结状态机、动作契约、错误模型和测试向量，再按 Go 的职责边界实现。

## 当前实现状态

分支 `feat/phase1-go-skeleton`。每一项都对应代码与测试；没有测试覆盖的能力在表中明确标注。

| 能力 | 状态 |
| --- | --- |
| 配置校验、JSON 日志、优雅关闭、健康检查 | 已实现 |
| SQLite 版本化迁移、`STRICT` 表、事务封装 | 已实现 |
| 任务状态机（单一迁移入口）、追加式审计哈希链 | 已实现 |
| 任务、时间线、证据、提案 API | 已实现 |
| Worker 认领、租约、重试、启动恢复、执行循环 | 已实现 |
| 通用 Bash Session：超时、输出截断、调用预算、进程组回收、环境隔离 | 已实现（`shell.Session` 每次调用独立进程；`shell.Console` 提供常驻会话与跨调用状态） |
| 调查：探针 → 证据、结论必须引用证据、提议动作 | 已实现（计划器是确定性替身） |
| 确定性 Policy（R0–R4） | 已实现 |
| 动作契约：规范化 JSON + SHA-256 摘要 | 已实现 |
| 一次性审批：只存哈希、绑定契约摘要、单条 SQL 原子消费 | 已实现 |
| Executor：重算摘要、复核策略、原子占用、执行 | 已实现 |
| 独立验证：执行后重新观测，通过则封存 | 已实现 |
| 回滚 / 补偿动作 | 已实现：契约内声明补偿动作，验证失败 → `ROLLING_BACK` → `ROLLED_BACK` / `RECOVERY_FAILED` |
| LLM 接入 | **未实现**：`StubPlanner` 只跑固定只读探针，不解释请求 |
| Linux 隔离（uid 切换、namespace、cgroup、systemd） | **未实现**：目前只有进程组、超时、输出上限和环境白名单 |
| 前端联调 | 已实现：`frontend/` 已对齐 Go 接口，`npm run build`（含 `vue-tsc`）通过，dev 代理与真实后端联调验证 |

### 已验证 / 未验证

- **已验证（Windows + Git Bash）**：队列、租约与恢复、Shell 边界、证据引用、策略判定、契约摘要、审批的绑定与重放拒绝、执行、独立验证、补偿回滚、审计链，以及一条跑通全流程的端到端测试与一次真实二进制冒烟；前端 `npm run build` 通过，dev 代理联调跑通了"创建任务 → Worker 调查 → 封存 → 审计校验"。
- **未验证**：全部 Linux 专属行为。本仓库的二进制面向 Linux，但当前开发机没有可用的 Linux 环境；`GOOS=linux` 只做了交叉编译与 `go vet`，不能替代运行时验证。
- 因此 `Linux Runtime` 一栏标记为未实现，而不是"应该可以"。

### 一条任务会经历什么

```
POST /api/tasks
  └─ QUEUED，写入 investigate 作业
       └─ Worker 认领（租约）→ INVESTIGATING
            └─ 调查：只读探针 → evidence（内容寻址）
                 ├─ 无副作用 → 结论必须引用 evidence → SEALED
                 └─ 有副作用 → Policy 定价
                      ├─ DENY  → REJECTED（不产生 proposal）
                      └─ 通过  → 冻结契约 → AWAITING_APPROVAL
                           └─ 人工批准 → 一次性 token（只存哈希）
                                └─ POST /execute → EXECUTING
                                     └─ 结果写入 execution_records + evidence
                                          ├─ SUCCEEDED → VERIFYING
                                          │    └─ 独立验证 → SEALED
                                          │          ├─ 失败且有补偿 → ROLLING_BACK → ROLLED_BACK / RECOVERY_FAILED
                                          │          └─ 失败且无补偿 → FAILED(verification)
                                          ├─ FAILED    → FAILED(execution)
                                          └─ UNKNOWN   → FAILED(outcome_unknown)，永不重放
```

租约恢复的规则是显式的：过期租约只说明持有者停止续租，不说明工作是否完成。只读任务（`investigate`）会重新入队重试；带副作用的任务不会自动重放，而是记为 `UNKNOWN` 并把任务置为 `FAILED`（`failure_stage=outcome_unknown`），交由人工确认。

### 已知限制（不是待办清单的装饰）

- **计划器是替身**：默认部署只做只读调查并直接封存任务，不会产生提案。审批与执行路径通过测试与显式调用验证，而不是默认就会发生。
- **执行身份**：契约要求声明 `run_as`，并且拒绝 `root`；但服务本身没有能力切换到该身份，脚本默认以服务进程身份运行。设置 `GRADOPS_REQUIRE_RUN_AS_MATCH=true` 后，身份不一致的契约会被直接拒绝并写入 `execution.rejected` 审计，而不是"照契约声明、按进程身份执行"。真正的身份隔离属于未实现的 Runtime。
- **策略是模式匹配**：危险命令检测是正则，不是 AST；它用于拒绝明显危险的动作，真正的边界是 Runtime，而 Runtime 尚未实现。
- **审计链**只防御朴素篡改，DB 拥有者仍可重写整条链；信任边界见[威胁模型](docs/threat-model.md)。

## 本地运行与测试

```bash
go build -o bin/gradops ./cmd/gradops
GRADOPS_DB_PATH=./data/gradops.db GRADOPS_ALLOWED_ROOTS=/var/log ./bin/gradops
go test ./...
```

Linux 上使用默认值即可；`GRADOPS_BASH` 默认 `/bin/bash`。当前开发机是 Windows，冒烟时用 Git Bash 并显式指定工作目录：

```powershell
$env:GRADOPS_BASH='C:\Program Files\Git\bin\bash.exe'
$env:GRADOPS_WORK_DIR=(Resolve-Path .\data).Path
$env:GRADOPS_CONTRACT_WORK_DIR='/work'
$env:GRADOPS_ALLOWED_ROOTS='/var/log'
go test ./...
```

需要 shell 的测试在找不到可用 Bash 时会 `t.Skip`，而不是假装通过。

环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GRADOPS_ADDR` | `127.0.0.1:8080` | 监听地址，必须为 `host:port` |
| `GRADOPS_DB_PATH` | `./data/gradops.db` | SQLite 文件路径 |
| `GRADOPS_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `GRADOPS_MAX_REQUEST_BYTES` | `1048576` | 请求体上限，最大 8 MiB |
| `GRADOPS_BASH` | `/bin/bash` | 绝对路径；启动时校验存在且可执行 |
| `GRADOPS_WORK_DIR` | `/` | 探针与动作运行的宿主目录 |
| `GRADOPS_CONTRACT_WORK_DIR` | `/` | 写入契约的 POSIX 路径；开发机上与 `WORK_DIR` 不同 |
| `GRADOPS_ALLOWED_ROOTS` | `/var/log` | 冒号分隔，动作可声明路径的上界 |
| `GRADOPS_POLICY_VERSION` | `2026-11-01` | 写入契约，便于追溯 |
| `GRADOPS_APPROVAL_TTL` | `15m` | 审批 token 有效期，最长 24h |
| `GRADOPS_PROBE_TIMEOUT` | `10s` | 单次探针/验证命令超时 |
| `GRADOPS_MAX_OUTPUT_BYTES` | `65536` | 每个输出流上限 |
| `GRADOPS_WORKER` | `true` | 是否在本进程运行作业循环 |
| `GRADOPS_REQUIRE_RUN_AS_MATCH` | `false` | 开启后契约声明的 `run_as` 必须等于当前进程身份，否则拒绝执行 |

接口：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/healthz`、`/api/health` | 健康检查 |
| `POST` | `/api/tasks` | 创建任务，请求体 `{"request_text": "..."}` |
| `GET` | `/api/tasks?status=&limit=` | 任务列表 |
| `GET` | `/api/tasks/{id}` | 任务详情 |
| `GET` | `/api/tasks/{id}/timeline` | 审计时间线 |
| `GET` | `/api/tasks/{id}/evidence` | 证据列表（含内容哈希） |
| `POST` | `/api/tasks/{id}/verify` | 执行后独立验证 |
| `POST` | `/api/tasks/{id}/recover` | 验证失败后执行契约内补偿动作 |
| `GET` | `/api/approvals?status=` | 审批队列（区分待执行 / 已消费 / 已过期 / 已拒绝） |
| `GET` | `/api/proposals?task_id=&status=` | 提案列表 |
| `GET` | `/api/proposals/{id}` | 提案与冻结的契约 |
| `POST` | `/api/proposals/{id}/approvals` | 批准/拒绝，批准时返回一次性 token |
| `POST` | `/api/proposals/{id}/execute` | 持 token 执行 |
| `GET` | `/api/tasks/{id}/executions` | 执行记录 |
| `GET` | `/api/tasks/{id}/verifications` | 验证记录 |
| `GET` | `/api/safety` | 运行边界与防护闸门（未实现项标注为 warn） |
| `GET` | `/api/audit/verify` | 审计链校验 |

最小冒烟（只读闭环）：启动后创建任务，worker 会自动调查并封存；随后读取证据与时间线。已实现的 Go 测试在 Linux CI 通过之前，不应被当作 Linux 环境下的验证结果。

### 控制台

```bash
cd frontend
npm ci
npm run build     # vue-tsc 类型检查 + vite 构建
npm run dev       # 默认把 /api 代理到 127.0.0.1:8080
```

后端不在默认端口时用 `GRADOPS_BACKEND=http://127.0.0.1:18080 npm run dev` 覆盖代理目标。

### 端到端演示

```bash
bash scripts/demo.sh                 # 目标平台：构建 → 启动 → 建任务 → 等待封存 → 打印证据、时间线与审计校验
pwsh scripts/demo.ps1 -Addr 127.0.0.1:18080   # 开发机：Git Bash 作为 shell
```

两个脚本都会在任务没有到达 `SEALED` 或审计链校验失败时以非零码退出，所以它是一次冒烟测试，而不是"总会成功"的演示。开发机上要显式给出 `GRADOPS_BASH`（Git Bash 的绝对路径）与 `GRADOPS_WORK_DIR`。

控制台里的"批准并执行"是两次调用：先取得一次性 token，再带着它调用执行接口，token 不落盘；刷新页面后 token 即丢失，需要等审批过期再重新批准，而不是偷偷保留一个可用凭证。
