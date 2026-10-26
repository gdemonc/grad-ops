# 多主机主从平台 + Go Agent 设计与实施计划

> 用途：开题材料 / 毕业设计技术方案。本文档同时是实施计划，可按阶段推进。

**Goal:** 在"不信任 LLM"的前提下，把单机运维控制平面扩展为「单主控 + 多被管主机」，并用 Go 实现被管端的 Node Agent。

**Architecture:** 保留现有 Python 控制平面（任务、策略、审批、审计已成型），新增 Go 单二进制 Agent 以 **mTLS 主动回连（pull 模型）** 方式接入；主控只下发经签名、带时效的命令，Agent 侧再做一次本地策略校验，形成"双重闸门"。

**Tech Stack:** Python 3.11 + FastAPI + SQLAlchemy（主控）；Go 1.22+ + 标准库 `crypto/tls`、`crypto/ed25519`（Agent）；SQLite；Vue 3（前端沿用）。

---

## 0. 先做 Scope 决策（重要）

### 方案对比

| 方案 | 工作量 | 风险 | 论文价值 | 建议 |
| --- | --- | --- | --- | --- |
| **A. Python 主控 + Go Agent（推荐）** | 2–3 周 | 中 | 高：单二进制部署、并发扇出、mTLS、双重策略 | ✅ 采用 |
| B. 全量 Go 重写 | 6–10 周 | 高 | 中：语言迁移本身不算创新 | ❌ 一个月内不做 |
| C. Go 只写远程采集 CLI | 1 周 | 低 | 低：像脚本，不像系统 | 仅作过渡 |

### 为什么推荐 A

- 现有 Python 版本已有 **157 个测试**、哈希审计链、威胁模型、对抗用例，是论文第 6 章实验数据的来源，不能推倒重来。
- Go 真正有优势的地方是**被管端**：单静态二进制、无运行时依赖、goroutine 适合多主机并发、`crypto/ed25519` 标准库现成。
- 主控的价值在策略与审计，用 Python 继续迭代最快。
- 答辩时可以说："**用 Go 解决部署与并发，用 Python 保留策略与可审计性**"——这是有依据的工程取舍，不是"随便选个语言"。

### 如果坚持全量 Go

必须分阶段，且 **Python 版保留为参考实现**（用于基线对比实验）。第 1 个月先完成 Agent，不要动主控；论文写完再迁移。否则极可能"代码重写完、论文没时间写"。

---

## 1. 目标与非目标

### 目标

1. 一个主控管理 N（≥2）台被管主机，只读采集可跨主机扇出。
2. 命令具备：来源认证（mTLS）、完整性（签名）、防重放（nonce + TTL）。
3. Agent 本地二次校验：即使主控被攻破，也只能执行本机白名单内的工具。
4. 每台主机独立记录执行结果与证据，支持部分失败。
5. 审计链覆盖"哪台主机、什么命令、谁批准、结果如何"。

### 非目标（YAGNI）

- 不做主控高可用（单主控即可，SQLite 单文件）。
- 不做 etcd / 分布式事务。
- 不做 K8s Operator。
- 不用 gRPC / WebSocket；HTTP + JSON + 长轮询足够。
- 不做 Agent 自动升级。

---

## 2. 架构

```text
┌────────────────────┐        HTTP/JSON
│  Vue3 工作台        │ ─────────────────┐
└────────────────────┘                  v
                              ┌──────────────────────────┐
                              │ Python 控制平面           │
                              │  FastAPI / Worker / SQLite│
                              │  policy · approval · audit│
                              └───────────┬──────────────┘
                                          │  mTLS（Agent 主动回连）
                     ┌────────────────────┼────────────────────┐
                     v                    v                    v
              ┌────────────┐       ┌────────────┐       ┌────────────┐
              │ Go Agent A │       │ Go Agent B │       │ Go Agent C │
              │ 本地策略    │       │ 本地策略    │       │ 本地策略    │
              └─────┬──────┘       └─────┬──────┘       └─────┬──────┘
                    v                    v                    v
              [host 本机工具]        [host 本机工具]        [host 本机工具]
               ss/ps/df/journal       ...                   ...
```

### 为什么用 pull（Agent 主动连主控）

| 维度 | push（主控 SSH 到被管机） | **pull（Agent 回连，推荐）** |
| --- | --- | --- |
| 被管机要求 | 开放 22 端口 | 只需出站网络 |
| 主控密钥 | 持有所有主机私钥 | 只持有 CA，可单台吊销 |
| NAT / 云环境 | 需跳板或公网 IP | 天然穿透 |
| 单点风险 | 主控被攻破 = 全部沦陷 | 主控被攻破仍受 Agent 本地策略限制 |
| 代码量 | 少 | 多（需 Agent） |

选 pull 的核心理由：**把"信任"从主控下沉一台**。这与整个项目的"LLM 不可信"主线一致。

---

## 3. 关键设计决策

| 编号 | 决策 | 理由 |
| --- | --- | --- |
| D1 | Agent 主动回连，长轮询取命令 | 无需入站端口，NAT 友好，可单台吊销 |
| D2 | mTLS，双向证书 | 传输层认证 + 加密 |
| D3 | 命令用 Ed25519 签名，带 `issued_at`/`expires_at`/`nonce` | 防篡改 + 防重放 |
| D4 | Agent 侧独立策略（工具白名单、路径白名单） | 纵深防御：主控被攻破也不失控 |
| D5 | LLM 只能传 `host_id`，不能传 `user@ip` | 防横向移动 / SSRF |
| D6 | 扇出上限 + 金丝雀 | 控制爆炸半径 |
| D7 | 每台主机独立 `ExecutionRecord`，任务可进入 `PARTIAL_FAILED` | 部分失败可解释、可回滚 |
| D8 | 验证在目标机执行，且不得复用调查证据 | 验证独立性不因分布式而失效 |
| D9 | 风险等级由 `max(LLM 声明, 工具登记)` 决定 | 修复当前"LLM 可谎报 R0 绕过审批"的缺陷 |

---

## 4. 通信协议

### 4.1 端点（主控侧，`app/api/agents.py`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/agent/enroll` | 一次性注册令牌换发客户端证书 |
| POST | `/agent/heartbeat` | 上报存活、版本、负载 |
| GET | `/agent/commands?wait=30` | 长轮询取待执行命令 |
| POST | `/agent/results` | 上报执行结果与证据 |
| GET | `/api/hosts` | 工作台查看主机列表与状态 |

### 4.2 命令信封

```json
{
  "command_id": "0f3c...",
  "host_id": "web-01",
  "tool": "disk_usage",
  "params": {},
  "issued_at": "2026-10-26T10:00:00Z",
  "expires_at": "2026-10-26T10:00:30Z",
  "nonce": "b7e1...",
  "signature": "ed25519:9a2f..."
}
```

签名原文 = 除 `signature` 外所有字段的**规范化 JSON**（键排序、无空格）。Agent 校验：

1. mTLS 客户端证书合法；
2. 主控公钥验签通过；
3. 当前时间在 `[issued_at, expires_at]` 内；
4. `nonce` 未见过（本地 LRU 缓存）；
5. `host_id` 等于自己；
6. 工具与参数通过**本地策略**。

任一条失败 → 拒执行，回传 `rejected` 与原因。

### 4.3 结果信封

```json
{
  "command_id": "0f3c...",
  "host_id": "web-01",
  "ok": true,
  "summary": "3 mounts",
  "data": {"disks": []},
  "finished_at": "2026-10-26T10:00:03Z",
  "agent_signature": "ed25519:..."
}
```

主控入库前：验 Agent 签名 + 计算 `content_hash`，写入审计链。

---

## 5. 威胁模型

| 威胁 | 场景 | 缓解 | 残余风险 |
| --- | --- | --- | --- |
| 伪造 Agent | 攻击者冒充被管机接入 | mTLS 客户端证书 + 一次性注册令牌 + `host_id` 与证书绑定 | 注册令牌泄露（短时效 + 单次） |
| 中间人 | 篡改命令/结果 | mTLS + Ed25519 签名 | 主控私钥泄露 |
| 重放 | 重发旧命令重复执行 | `nonce` + 短 TTL + `command_id` 幂等 | 时钟漂移（需 NTP） |
| 恶意 LLM | 谎报风险、构造越权参数 | 确定性风险定级（D9）+ 参数校验 + 白名单 | 工具设计缺陷 |
| 主控被攻破 | 攻击者拿到主控 | Agent 本地策略二次拦截、路径边界 | 白名单内仍可被滥用 |
| Agent 被攻破 | 攻击者控制被管机 | 主控验 Agent 签名失败、审计断链告警 | 该主机数据已泄露 |
| 横向移动 | LLM 指定任意地址 | 只接受 `host_id`，地址由清单解析 | 清单被篡改 |
| 证据篡改 | 改执行结果 | 结果签名 + 哈希链 + 跨主机交叉校验 | 主控与 Agent 同时沦陷 |
| 扇出爆炸 | 一次操作打挂 N 台 | `max_hosts_per_action`、金丝雀先 1 台、分批 | 运维误操作 |
| 部分失败 | 3 台中 1 台超时 | 每台独立记录 + `PARTIAL_FAILED` + 回滚计划 | 回滚本身可能失败 |

---

## 6. 数据模型变更

在现有 8 张表上新增 3 张，并给 2 张表加列：

```text
hosts(id, host_id, address, environment, criticality, status, created_at)
agent_certs(id, host_id, fingerprint, public_key, issued_at, revoked_at)
command_queue(id, command_id, host_id, tool, params_json, envelope_json,
              status, issued_at, expires_at, attempts)
```

- `evidence` 增加 `host_id`（可空，兼容旧数据）
- `execution_records` 增加 `host_id`

任务状态机新增：

```text
PENDING → FANOUT_RUNNING → (SEALED | PARTIAL_FAILED | ROLLED_BACK | REJECTED)
```

`PARTIAL_FAILED`：至少一台成功、至少一台失败，且回滚未全部完成。必须保留每台的明细，不能只给一个总状态。

---

## 7. 文件结构

### Go Agent（新增）

```text
agent/
  go.mod
  cmd/gradops-agent/main.go
  internal/config/config.go        # 配置文件 / 环境变量
  internal/enroll/enroll.go        # 注册、证书落盘
  internal/mtls/mtls.go            # TLS 配置、证书轮换
  internal/protocol/envelope.go    # 命令/结果结构体、规范化 JSON
  internal/protocol/verify.go      # 验签、TTL、nonce
  internal/policy/policy.go        # 本地工具/路径白名单
  internal/execute/execute.go      # 命令分发、超时、结果封装
  internal/tools/disk.go
  internal/tools/process.go
  internal/tools/ports.go
  internal/tools/service.go
  internal/tools/logs.go
  internal/tools/registry.go
  internal/report/report.go        # 结果签名、上报
```

### Python 主控（修改/新增）

```text
app/api/agents.py            # 新增：注册、心跳、取命令、收结果
app/agent_protocol.py        # 新增：签名/验签、信封构造、nonce 表
app/inventory.py             # 新增：host_id → address / 环境 / 关键级别
app/tools/remote.py          # 新增：把只读工具包装成远程命令
app/models.py                # 修改：+3 表，evidence/execution +host_id
app/worker.py                # 修改：扇出编排、PARTIAL_FAILED
app/verification.py          # 修改：按 host 远程验证/回滚
app/policy/controller.py     # 修改：确定性风险定级（D9）、主机关键级别
app/config.py                # 修改：扇出上限、TTL、CA 路径
tests/test_agent_protocol.py # 新增
tests/test_agents_api.py     # 新增
tests/test_fanout.py         # 新增
agent/internal/**/*_test.go  # Go 单测
```

---

## 8. 分阶段实施计划

> 每个阶段都必须能独立运行、可演示、有测试。不要一次性写完整套。

### Phase 0：协议与威胁模型冻结（1–2 天）

- [ ] 在 `docs/specs/` 下把第 4、5 节落成正式规格（字段、错误码、超时）。
- [ ] 确定规范化 JSON 规则（键排序、UTF-8、无空格），并写跨语言测试向量。
- [ ] 验收：Python 与 Go 对同一份测试向量算出相同签名。

### Phase 1：Go Agent 只读 + 单主机（4–5 天）

- [ ] `agent/` 初始化：`go mod init github.com/<you>/gradops-agent`。
- [ ] 实现 `protocol`：信封结构、规范化 JSON、Ed25519 验签、TTL、nonce。
- [ ] 实现 `tools` 五个只读工具（对应 `ss/ps/df/journalctl/systemctl`）。
- [ ] 实现长轮询 + 结果上报 + 心跳。
- [ ] Python 侧 `app/api/agents.py`：注册、取命令、收结果。
- [ ] 单元测试：篡改命令被拒、过期命令被拒、重放被拒、越界工具被拒。
- [ ] 验收：`go test ./...` 全绿；一台 Ubuntu 上能采集 `disk_usage` 并写入证据表。

### Phase 2：多主机扇出（3–4 天）

- [ ] `inventory` + `hosts` 表；工作台显示主机列表与在线状态。
- [ ] 扇出编排：`max_hosts_per_action`（默认 3）、金丝雀先 1 台。
- [ ] 每台独立 `ExecutionRecord` + `evidence.host_id`。
- [ ] 部分失败状态 `PARTIAL_FAILED` 与错误汇总。
- [ ] 审计事件加 `host_id`，时间线可按主机过滤。
- [ ] 验收：2 台主机同时采集；停掉 1 台 → 任务 `PARTIAL_FAILED` 且另一台数据完整。

### Phase 3：副作用 + 远程验证/回滚（4–5 天）

- [ ] `remote.py` 下发副作用命令（仅备份/轮转），仍走审批。
- [ ] `verification.py` 改为按 `host_id` 在目标机执行 `stat` 校验。
- [ ] 回滚改为远程 `mv`，并记录每台回滚结果。
- [ ] 测试：远程验证通过/失败、回滚成功/失败、审批令牌一次性。
- [ ] 验收：一台主机轮转日志 → 远程验证 → 审计链校验通过。

### Phase 4（可选，论文写完后再做）：Go 控制平面

- 仅当时间充裕时考虑；否则明确写入"未来工作"。

---

## 9. 验收标准（可写进论文第 6 章）

| 编号 | 验收项 | 判定 |
| --- | --- | --- |
| A1 | 命令篡改 | Agent 拒绝，回传 `signature_invalid` |
| A2 | 命令重放 | 第二次执行被拒，回传 `replay_detected` |
| A3 | 过期命令 | 超时后拒绝，回传 `expired` |
| A4 | 越权工具 | Agent 本地策略拒绝，即使主控签名合法 |
| A5 | 越权路径 | 路径边界拒绝（`/etc` 等） |
| A6 | 扇出上限 | 超过 `max_hosts_per_action` 直接拒绝 |
| A7 | 部分失败 | 任务进入 `PARTIAL_FAILED`，逐台明细可查 |
| A8 | 证据完整性 | 每台证据 `content_hash` 与审计链一致 |
| A9 | 审计篡改 | 改 1 字节后 `verify` 检出 |
| A10 | 审批绕过 | LLM 谎报 R0 的副作用动作仍被拦下（修复 D9 后） |

---

## 10. 与论文章节的对应

| 论文部分 | 用本文档的哪一块 |
| --- | --- |
| 第 1 章 绪论 | 主从运维的安全痛点、"LLM 不可信"前提 |
| 第 3 章 需求分析 | 第 1 节目标/非目标、第 5 节威胁模型 |
| 第 4 章 系统设计 | 第 2 节架构、第 3 节决策、第 4 节协议、第 6 节数据模型 |
| 第 5 章 系统实现 | 第 7 节文件结构、第 8 节 Phase 1–3 |
| 第 6 章 测试与评估 | 第 9 节验收 + 基线/消融/注入实验 |
| 第 7 章 总结与展望 | 第 1 节方案对比、Phase 4 未来工作 |

---

## 11. 风险清单

| 风险 | 影响 | 应对 |
| --- | --- | --- |
| Go 重写拖垮进度 | 论文没时间写 | 只写 Agent；主控不动；Phase 4 砍掉 |
| 证书管理复杂 | 环境搭不起来 | 先用自签 CA + 脚本生成，不上 PKI |
| 时钟漂移导致 TTL 误判 | 命令被误拒 | 放宽 TTL 到 60s；文档要求 NTP |
| 分布式调试困难 | 卡在联调 | 每个阶段先单机跑通再加第二台 |
| 扇出误操作 | 多台同时出问题 | 金丝雀 + 上限 + 审批 |
| 范围膨胀 | 什么都做一半 | 严格按 Phase 1→2→3，Phase 4 默认不做 |

---

## 12. 下一步

1. 把 Phase 0 的协议规格写进 `docs/specs/2026-10-26-agent-protocol.md`。
2. 先做 **Phase 1 单主机**，跑通再谈多主机。
3. 论文实验数据仍以现有 Python 版为准；Go Agent 作为"分布式扩展"章节。

> 注意：**语言迁移不是创新点**。论文的创新仍然来自"证据约束 + 确定性策略 + 双重闸门 + 审计追溯"。Go 只负责把这件事做得更适合部署和并发。
