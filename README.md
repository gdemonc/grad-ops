# grad-ops

证据约束型自主运维控制平面 · Go + Linux

> **项目实现方式：** Greenfield Go implementation（Go 全新实现）。  
> **目标架构：** Go 后端 + Linux-only 工具层 + SQLite + 沿用 Vue 3 前端。  
> **实现状态：** 单机闭环的机制已实现并有测试覆盖（队列 → 调查 → 证据 → 策略 → 契约 → 审批 → 执行 → 独立验证 → 补偿回滚 → 审计），控制台已对接 Go 接口；LLM 客户端、两阶段计划器和可选的多轮只读 Agent Loop 已实现（默认仍用 stub），Linux Runtime 的 per-action cgroup v2（可选）、uid 切换、prlimit 资源限制和可选的 bubblewrap 命名空间/文件系统沙箱已实现，并已在真实 systemd 服务中验证。详见[当前实现状态](#当前实现状态)。

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
| [测试矩阵与验证现状](docs/testing.md) | Go 版：属性 → 测试对照、运行环境、未覆盖清单 |
| [Go 版本策略](docs/go-version-policy.md) | 最低兼容版本、开发/CI 工具链、兼容性约束与升级流程 |
| [systemd 部署](deploy/README.md) | 非 root 账号、unit 加固、安装、验证与当前边界 |

## 目标技术栈

- Go 后端，REST/JSON API 与持久化 Worker；最低 Go 1.26.0，开发/CI 工具链 Go 1.27.3（见 [Go 版本策略](docs/go-version-policy.md)）。
- SQLite，使用显式迁移、事务和任务租约。
- 动态 Linux Shell 执行层：以持久 Bash Session 提供通用 Shell 能力；Policy/Approval 控制主机操作权限，受限 Runtime 提供 OS 级隔离、资源预算和进程回收；AST（`mvdan.cc/sh/v3`）作为辅助分析手段。
- OpenAI 兼容 LLM 客户端；测试使用 `llm.CompleterFunc` 作为离线替身，不需要真实密钥。
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
| API 认证 | 已实现：可选 `GRADOPS_API_TOKEN` Bearer token；无 token 时只允许回环监听；尚无用户/角色授权 |
| Worker 认领、租约、重试、启动恢复、执行循环 | 已实现 |
| 通用 Bash Session：超时、输出截断、调用预算、进程组回收、环境隔离 | 已实现（`shell.Session` 每次调用独立进程；`shell.Console` 提供常驻会话与跨调用状态） |
| Shell 脚本辅助分析（AST） | 已实现：基于 `mvdan.cc/sh/v3` 识别写操作、间接执行和动态命令名；探针、`observe` 动作和验证命令在运行前做 fail-closed 校验。它不是策略引擎或沙箱 |
| 调查：探针 → 证据、结论必须引用证据、提议动作 | 已实现；默认 `StubPlanner`，可配两阶段 `model` 或可选多轮只读 Agent Loop |
| 确定性 Policy（R0–R4） | 已实现 |
| 动作契约：规范化 JSON + SHA-256 摘要 | 已实现 |
| 一次性审批：只存哈希、绑定契约摘要、单条 SQL 原子消费 | 已实现 |
| Executor：重算摘要、复核策略、原子占用、执行 | 已实现 |
| Linux 资源限制：CPU、地址空间、进程数、文件大小、文件描述符 | 已实现：通过 `prlimit` 在 shell 启动前应用，默认开启，可逐项设为 `0` 关闭 |
| per-action cgroup v2 边界 | 已实现并验证：`GRADOPS_RUNTIME=cgroup-v2` 时每个 shell 启动在独立子 cgroup，设置内存/进程/CPU 限制；真实 systemd delegated cgroup 上验证了 `memory.max`、`pids.max`、`cpu.max` 和超时清理。kill/close 失败会向调用者报告，启动时清理残留的 `gradops-*` 子 cgroup |
| 执行身份切换 | 部分实现：配置 `GRADOPS_EXECUTOR_USER` 后以该身份运行探针、动作和验证；非 root 无法切换时启动失败。未配置时仍以服务进程身份运行 |
| 文件目标路径安全 | 部分实现：`internal/fileops` 使用 openat2 做 fd 相对打开和身份绑定；Executor 已接入受控 `fileops` append，HTTP → Worker → Approval → Executor E2E 通过；只支持已有普通文件追加，任意 Bash 副作用仍未接入 |
| 独立验证：执行后重新观测，通过则封存 | 已实现 |
| 回滚 / 补偿动作 | 已实现：契约内声明补偿动作，验证失败 → `ROLLING_BACK` → `ROLLED_BACK` / `RECOVERY_FAILED` |
| LLM 接入 | 已实现客户端、两阶段计划器和可选 Agent Loop；DeepSeek `deepseek-flash` 已用真实 Key 联调通过，默认仍为 `StubPlanner`，其他 provider/model 未验证 |
| 多轮只读诊断 Agent Loop | 已实现（可选）：`GRADOPS_AGENT_LOOP=true` 时模型每轮看到已保存的 Evidence，选择 `observe` 或 `finish`；支持单轮多条串行观察、轮数/调用/上下文预算、finalization-only 收尾、按 attempt 区分证据、逐条 Evidence/Audit；默认关闭 |
| per-action bubblewrap 沙箱 | 已实现并验证：`GRADOPS_SANDBOX=bubblewrap` 时使用独立 mount/PID/IPC/UTS/user/network namespace、只读根和按策略绑定的可写根；真实 systemd 服务上启动校验通过，任务探针在沙箱内完成并封存 |
| Linux 隔离（systemd、文件系统、网络） | **已实现（可选）**：bubblewrap 沙箱提供 namespace、只读根和默认隔离网络；默认 `.env.example` 为 `none`，部署示例启用 `bubblewrap` |
| 前端联调 | 已实现：`frontend/` 已对齐 Go 接口，`npm run build`（含 `vue-tsc`）在 Ubuntu 上通过；dev 代理联调沿用之前的验证结果 |
| Linux CI | 已实现：GitHub Actions 在 Ubuntu 上跑 build/vet/test/race，并单独验证最低 Go 1.26.0（[Linux CI workflow](https://github.com/gdemonc/grad-ops/actions/workflows/linux-ci.yml)） |
| systemd 部署 | 已实现：hardened unit、环境文件、安装脚本和静态检查；要求 systemd 254+（`DelegateSubgroup`）；已在真实 systemd 主机安装运行，per-action cgroup 验证通过；`ProtectSystem`/`PrivateTmp` 等 unit 边界的逐项运行效果未单独测试 |

### 已验证 / 未验证

- **已验证（Ubuntu 26.04.1 LTS + Go 1.27.3，2026-11-10）**：`go build ./...`、`go vet ./...`、`go test ./... -count=1` 与 `go test -race ./... -count=1` 全部通过；`scripts/demo.sh` 以真实二进制跑通"创建任务 → Worker 调查 → 封存 → 证据/时间线 → 审计链校验"。prlimit 的资源限制由 `ulimit` 断言验证；非 root 进程无法切换到其他用户时在启动期失败；LLM 客户端与两阶段计划器由本地 `httptest`、函数替身和一次本地 fake provider 端到端冒烟验证（任务以模型结论封存）；另于 2026-11-11 用真实 DeepSeek `deepseek-flash` 跑通 `TestDeepSeekAgentLoopRealProvider`，模型执行 `wc`、`grep`、`tail` 三条只读观察并正确总结 fixture 日志；同一配置下真实 systemd 服务的 task 3 以 `ls`/`grep` 两条 Evidence 到达 `SEALED`，attempt=1；task 4 完成真实多轮：round 1 读取 step1，round 2 根据 step1 内容读取 step2，round 3 finish，三份审计事件按 round 记录；API Bearer 认证由 `httptest` 和一次真实二进制冒烟覆盖（health=200、无 token=401、带 token=200）；per-action cgroup v2 在真实 systemd delegated cgroup 上通过 shell 集成测试（`memory.max=67108864`、`pids.max=32`、`cpu.max=50000 100000`、超时清理、无残留子 cgroup）；bubblewrap 沙箱在真实 systemd 服务中完成启动探测，worker 探针在沙箱内执行并到达 `SEALED`，root-only/loopback-only 集成测试通过。
- **未验证**：uid 切换的成功路径（需要 root 和真实目标账号）、cgroup 的 `cpuset`/`io` 控制器。这些不能因为测试套件全绿而被视为已实现。
- **Linux CI**：GitHub Actions `Linux CI` 已在 `feat/phase1-go-skeleton` 上成功执行 build/vet/test/race；最低 Go 1.26.0 由独立 job 验证（[Linux CI workflow](https://github.com/gdemonc/grad-ops/actions/workflows/linux-ci.yml)）。
- 前端 `npm run build`（含 `vue-tsc`）已在本机重新验证通过；dev 代理与真实后端的联调沿用此前的开发机结果，本次未重跑。

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

- **默认计划器是替身**：`GRADOPS_PLANNER` 默认为 `stub`，只做固定只读调查并直接封存任务，不会产生提案。设置为 `model` 后才调用 OpenAI 兼容 provider；`GRADOPS_AGENT_LOOP=true` 才启用多轮 observe/finish，否则仍走两阶段。DeepSeek `deepseek-flash` 已做真实联调（直接测试 + systemd 服务任务 + 两轮动态调查），Key 读取 `GRADOPS_LLM_API_KEY` 或仅 deepseek provider 下的 `DEEPSEEK_API_KEY`；其他 provider/model 尚未验证；模型输出仍只被当作不可信输入。
- **bubblewrap 沙箱**：代码已实现，部署示例启用 `bubblewrap`；真实 systemd 服务上启动探测通过，任务探针在沙箱内完成并封存。默认 `.env.example` 仍为 `none`，需要显式启用。
- **Agent 重试**：Worker 每次尝试是独立 attempt；Evidence 按 Task + Attempt 关联，重试不自动恢复上一次的模型上下文，旧 attempt 只保留审计追溯。
- **文件路径安全**：`internal/fileops` 已提供 fd 相对的安全打开、目标身份绑定、受控 append 和 Executor 测试；当前只支持已有普通文件的追加，需要 `GRADOPS_FILE_ROOTS` 且要求 `run_as` 与当前进程身份一致；任意 Bash 动作仍未接入这个 API。
- **隔离粒度**：`Session` 每次 `Run` 新建 cgroup/namespace；`Console` 的 cgroup/namespace 从 `NewConsole` 存活到 `Close`，一次调查的多条探针共享它，但不会跨 Action 复用。可写路径是全局 `GRADOPS_ALLOWED_ROOTS`，网络只有隔离/共享两种模式，没有 IP/端口/DNS allowlist；symlink/TOCTOU 未防护。
- **per-action cgroup**：`cgroup-v2` 模式已安装到真实 systemd 主机，并在 delegated cgroup 上用 shell 集成测试验证了子 cgroup 创建、`memory.max`/`pids.max`/`cpu.max` 生效和超时清理；默认仍是 `direct`，只使用 prlimit。systemd unit 已配置 `Delegate=yes`、`DelegateSubgroup=supervisor`。
- **执行身份**：契约要求声明 `run_as`，并且拒绝 `root`。配置 `GRADOPS_EXECUTOR_USER` 后，服务会解析该账号并真正切换 uid/gid；非 root 进程无法切换到其他用户时启动失败。未配置时仍以服务进程身份运行，此时设置 `GRADOPS_REQUIRE_RUN_AS_MATCH=true` 会拒绝身份不一致的契约并写入 `execution.rejected`。uid 切换的成功路径尚未在 root 环境实测。
- **策略仍是模式匹配**：危险命令检测是正则，不是 AST。新增的 AST 分析只用于校验“探针/observe/验证命令是否只读”，它不认识所有命令，也不解析运行时展开；未知命令仍可能写文件。真正的边界是 Runtime，而 Runtime 目前提供进程组、超时、输出上限、环境白名单、prlimit 资源限制、可选 uid 切换、可选 per-action cgroup v2 和可选 bubblewrap 沙箱；bubblewrap 默认关闭，部署示例启用并已在真实 systemd 服务中验证。
- **资源限制依赖 util-linux**：`prlimit` 缺失时服务会拒绝启动；限制值默认适合只读探针和小文件，不保证兼容所有运维命令。
- **时限**：`GRADOPS_INVESTIGATION_TIMEOUT` 限制调查阶段（探针 + 模型调用），默认 5 分钟；`GRADOPS_EXECUTION_TIMEOUT` 限制单个已批准动作/补偿的紧急执行上限，默认 5 分钟。审批等待和人工复核没有任务级时限。
- **API 认证**：设置 `GRADOPS_API_TOKEN` 后所有 `/api` 路由要求 Bearer token；未设置时只允许 `127.0.0.1` / `localhost` / `::1` 监听，同机进程仍可无认证调用。当前没有用户、角色或按任务的细粒度授权。
- **DeepSeek 联调**：`GRADOPS_LLM_PROVIDER=deepseek` 使用 `https://api.deepseek.com` 和 `deepseek-flash`，API Key 只从环境变量读取；2026-11-11 的直接测试和 systemd 服务任务均已通过，测试默认仍 skip，需显式设置 `GRADOPS_TEST_REAL_LLM=1`。
- **模型数据边界**：`GRADOPS_PLANNER=model` 会把任务文本和截断后的探针观测发送给配置的 provider；启用 `GRADOPS_AGENT_LOOP=true` 时，每一轮都会把最近的 Evidence 再次发送。尚未实现提示内容的秘密扫描。API Key 不进入 shell 环境、日志或数据库，但 provider 本身必须被信任到相应程度。默认 `stub` 不发送任何外部数据。
- **审计链**只防御朴素篡改，DB 拥有者仍可重写整条链；信任边界见[威胁模型](docs/threat-model.md)。

## 本地运行与测试

```bash
go build -o bin/gradops ./cmd/gradops
GRADOPS_DB_PATH=./data/gradops.db GRADOPS_ALLOWED_ROOTS=/var/log ./bin/gradops
go test ./...
```

要求 Go 1.26.0 或更高版本；当前开发与 CI 使用 Go 1.27.3。最低版本可以用 `GOTOOLCHAIN=go1.26.0` 复现，策略见 [Go 版本策略](docs/go-version-policy.md)。

Linux 上使用默认值即可；`GRADOPS_BASH` 默认 `/bin/bash`。资源限制默认通过 `/usr/bin/prlimit` 应用；该工具缺失时服务会拒绝启动，而不是静默取消限制。需要以另一个身份运行时，服务本身必须是 root：

```bash
GRADOPS_EXECUTOR_USER=gradops-agent GRADOPS_ALLOWED_ROOTS=/var/log ./bin/gradops
```

如果服务不是 root，而 `GRADOPS_EXECUTOR_USER` 指向另一个用户，启动会失败并说明无法切换身份。需要 shell 或 prlimit 的测试在工具缺失时会 `t.Skip`，而不是假装通过。

systemd 部署：

```bash
go build -o bin/gradops ./cmd/gradops
sudo deploy/install.sh ./bin/gradops
sudo scripts/verify-cgroup.sh
```

细节、权限和边界见 [systemd 部署](deploy/README.md)。

环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GRADOPS_ADDR` | `127.0.0.1:8080` | 监听地址，必须为 `host:port` |
| `GRADOPS_DB_PATH` | `./data/gradops.db` | SQLite 文件路径 |
| `GRADOPS_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `GRADOPS_MAX_REQUEST_BYTES` | `1048576` | 请求体上限，最大 8 MiB |
| `GRADOPS_API_TOKEN` | 空 | `/api` 的 Bearer token，至少 16 字符；为空时只允许回环监听 |
| `GRADOPS_BASH` | `/bin/bash` | 绝对路径；启动时校验存在且可执行 |
| `GRADOPS_WORK_DIR` | `/` | 探针与动作运行的宿主目录 |
| `GRADOPS_CONTRACT_WORK_DIR` | `/` | 写入契约的 POSIX 路径；开发机上与 `WORK_DIR` 不同 |
| `GRADOPS_ALLOWED_ROOTS` | `/var/log` | 冒号分隔，动作可声明路径的上界 |
| `GRADOPS_FILE_ROOTS` | 空 | 冒号分隔，fd 文件操作的可信根目录；必须位于 AllowedRoots 内，空表示禁用文件操作 |
| `GRADOPS_POLICY_VERSION` | `2026-11-01` | 写入契约，便于追溯 |
| `GRADOPS_APPROVAL_TTL` | `15m` | 审批 token 有效期，最长 24h |
| `GRADOPS_PROBE_TIMEOUT` | `10s` | 单次探针/验证命令超时 |
| `GRADOPS_INVESTIGATION_TIMEOUT` | `5m` | 单次调查的总时限（探针 + 模型调用），最长 30 分钟 |
| `GRADOPS_RUNTIME` | `direct` | `direct` 或 `cgroup-v2`；后者要求可写的 delegated cgroup v2 子树 |
| `GRADOPS_CGROUP_ROOT` | 空 | 可选，delegated cgroup 目录；为空时从 `/proc/self/cgroup` 推导 |
| `GRADOPS_CGROUP_MEMORY_MAX_BYTES` | `536870912` | 每个 shell 的 cgroup 内存上限 |
| `GRADOPS_CGROUP_PIDS_MAX` | `256` | 每个 shell 的 cgroup 进程数上限 |
| `GRADOPS_CGROUP_CPU_QUOTA_MICROS` | `0` | cgroup CPU 配额；`0` 表示不节流 |
| `GRADOPS_CGROUP_CPU_PERIOD_MICROS` | `100000` | cgroup CPU 周期 |
| `GRADOPS_SANDBOX` | `none` | `none` 或 `bubblewrap` |
| `GRADOPS_BWRAP` | `/usr/bin/bwrap` | bubblewrap 绝对路径 |
| `GRADOPS_SANDBOX_NETWORK` | `false` | `true` 保留宿主网络命名空间；默认只有 loopback |
| `GRADOPS_EXECUTION_TIMEOUT` | `5m` | 单个已批准动作/补偿的紧急执行上限，最长 24 小时；契约自身的 Runtime 超时仍然有效 |
| `GRADOPS_MAX_OUTPUT_BYTES` | `65536` | 每个输出流上限 |
| `GRADOPS_WORKER` | `true` | 是否在本进程运行作业循环 |
| `GRADOPS_REQUIRE_RUN_AS_MATCH` | `false` | 开启后契约声明的 `run_as` 必须等于当前进程身份，否则拒绝执行 |
| `GRADOPS_EXECUTOR_USER` | 空 | 探针、动作和验证实际运行的身份；为空表示使用服务进程身份。设置后契约的 `run_as` 必须与之一致 |
| `GRADOPS_PRLIMIT` | `/usr/bin/prlimit` | util-linux prlimit 的绝对路径；仅在启用资源限制时使用 |
| `GRADOPS_RLIMIT_CPU` | `10s` | 单个 shell 的 CPU 时间上限；`0` 表示不限制 |
| `GRADOPS_RLIMIT_AS_BYTES` | `536870912` | 地址空间上限（512 MiB）；`0` 表示不限制 |
| `GRADOPS_RLIMIT_NPROC` | `256` | 服务用户可创建的进程数上限；`0` 表示不限制 |
| `GRADOPS_RLIMIT_FSIZE_BYTES` | `16777216` | 单个文件大小上限（16 MiB）；`0` 表示不限制 |
| `GRADOPS_RLIMIT_NOFILE` | `256` | 打开文件描述符上限；`0` 表示不限制 |
| `GRADOPS_PROBE_CONSOLE` | `false` | 开启后同一次调查的探针共享一个常驻 Bash、cgroup 和 sandbox；默认每次探针独立进程/边界 |
| `GRADOPS_PLANNER` | `stub` | `stub`：固定只读探针；`model`：OpenAI 兼容模型规划 |
| `GRADOPS_LLM_BASE_URL` | 空 | `model` 模式必填，例如 `https://api.example.com/v1`；仅回环地址允许明文 HTTP |
| `GRADOPS_LLM_API_KEY` | 空 | Bearer token；不写日志、不落库 |
| `GRADOPS_LLM_MODEL` | 空 | `model` 模式必填 |
| `GRADOPS_LLM_TIMEOUT` | `30s` | 单次 provider 请求超时，最长 5 分钟 |
| `GRADOPS_LLM_MAX_RESPONSE_BYTES` | `1048576` | provider 响应体上限 |
| `GRADOPS_LLM_PROVIDER` | `openai` | `openai` 或 `deepseek`；deepseek 自动填充 base URL/model，且要求 API Key（也接受 `DEEPSEEK_API_KEY`） |
| `GRADOPS_LLM_MAX_PROBES` | `6` | 单次调查最多探针数 |
| `GRADOPS_AGENT_LOOP` | `false` | 多轮 observe/finish；需要 `GRADOPS_PLANNER=model` |
| `GRADOPS_AGENT_MAX_ROUNDS` | `20` | 最大决策轮数，2–20；最后一轮只允许 finish |
| `GRADOPS_AGENT_MAX_CALLS` | `40` | 整个调查的最大观察命令数，1–200 |
| `GRADOPS_AGENT_MAX_CALLS_PER_ROUND` | `5` | 每轮最多观察命令数，1–8 |
| `GRADOPS_AGENT_CONTEXT_BYTES` | `32768` | 回传给模型的观察上下文上限 |
| `GRADOPS_LLM_MAX_OBSERVATION_BYTES` | `4096` | 结论提示中单条观测的上限 |
| `GRADOPS_LLM_MAX_TOKENS` | `1024` | 单次补全 token 上限 |

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

最小冒烟（只读闭环）：启动后创建任务，worker 会自动调查并封存；随后读取证据与时间线。Linux CI 与本地 Ubuntu 结果均可在 Actions / `docs/testing.md` 追溯。

### 控制台

```bash
cd frontend
npm ci
npm run build     # vue-tsc 类型检查 + vite 构建
npm run dev       # 默认把 /api 代理到 127.0.0.1:8080
```

后端不在默认端口时用 `GRADOPS_BACKEND=http://127.0.0.1:18080 npm run dev` 覆盖代理目标。若后端设置了 `GRADOPS_API_TOKEN`，在浏览器 `localStorage` 写入同名的 `gradops_api_token` 后刷新页面；token 不打包进前端产物。

### 端到端演示

```bash
bash scripts/demo.sh                 # 构建 → 启动 → 建任务 → 等待封存 → 打印证据、时间线与审计校验
```

两个脚本都会在任务没有到达 `SEALED` 或审计链校验失败时以非零码退出，所以它是一次冒烟测试，而不是"总会成功"的演示。设置 `GRADOPS_API_TOKEN` 时，两个脚本会自动把同一个 token 作为 Bearer 头发给 API。开发机上要显式给出 `GRADOPS_BASH`（Git Bash 的绝对路径）与 `GRADOPS_WORK_DIR`。

控制台里的"批准并执行"是两次调用：先取得一次性 token，再带着它调用执行接口，token 不落盘；刷新页面后 token 即丢失，需要等审批过期再重新批准，而不是偷偷保留一个可用凭证。
