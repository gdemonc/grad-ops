# Go + Linux 核心闭环规格

> 状态：Draft / 实施前基线  
> 目标：将 grad-ops 重构为 Go 实现、Linux 唯一支持平台的证据约束型运维控制平面。  
> 范围：单机闭环优先；多主机、远程 Agent 暂不属于首期交付。

## 1. 产品目标

系统接收运维人员的自然语言请求，通过只读调查收集证据、生成有证据约束的诊断结论，并在需要变更系统状态时执行确定性策略检查、人工审批、受限操作、独立验证和可追溯审计。

### 1.1 核心原则

1. **LLM 不可信**：模型可提出调查步骤和动作建议，但不能授权动作、降低风险等级或直接执行命令。
2. **默认只读**：调查阶段只允许调用注册表中的只读工具。
3. **证据可追溯**：每条事实性结论都必须绑定本次任务真实存在的 evidence ID。
4. **策略确定性**：工具、参数、目标路径、运行身份和风险登记共同决定是否允许动作。
5. **副作用显式授权**：所有写操作必须匹配不可变动作契约；需要审批的动作必须消费一次性审批凭证。
6. **独立验证**：执行后重新读取系统状态；不能将调查阶段的证据当作执行成功的验证结果。
7. **失败可解释**：执行、验证、恢复和审计分别记录；恢复失败不能被标记为成功。
8. **Linux-only**：唯一支持 Linux。删除 Windows 适配、PowerShell 脚本和跨平台兼容分支。

## 2. 首期范围与非目标

### 2.1 首期范围

- Linux 主机上的单机控制平面。
- Go HTTP API、持久化任务队列、Worker。
- OpenAI 兼容的 LLM 接口，支持 mock 模式。
- Linux 只读观测工具：磁盘使用、进程、监听端口、systemd 服务健康、journal 日志。
- 有限且显式登记的副作用工具；首期可先实现日志备份和日志轮转，但必须有审批、边界和恢复测试。
- 证据绑定、动作契约、确定性策略、审批、独立验证、哈希链审计。
- Vue 3 前端沿用现有项目，不要求同步重写。

### 2.2 非目标

- Windows/macOS 支持。
- 首期多主机扇出、远程 Agent、自动升级。
- Kubernetes Operator、分布式事务、主控高可用。
- 任意 Shell 执行、任意脚本运行、任意文件路径写入。
- 让 LLM 生成后直接执行 shell 命令。
- 宣称哈希链可以抵御拥有数据库和密钥完整控制权的攻击者。

## 3. 关键角色与职责

- **API**：认证、输入校验、任务创建、审批接口和查询接口；不直接执行运维工具。
- **Worker**：认领持久化任务，驱动状态机，设置超时和预算。
- **Investigator**：调用 LLM 规划只读调查；模型输出必须经 schema 校验和工具注册表过滤。
- **Evidence Store**：保存观测原始结果、来源、时间、工具、主机标识和内容摘要。
- **Grounding**：只接受引用真实 evidence ID 的 claims。
- **Policy Engine**：纯确定性模块，不依赖 LLM 包；依据工具登记、参数、目标、身份、策略版本判定风险。
- **Approval Service**：签发与动作契约绑定的短期一次性授权凭证。
- **Executor**：唯一副作用执行入口；重新校验契约、策略、身份、路径和审批，拒绝绕过 API/Worker 直接调用工具。
- **Verifier / Recovery**：执行新的只读检查；失败时按已登记的恢复策略处理并记录结果。
- **Audit Log**：追加式事件记录与哈希链校验。
- **Linux Tool Registry**：工具名、输入 schema、只读/副作用属性、风险基线、超时、输出上限和执行器实现。

## 4. 建议 Go 目录结构

```text
cmd/gradops/
  main.go
internal/
  api/             # HTTP handlers、middleware、request/response DTO
  config/          # env/config validation
  domain/          # enums、entities、state transitions、errors
  task/            # task service and orchestration
  worker/          # durable job claiming, leases, retry policy
  llm/             # OpenAI-compatible client, schema validation
  investigation/   # bounded read-only investigation loop
  evidence/        # evidence persistence and content hashes
  grounding/       # claim-to-evidence binding
  policy/          # deterministic risk and authorization decisions
  contract/        # canonical action contract and digest
  approval/        # one-time approval tokens
  executor/        # only side-effect execution entrypoint
  verifier/        # independent postcondition checks
  recovery/        # explicit compensating actions
  audit/           # append-only event model and chain verification
  store/           # DB interfaces and SQLite implementation
  linux/           # Linux-specific observation and side-effect tools
  httpx/           # outbound HTTP client helpers if needed
web/               # existing Vue app or documented frontend path
migrations/        # versioned SQL migrations
scripts/           # Linux shell scripts only
tests/
  integration/
  security/
```

依赖方向必须单向：domain 不依赖基础设施；policy 不依赖 llm、api 或 executor；executor 依赖 policy/contract/approval interfaces；Linux 工具不直接访问任务状态或审批数据库。避免为了“架构完整”过早引入微服务或大量接口。

## 5. 核心状态机

任务主状态建议：

`QUEUED → INVESTIGATING → (SEALED | AWAITING_APPROVAL)`

`AWAITING_APPROVAL → EXECUTING → VERIFYING → (SEALED | ROLLING_BACK)`

`ROLLING_BACK → (ROLLED_BACK | RECOVERY_FAILED)`

任一阶段也可能进入 `FAILED` 或 `REJECTED`。状态迁移必须通过单一状态机函数校验，并在同一事务中写入状态变化和审计事件。

- `SEALED`：只读调查完成，或副作用执行后独立验证通过。
- `ROLLED_BACK`：恢复动作执行且恢复后的状态检查通过。
- `RECOVERY_FAILED`：恢复未成功或无法证明已恢复。
- `FAILED`：系统错误、预算耗尽或未归类失败；必须保留失败阶段。
- `REJECTED`：策略或安全检查拒绝，不发生被拒绝的副作用。

## 6. 风险和授权规则

风险等级由可信工具注册表与经过 schema 校验的参数计算，不采信模型自报等级。建议等级语义：

- R0：只读观测，不改变系统状态，可自动执行。
- R1：低风险、可逆的受限变更；默认仍需审批，只有显式部署策略允许时才能免审批。
- R2：影响单个服务或文件的变更，必须人工审批。
- R3：高影响变更，默认拒绝或要求额外的受限身份与二次确认。
- R4：禁止动作，不可通过配置为普通审批放行。

风险计算必须考虑工具风险基线、参数、路径、目标资源、运行身份与环境。未知工具、未知参数、未登记路径、解析失败和策略缺失均 fail closed。不得使用“模型声明风险与工具风险取最大值”作为唯一规则。

## 7. 动作契约

动作契约至少绑定：

- proposal/task ID、工具名、经过 schema 校验的参数；
- 规范化目标资源和预期前置条件；
- 风险等级、策略版本、所需审批级别；
- 创建时间、有效期、随机 nonce；
- 契约摘要。

规范化编码必须跨语言确定：UTF-8、对象键排序、无无关空白、数字与时间格式规则固定。不能直接依赖 map 的迭代顺序或未经规范化的 JSON 字节。执行时重新计算摘要并比较实际参数；任何不一致都拒绝。

## 8. Linux 工具安全约束

- 不调用 shell 解释器执行模型生成的字符串；使用固定可执行文件和独立参数数组。
- 每个工具设置超时、最大输出字节数和可控环境变量。
- 不接受任意可执行文件路径；工具二进制与参数 schema 由服务端登记。
- 文件操作必须验证规范化路径并处理符号链接与 TOCTOU；仅做 `EvalSymlinks` 后再按原路径操作不构成完整防护。
- 高风险文件操作优先使用 Linux 专用安全原语（如 dirfd/openat 系列与 `O_NOFOLLOW`，必要时评估 `openat2`）；无法安全保证时拒绝操作。
- 不以 root 运行整个 Web/API 进程。首期建议服务账号最小权限；需要特权时使用独立、窄接口的 helper，禁止给 LLM 任意 sudo。
- 读日志和进程信息也需要输出截断、敏感字段处理和访问控制。
- 不假定 `systemctl`、`journalctl`、`ss` 在所有发行版中输出完全一致；明确首期验证的 Linux 发行版。

## 9. 数据模型基线

至少需要：

- `tasks`：请求、状态、摘要、创建/更新时间、失败阶段。
- `task_jobs`：可恢复队列、租约持有者、租约到期、尝试次数。
- `evidence`：任务 ID、工具、观测时间、结构化内容、content hash。
- `proposals`：动作契约、风险判定、策略版本、审批状态。
- `approvals`：审批人、评论、token hash、过期时间、消费状态。
- `execution_records`：实际执行参数摘要、退出状态、输出摘要、开始/结束时间。
- `verification_records`：独立检查结果、postconditions、验证所用新 evidence。
- `audit_events`：序号、事件类型、关联 ID、payload hash、prev hash、event hash。

使用迁移文件管理 schema，不在每次启动时静默重建表。审批消费和任务认领必须通过事务与条件更新保证原子性。SQLite 首期单实例、单写者设计；如需多进程并发，必须配置 busy timeout、WAL，并测试锁争用与崩溃恢复。

## 10. 首期验收标准

1. Linux 上一条命令启动 API 和 Worker；Windows 支持不在验收范围。
2. R0 任务完成只读调查，claims 只能引用真实 evidence。
3. 未登记工具、无效参数、提示注入工具调用和超预算调查均被拒绝。
4. 副作用动作必须通过契约、确定性策略、路径边界和审批闸门。
5. 过期、篡改、重放、并发双消费的审批凭证不能导致重复执行。
6. 文件路径穿越、符号链接逃逸和路径替换竞态测试通过。
7. 执行后使用新的只读检查；验证失败时恢复结果可查，恢复失败进入 `RECOVERY_FAILED`。
8. 审计链对事件修改、删除、重排能够检出；文档明确其信任边界。
9. Worker 崩溃重启后任务可恢复；同一动作不会因不确定的重试被盲目重复执行。
10. 单元、集成、安全测试均可在受控 Linux CI 中运行，测试报告注明发行版与 Go 版本。

## 11. 多主机作为后续扩展

后续可评估 Agent 主动回连、mTLS、Ed25519 命令签名、TTL/nonce 防重放、主控与 Agent 双重策略、每主机独立执行记录。协议必须先冻结规范化编码与跨语言测试向量。首期不实现这些内容，避免分布式复杂性掩盖单机安全缺陷。
