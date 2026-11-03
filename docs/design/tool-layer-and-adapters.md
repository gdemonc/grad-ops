# Tool 层安全与 Adapter 扩展设计

> 状态：规划文档 / 首期接口边界
> 目标：先安全支持 Linux 本地工具，同时为未来 MCP、Kubernetes 等外部能力保留统一接入方式。

## 1. 设计原则

Tool 层是系统与外部执行能力之间的受控边界。LLM 只能请求调用已登记工具，不能提供任意可执行文件、Shell 字符串或未经登记的目标资源。

所有工具，无论来自本地 Linux 实现还是未来 MCP Adapter，都必须经过统一注册、参数校验、风险判定、授权、执行、验证和审计链。工具来源不构成信任依据。

## 2. 逻辑分层

```text
Investigation / Task Planner
          |
          v
     Tool Registry
          |
     Tool Interface
       /       \
      v         v
 Linux Tools   External Adapters (future)
                 |
              MCP Client
                 |
           MCP Server / APIs
```

Tool Interface 负责统一描述与调用契约；Adapter 负责将统一契约映射到具体后端。Policy、Approval 和 Executor 不应依赖某个具体 MCP Server 或 Kubernetes SDK。

## 3. 工具定义

每个工具登记至少描述：
- 稳定的工具 ID、名称、版本和用途；
- 输入参数 Schema、必填项、枚举与边界；
- 只读或副作用属性；
- 风险基线、允许的目标资源与运行身份要求；
- 超时、输出大小、调用预算和并发限制；
- 执行后置条件、验证方式、幂等属性和恢复能力；
- 实现来源与适配器标识。

模型可见的工具描述应是登记信息的受控投影，不得暴露内部执行细节或绕过参数限制。

## 4. 调用与安全流程

1. Planner 请求工具调用。
2. Registry 确认工具已登记且当前阶段允许调用。
3. 对参数执行严格 Schema 校验、规范化和资源范围检查。
4. Investigation 阶段仅开放只读工具。
5. 副作用调用必须生成不可变 Action Contract，并经过确定性 Policy 与必要的人工 Approval。
6. Executor 在调用前重新校验工具版本、参数摘要、目标资源、身份、策略版本和审批凭证。
7. 执行结果进入 Execution Record；后续由独立 Verifier 检查后置条件。
8. 记录工具调用、输入摘要、输出摘要、结果状态和审计关联。

任何未知工具、未知参数、解析失败、后端身份异常或策略不可用均 fail closed。

## 5. Tool Interface 的抽象边界

接口应表达工具元数据、输入输出结构、调用上下文和执行结果，不应把某个后端的协议细节泄漏到领域层。接口设计以首期 Linux 本地工具的真实需求为基线，避免为了未来 Adapter 提前引入复杂插件框架。

首期只实现 Linux 本地工具注册表和执行器。可以在设计上区分 Tool Definition、Tool Registry、Tool Adapter，但不要求首期实现动态插件加载、远程工具发现或热更新。

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

- 未登记工具无法被模型调用。
- 参数缺失、额外字段、类型错误和越界资源均被拒绝。
- 调查阶段无法调用副作用工具。
- 工具超时和输出上限生效。
- 副作用工具无法绕过 Executor、Policy 和 Approval。
- 工具执行结果可关联任务、Evidence、Execution、Verification 与 Audit。
- 更换或新增工具实现，不需要让 LLM 直接依赖具体命令或后端 SDK。

## 8. 明确不做

首期不实现 MCP Client、Kubernetes 操作、动态工具市场、任意 Shell、任意脚本执行、远程插件安装和运行时自动信任新工具。
