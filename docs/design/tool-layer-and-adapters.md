# Tool 层安全与 Adapter 扩展设计

> 状态：规划文档 / 首期接口边界  
> 目标：提供真实、通用的 Linux Shell 能力，同时确保模型不能直接获得宿主机权限。

## 1. 核心结论

Grad-Ops 不应把“查看磁盘”“检查服务”“查找大文件”等运维意图逐一封装成固定业务工具。主流 Agent 的常见模式是提供通用 Shell Tool，再由权限控制、审批和执行环境约束其能力。Grad-Ops 也应采用这一方向，但由于它操作的是真实 Linux 主机，必须额外明确主机权限边界。

**Shell 是通用执行接口；Policy 是授权边界；Linux Runtime 是最终执行边界。AST 是辅助分析工具，不是安全边界。**

## 2. 逻辑架构

```text
LLM Agent
    |
    v
Shell Tool (Persistent Bash Session)
    |  命令 / 脚本 / 管道 / 重定向 / 多轮会话
    v
Policy Engine + Approval
    |  身份 / 目标资源 / 副作用 / 风险 / 授权范围
    |------------------- AST Analyzer (辅助分析)
    v
Execution Broker
    |  会话身份 / Action Contract / 资源预算
    v
Linux Runtime
    |  systemd transient unit / cgroup / 文件与网络边界
    v
Linux Host
    |
    v
Result + Evidence + Audit + Independent Verification
```

Agent 每轮根据执行结果和 Evidence 决定下一步。默认采用短步骤、可观测的交互，不要求一次性生成大型脚本；对于确实需要脚本的任务，仍允许提交多行 Shell 脚本并按同一边界执行。

## 3. Shell Tool：提供真实 Shell 表达力

首期提供通用 Bash Tool，建议使用持久 Session，以保留工作目录、会话变量和必要的会话状态。它至少负责：

- 固定 Bash 可执行文件、版本和启动参数；
- 管理 Session ID、工作目录、受控环境变量和生命周期；
- 支持普通命令、脚本、多行输入、管道、重定向、条件连接、命令替换和子 Shell；
- 设置单次超时、会话空闲超时、输出字节上限、调用次数和累计资源预算；
- 捕获 stdout、stderr、退出码、信号和超时状态；
- 超时或会话取消时终止整个进程组/cgroup，避免遗留后台进程。

Shell Tool 不负责判断用户是否有权修改某个服务或文件，也不能持有任意 root/sudo 权限。会话不能通过继承主服务环境而获得数据库凭据、LLM API Key 或其他服务秘密。

## 4. Policy：围绕权限与影响范围做判断

Policy 不维护“所有合法运维命令”的穷举清单，也不试图通过静态分析证明任意 Bash 程序安全。它根据可验证的上下文决定允许哪些操作：

- **主体**：用户、Agent 任务、执行身份和授权来源；
- **目标资源**：允许观察或修改的文件范围、服务、进程、设备和系统资源；
- **操作类型**：只读观察、文件写入、服务管理、包管理、用户/权限变更等；
- **副作用与影响**：可逆性、影响范围、可用性风险和数据丢失风险；
- **任务能力**：本任务明确授予的资源范围、时间/调用预算和网络访问范围；
- **环境约束**：当前主机配置、运行身份、Runtime 能力和策略版本。

R0-R4 风险等级继续保留为决策结果：

| 等级 | 含义 | 默认处理 |
|---|---|---|
| R0 | 受限的只读观察 | 可自动执行，仍受资源与数据访问边界约束 |
| R1 | 低风险、可逆变更 | 默认审批；明确部署策略可对窄范围动作免审批 |
| R2 | 影响单个服务或文件的变更 | 人工审批 |
| R3 | 高影响变更 | 默认拒绝或要求更强身份与二次确认 |
| R4 | 禁止动作 | 不允许通过普通审批放行 |

风险不能由 LLM 自报。未知资源、权限状态不明、策略缺失或无法确定影响范围时，不得自动扩权。策略规则关注的是授权范围和操作效果，而不是为每种业务需求设计一个独立工具。

## 5. AST Analyzer：辅助理解，不承担安全承诺

可采用 `mvdan.cc/sh` 等 Go Shell Parser 对命令进行 AST 分析。它适合用于：

- 展示管道阶段、重定向、条件控制、子 Shell 和命令替换；
- 提示潜在写入目标、危险命令和可能的间接执行入口；
- 识别 `eval`、`bash -c`、`source`、`xargs`、`find -exec`、`awk system()` 等需要特别关注的语法；
- 为审批界面、审计记录和安全测试提供结构化信息。

但 AST 分析无法完整证明任意 Shell 安全。变量展开、环境变量、命令替换、外部脚本、运行时文件内容和动态命令名都可能使静态分析不完整。即使 AST 解析成功，也不能据此推断命令安全；即使分析器漏掉某个动态路径，Runtime 仍必须限制它能影响的资源。

建议将 AST 结果分成“已识别结构”“潜在副作用”“无法静态确定的部分”三类展示。对无法确定的部分，策略可以要求人工审批、进一步缩小 Runtime 权限或拒绝执行；不能把 AST 白名单作为唯一安全闸门。

## 6. Approval 与 Action Contract

对需要审批或有明显副作用的操作生成 Action Contract。契约应固定：

- 原始 Shell 提案摘要与任务/会话 ID；
- 目标资源和操作范围；
- 用户/Agent 主体、实际运行身份；
- 工作目录、环境白名单和 Shell/Runtime 版本；
- Runtime 隔离配置、文件/网络边界和资源预算；
- 风险等级、策略版本、审批级别和有效期；
- 预期后置条件及独立验证方法；
- AST 摘要（如果启用），作为审计元数据而非安全证明。

审批页面展示可读脚本、目标资源、可能的写入/删除/服务影响、执行身份和隔离边界。契约中的脚本、目标、身份、预算或 Runtime 配置发生变化时，必须重新评估并在需要时重新审批。

对高权限操作，不应通过让 Shell 调用任意 sudo 来实现。需要 root 能力时，使用独立 privileged broker/helper，只暴露少量参数化操作；helper 自己校验主体、资源、参数和一次性授权，并记录审计事件。

## 7. Linux Runtime：最终的 OS 级边界

首期优先采用 Linux 原生机制，避免一开始就构建复杂容器平台：

1. **身份隔离**：API/Agent 服务与 Shell 执行身份分离；默认使用专用非 root 用户。
2. **资源隔离**：每个任务或操作 Session 使用独立 systemd transient service/scope 或等效 cgroup，限制 CPU、内存、Tasks/PID、执行时间和文件大小。
3. **进程治理**：命令超时、用户取消或预算耗尽时终止整个进程组/cgroup；回收后台任务，记录信号和退出状态。
4. **文件边界**：通过专用工作目录、只读挂载、systemd 文件系统保护等限制可见路径；涉及受保护文件的写操作必须由受控入口处理。
5. **网络边界**：默认限制网络访问；确有需要时按任务和目标开放，避免 Shell 任意访问内网或外部服务。
6. **环境清理**：固定 PATH、HOME、umask、locale 等必要变量，不继承主服务密钥和凭据。
7. **增强机制**：Landlock、Bubblewrap、seccomp、AppArmor 可按兼容性和实际威胁逐步引入，不要求首期全部叠加。

每个 Agent Task/Session 使用一个 Runtime 边界，同一 Bash Session 中的管道阶段共享该边界；不为每个命令或管道阶段单独创建沙箱。调查时可开放必要的只读主机视图；变更时必须切换到明确授权的受限执行上下文。隔离能力不可用时，应拒绝对应操作或退回只读，不能静默使用无限制宿主 Shell。

## 8. Host Investigation 与 Host Operation

将主机访问分成两类流程，避免把调查权限和变更权限混在一起：

### Host Investigation

- 使用非 root 身份和通用 Shell 进行只读取证；
- 仅开放明确需要的系统信息、日志和状态视图；
- 默认限制网络、进程数量、执行时间和输出量；
- Evidence 记录来源、时间、命令、退出状态和截断信息；
- 不因调查需要而自动获得文件写入、服务管理或提权能力。

### Host Operation

- 先形成目标明确的 Action Contract；
- Policy 判断主体、资源、副作用、影响范围和风险；
- 必要时由人工审批签发一次性授权；
- Executor 将动作交给隔离 Runtime 或 privileged broker；
- 执行后重新采集 Evidence，独立验证后置条件；
- 失败或状态不确定时记录 UNKNOWN，禁止盲目重放。

## 9. Tool Interface 与后续 Adapter

首期只实现 Linux Shell Tool 和本机 Runtime。接口围绕会话、调用上下文、授权结果、Runtime 配置和执行结果定义，不应把“工具名”当作唯一权限单位。

后续 MCP/Kubernetes Adapter 必须复用统一的主体身份、目标资源、Policy、Approval、Executor、Evidence、Verification 和 Audit 流程。MCP 是工具接入协议，不是授权系统或沙箱；接入 MCP 不意味着可以绕过主机权限边界。

## 10. 首期验收

- Agent 可以通过通用 Bash Session 组合常见 Linux 命令、管道、重定向、条件连接和脚本完成调查。
- Shell Session 不继承主服务秘密，不持有任意 sudo/root 权限。
- Policy 对主体、目标资源、副作用和风险作确定性判断；模型自报风险不能改变决策。
- AST 可提供结构化展示与风险提示，但不作为唯一安全边界。（当前已实现写操作/间接执行/动态命令名的辅助分析，并用于探针、`observe` 动作与验证命令的只读性校验；尚未实现完整的命令结构展示。）
- systemd/cgroup 等 Runtime 限制时间、CPU、内存、PID、输出和进程回收；实际生效配置有集成测试。（当前实现 prlimit 的 CPU/地址空间/进程数/文件大小/fd 限制与可选 uid 切换，并提供 hardened systemd unit 的服务级边界；per-action systemd/cgroup 尚未实现，unit 也未在真实 systemd 主机验证）
- 文件和网络访问按调查/操作场景施加边界；越界操作被阻断。
- 高权限操作通过独立、窄接口的 privileged broker/helper，不通过任意 sudo。
- Action Contract 绑定脚本、目标、身份、预算和 Runtime 配置；关键字段变化触发重新评估。
- 执行后使用新的 Evidence 独立验证；UNKNOWN 状态不自动重试。
- 所有提案、授权、审批、Runtime 配置、执行结果和验证结果可审计。

## 11. 首期不做

- 多主机远程 Agent、SSH fleet 和 Agent 自动升级；
- MCP Client、Kubernetes 操作和外部工具市场；
- 完整容器编排平台；
- 任意宿主机提权、任意 sudo、无边界文件写入和无限制网络访问；
- 把 AST 静态分析当成可以证明任意 Shell 程序安全的机制。
