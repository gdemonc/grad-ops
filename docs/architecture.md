# Grad-Ops 架构（Go + Linux）

> 当前目标架构：Go 后端、Linux 唯一支持平台、SQLite 持久化、Vue 3 前端沿用。  
> 本文取代早期 Python 主控架构作为新实现的设计基线；旧实现仅用于行为参考和历史测试基线。

## 1. 系统定位

Grad-Ops 是一个证据约束型运维控制平面。它不是任意 Shell Agent，也不是单纯的主机监控面板。系统将自然语言请求转换为有限的调查流程和受控动作，并通过确定性策略、人工审批、独立验证与审计保证动作可解释、可拒绝、可追踪。

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

系统工具由固定注册表提供，不允许模型指定任意命令。执行使用 `exec.CommandContext` 和固定参数数组，不经过 `sh -c`。每个工具定义：

- 固定工具名与可执行文件；
- 输入参数 schema 与允许值；
- 只读/副作用标记、风险基线；
- 超时、输出字节上限和环境变量；
- 输出解析器、错误分类和测试夹具。

文件副作用必须对实际使用的文件对象实施边界检查，而不是只对字符串路径做检查。对符号链接、路径替换和 TOCTOU 风险，采用 dirfd/openat 系列安全原语；如果安全原语无法满足约束，拒绝执行。

## 5. 任务状态与事务

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

## 8. 部署

- 单个 Go 服务二进制，配置从环境变量或受权限保护的配置文件读取。
- SQLite 数据库和审计相关文件位于服务专用目录，权限最小化。
- systemd 管理服务；服务默认使用专用非 root 账号。
- 日志不输出密钥、审批原始令牌或未经处理的敏感日志内容。
- 提供 Linux shell 脚本；不再提供 PowerShell 和 Windows 启动流程。

## 9. 暂不实现的扩展

- 多主机 Agent、mTLS 注册与远程命令协议；
- 主控高可用、分布式数据库、分布式事务；
- Kubernetes Operator；
- 自动升级；
- 任意 Shell、任意脚本和任意 sudo；
- Windows/macOS 适配。
