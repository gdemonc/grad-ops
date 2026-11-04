# grad-ops

证据约束型自主运维控制平面 · Go + Linux

> **项目实现方式：** Greenfield Go implementation（Go 全新实现）。  
> **目标架构：** Go 后端 + Linux-only 工具层 + SQLite + 沿用 Vue 3 前端。  
> **实现状态：** 当前仓库以需求规格、架构设计、威胁模型和实施计划为基线；Go 后端按计划从零构建。

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

在 Go 版代码与测试真正落地前，本 README 不提供 Go 运行命令，也不声称 Go 版已经具备旧 Python 版本的功能或测试覆盖率。
