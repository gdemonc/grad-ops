# grad-ops

证据约束型自主运维控制平面 · 毕业设计

自然语言请求 → 只读取证 → LLM 调查 → 动作契约 → 确定性策略 → 人工审批 → 受限执行 → 独立验证 → 哈希审计，完整闭环可离线演示、可答辩取证。

## 核心特性

- **证据约束**：LLM 的每条结论必须绑定真实 `evidence_id`，引用不存在证据或零引用的结论被丢弃并写审计（`app/grounding.py`）。
- **策略外部化**：R0–R4 风险决策表、工具白名单、路径边界、危险模式全部由确定性控制器裁决，`app/policy/` 禁止 import 任何模型代码，模型无权自授。
- **受限执行**：执行前固定六道闸门——契约摘要 → 一次性审批令牌 → 运行身份 → 副作用工具白名单 → 禁止模式 → 路径边界（`app/executor.py`）。
- **审批门**：R1/R2/R3 提案挂起 `AWAITING_APPROVAL`，人工批准后签发与契约绑定、仅存哈希的一次性令牌，消费即置 `CONSUMED`，重放必拒。
- **独立验证**：验证阶段以全新只读回读确认 `postconditions`，复用调查证据直接抛 `VerificationIndependenceError`，失败自动回滚并置 `ROLLED_BACK`（`app/verification.py`）。
- **哈希审计**：每个 `trace_id` 一条 SHA-256 `prev_hash` 链，可整链校验、可精确定位被篡改的 `seq`、可按阶段重放（`app/audit.py`）。

## 快速开始（Windows 开发）

```powershell
# 1) 后端依赖（与 pyproject.toml 的 dependencies 一致）
cd grad-ops
python -m venv .venv
.\.venv\Scripts\activate
pip install fastapi "uvicorn[standard]" sqlalchemy httpx pydantic pydantic-settings python-multipart pytest pytest-cov ruff

# 2) 配置（保持 GRADOPS_LLM_MODE=mock 即可完全离线演示，不发任何网络请求）
copy .env.example .env

# 3) 终端 A：启动 API（8000 端口）
powershell -ExecutionPolicy Bypass -File scripts\run_dev.ps1
# 或：python -m uvicorn app.main:build_app --factory --port 8000

# 4) 终端 B：启动 Worker 主循环
python scripts\worker.py

# 5) 终端 C：启动前端（Vite 代理 /api 到 127.0.0.1:8000）
npm --prefix frontend install
npm --prefix frontend run dev
# 打开 http://127.0.0.1:5173
```

不启动独立 Worker 时，也可在「任务列表」页或通过 `POST /api/tasks/run` 手动驱动一次队列（演示用）。

关于 mock 模式的如实说明：`GRADOPS_LLM_MODE=mock` 下 `LLMClient` 不发起任何网络请求；未注入剧本时返回回显响应，调查会以「模型返回了无效响应」收敛并封存任务，足以离线演示入队、扫描、状态机、审计链与恶意请求秒拒。完整闭环（取证 → 审批 → 执行 → 验证 → 回滚）由测试套件通过 `LLMClient.set_script` 注入剧本驱动，同样完全离线，详见 `docs/test-report.md`。

### Linux 目标环境说明

- 开发机为 Windows，工具层经 `app/platform.py` 适配；在 Linux 上只读工具使用**真实语义**：`ss -Hlnpt`、`ps`、`df -hP`、`journalctl -u <svc> -n 50`、`systemctl is-active <svc>`，Windows 下分别映射为 `Get-NetTCPConnection`、`Get-Process`、`Get-PSDrive`、日志文件尾读取、`Get-Service`。
- **建议以受限用户运行**（不要用 root）：创建专用用户（如 `gradops-agent`）并保持 `GRADOPS_ALLOW_ROOT_EXECUTOR=false`。执行器在 `uid == 0` 时会拒绝副作用工具，`GET /api/safety/report` 也会把该状态标记为 `blocked`。
- 部署形态即「uvicorn + worker 脚本 + SQLite 文件」，无需 Docker / PostgreSQL。

```bash
python3 -m venv .venv && . .venv/bin/activate
pip install fastapi "uvicorn[standard]" sqlalchemy httpx pydantic pydantic-settings python-multipart pytest pytest-cov
cp .env.example .env
uvicorn app.main:build_app --factory --port 8000   # 终端 A
python scripts/worker.py                            # 终端 B
```

## 真实模型接入

默认 `GRADOPS_LLM_MODE=mock` 离线运行；切换为 `live` 即接入任意 OpenAI 兼容接口（以阿里云百炼为例）：

```env
GRADOPS_LLM_MODE=live
GRADOPS_LLM_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1
GRADOPS_LLM_API_KEY=sk-xxxxxxxx
GRADOPS_LLM_MODEL=qwen-plus-latest
```

客户端走 `/chat/completions`，`response_format` 为 `json_object`、`temperature=0`，超时或非 JSON 响应会重试 2 次后抛 `LLMUnavailable` / `LLMFormatError`（`app/llm/client.py`）。

## API 一览

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/tasks` | 提交自然语言请求 |
| GET | `/api/tasks` | 任务列表（支持 `?status=` 过滤） |
| GET | `/api/tasks/{id}` | 任务详情：证据、提案、审批、执行、验证 |
| GET | `/api/tasks/{id}/timeline` | 按审计 `seq` 的事件时间线 |
| POST | `/api/tasks/run` | 手动驱动一次队列（演示用） |
| GET | `/api/approvals` | 审批队列（`?status=PENDING`） |
| POST | `/api/approvals/{id}/approve` | 批准并恢复执行（body：`comment`） |
| POST | `/api/approvals/{id}/reject` | 拒绝审批 |
| GET | `/api/audit/{trace_id}/verify` | 哈希链校验（`?full=true` 返回逐条结果） |
| GET | `/api/safety/report` | 运行时安全报告（身份、白名单、边界、审批闸门） |
| GET | `/healthz` | 健康检查 |

## 测试

```powershell
python -m pytest                 # 当前 137 passed, 1 skipped（pyproject addopts 已含 -q，勿再叠加 -q，否则 -qq 会隐藏汇总行）
python -m pytest --cov=app --cov-report=term   # 覆盖率：总体 89%，核心模块 policy/executor/verification/audit 均 ≥85%
npm --prefix frontend run build  # 前端类型检查 + 构建
```

详细统计、用例分布与覆盖率对照见 [`docs/test-report.md`](docs/test-report.md)。

## 目录结构

```
grad-ops/
├── app/
│   ├── api/                 # FastAPI 路由：任务 / 审批 / 审计与安全报告
│   │   ├── approvals.py
│   │   ├── audit.py
│   │   └── tasks.py
│   ├── llm/                 # LLM 客户端（mock / live 双模式）
│   │   └── client.py
│   ├── policy/              # 策略层：边界 / 规则表 / 控制器（禁止依赖模型）
│   │   ├── boundary.py
│   │   ├── controller.py
│   │   └── rules.py
│   ├── tools/               # 7 项工具：5 只读观测 + 2 副作用
│   │   ├── base.py
│   │   ├── observation.py
│   │   ├── registry.py
│   │   └── side_effect.py
│   ├── audit.py             # SHA-256 哈希审计链
│   ├── config.py            # pydantic-settings 配置（.env + GRADOPS_ 前缀）
│   ├── contract.py          # 动作契约与摘要绑定
│   ├── db.py                # SQLAlchemy 引擎 / 会话
│   ├── enums.py             # 任务 / 风险 / 提案 / 作业枚举
│   ├── executor.py          # 受限执行器（六道闸门 + 一次性令牌）
│   ├── grounding.py         # 结论-证据绑定校验
│   ├── investigation.py     # 有界迭代 LLM 调查引擎
│   ├── main.py              # FastAPI 应用工厂
│   ├── models.py            # 8 张表
│   ├── platform.py          # Windows / Linux 平台适配
│   ├── queue.py             # SQLite 租约队列
│   ├── verification.py      # 独立验证与回滚
│   └── worker.py            # 任务状态机主循环
├── docs/
│   ├── architecture.md      # 架构、模块表、数据模型、状态机
│   ├── sequence-*.md        # 5 张时序图（只读 / 审批 / 拒绝 / 篡改 / 回滚）
│   ├── threat-model.md      # 威胁模型表
│   ├── test-report.md       # 测试报告与覆盖率
│   ├── plans/               # 实施计划
│   └── specs/               # 规格说明
├── frontend/                # Vue 3 + Vite + Arco Design + Pinia 工作台
│   └── src/
│       ├── components/      # EvidenceCard / ProposalCard / TimelineItem
│       ├── pages/           # 任务列表 / 详情 / 审批队列 / 审计校验 / 安全报告
│       ├── stores/          # Pinia tasks store
│       ├── api.ts           # axios 实例（baseURL /api）
│       └── router.ts
├── scripts/
│   ├── run_dev.ps1          # Windows 开发启动 uvicorn
│   ├── run_dev.sh           # Linux 开发启动 uvicorn
│   └── worker.py            # 独立 Worker 进程入口
├── tests/                   # 138 个用例（对抗、篡改、重放、架构守护等）
├── .env.example
└── pyproject.toml
```

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [`docs/architecture.md`](docs/architecture.md) | 系统概述、总体架构图、模块表、8 张表数据模型、任务状态机、与原 OpsCouncil 的差异 |
| [`docs/sequence-readonly.md`](docs/sequence-readonly.md) | 只读调查路径时序图（R0 封存） |
| [`docs/sequence-approval.md`](docs/sequence-approval.md) | 受控处置路径时序图（审批 → 执行 → 独立验证 → 封存） |
| [`docs/sequence-denied.md`](docs/sequence-denied.md) | 恶意请求与策略拒绝路径时序图（REJECTED，零工具调用） |
| [`docs/sequence-tamper.md`](docs/sequence-tamper.md) | 审计篡改检测时序图 |
| [`docs/sequence-rollback.md`](docs/sequence-rollback.md) | 验证失败回滚时序图 |
| [`docs/threat-model.md`](docs/threat-model.md) | 威胁模型表：资产 / 攻击面 / 对策 / 验证用例 |
| [`docs/test-report.md`](docs/test-report.md) | 测试统计、用例分布、跳过说明、核心模块覆盖率 |
| [`docs/plans/2026-10-14-grad-ops-core-loop.md`](docs/plans/2026-10-14-grad-ops-core-loop.md) | 实施计划（任务 1–19 与验收清单） |
| [`docs/specs/2026-10-14-core-loop-spec.md`](docs/specs/2026-10-14-core-loop-spec.md) | 规格说明：核心闭环、安全性质、风险模型、明确不做 |

## License

本项目以 Apache License 2.0 发布（Apache-2.0）。
