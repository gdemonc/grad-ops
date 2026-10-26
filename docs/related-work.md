# 相关工作（Related Work）

> 用途：论文第 2 章「相关工作」与开题的文献调研底稿。
> 检索日期：2026-10-26。
> 说明：知网（CNKI）直接访问触发安全验证、百度学术返回 403、Semantic Scholar 限流，因此本稿的英文文献来自 **OpenAlex / Crossref / arXiv** 的公开接口；中文文献仅给出可检索线索，**条目需你回学校知网补全作者与刊名**。

---

## 1. 检索方式（可复现）

| 来源 | 检索式 / 接口 |
| --- | --- |
| OpenAlex | `https://api.openalex.org/works?search=large language model AIOps operations` |
| Crossref | `https://api.crossref.org/works?query=LLM+agent+autonomous+operations+security` |
| arXiv | `all:"AIOps" AND all:"large language model"`；`all:"agent" AND all:"human approval"` |
| 知网（待你补） | 大模型 运维 / 智能运维 大模型 / AIOps 大语言模型 / 智能体 安全 权限 / 提示注入 智能体 / 最小权限 智能体 |

---

## 2. 研究现状概览

### 2.1 LLM 与 AIOps

2024–2025 年出现多篇综述，系统梳理了 LLM 在日志解析、异常检测、根因定位、告警聚合等任务中的应用。共性是：**聚焦"看得更准"**（诊断质量），很少讨论"动手之后如何保证不出事"。

### 2.2 LLM Agent 的运行时安全

2025–2026 年这一支快速升温，核心问题从"模型会不会说错话"转向"**模型的意图变成真实动作时如何约束**"。代表工作包括运行时策略拦截（AgentWall）、安全上下文契约（CONTINUITY）、能力控制（Agent libOS）、授权凭证（Provable Authority）。

### 2.3 授权、审批与审计

多项工作把"人工审批"从 UI 交互提升为**可验证的密码学事件**：签名决策、一次性执行凭证、追加式证据日志。这与本项目的"审批令牌一次性消费 + 哈希链审计"方向一致。

### 2.4 中文文献现状

公开可检索到的中文文章几乎都是**行业应用型**（矿山、铁路、电网、高校、客运站），主题是"用大模型提升运维效率"，**未见针对"LLM 自主运维的安全约束与审计"的系统性研究**。这构成本项目的选题空间。

---

## 3. 与本项目最相关的五篇

### 3.1 Bilal 等（2027，Computer Science Review）

提出面向 agentic NetOps/AIOps 的 **operational assurance contract**：把自主等级与"允许的工具、必需证据、独立闸门、执行预算、发布与回滚职责、审计要求"绑定，并指出当前存在"能力—保证落差"。

**与本文关系**：框架层面高度重合，是本项目最需要正面回应的文献。

**差异**：该文是综述与框架，不含可运行系统与端到端实验；本项目提供实现、测试与可复现评估。

### 3.2 Aravind（2026，AgentWall）

本地 AI Agent 的运行时安全层，作为 MCP 代理拦截动作，按声明式策略判定，敏感操作要求人工批准，并记录完整执行轨迹。报告 92.9% 策略执行准确率。

**与本文关系**：最接近的工程实现。

**差异**：AgentWall 面向本地开发者的通用 Agent；本项目面向**运维闭环**，额外具备"结论必须绑定证据""独立验证不得复用调查证据""哈希链审计"。

### 3.3 Yin（2026，The Same Zero）

提出 Verification Autonomy Levels（VAL）：L0=LLM 自述、L1=确定性规则、L2=客观真值……并用受控实验说明"同样是 0 攻击成功率，保证强度可能完全不同"。

**与本文关系**：为"风险等级不能由 LLM 自述"提供直接理论依据。

**用法**：本项目当前风险等级取自模型输出（L0），改造为确定性计算（L1），可引用该文解释必要性。

### 3.4 Zheng 与 Yang（2026，CONTINUITY）

用 assume-guarantee 契约把来源、授权、策略、执行串起来，要求每个外部效果都有有效的授权见证，并用故障注入验证。

**与本文关系**："动作契约（action contract）"的理论参照。

**差异**：CONTINUITY 是通用组合框架与参考验证器；本项目把契约落到运维工具与文件路径边界上。

### 3.5 Wang、Li、Tian（2026，AHI）

对 59 篇论文、21 个生产系统、26 个安全插件做实证：业界真正部署的是**策略配置、运行时审批、范围限制**，而学术界偏爱的"意图锚定、信任标注"几乎没有落地。

**与本文关系**：为"审批 + 策略 + 边界"的工程选择提供实证支持，也可用于讨论"审批疲劳"这一局限。

---

## 4. 与代表性工作的对比

> 下表依据各文**摘要**整理；标"未提及"表示摘要未涉及，不代表全文没有。正式引用前请核对全文。

| 维度 | 本项目 grad-ops | AgentWall (2026) | CONTINUITY (2026) | Provable Authority (2026) | NetOps/AIOps 综述 (2027) |
| --- | --- | --- | --- | --- | --- |
| 形态 | 运维控制平面（API + Worker + 工作台） | 本地 Agent 运行时安全层 / MCP 代理 | 安全上下文契约框架 + 参考验证器 | 授权与证据控制协议 | 综述 + 保证契约 |
| 策略执行点 | 主控确定性策略控制器 | 动作到达主机前拦截 | 跨组件契约边界 | 提交前执行谓词 | 概念框架 |
| 证据绑定 | 结论必须引用 `evidence_id`，否则丢弃 | 未提及 | 来源承诺（provenance） | 来源证据断言 | 要求 evidence traces |
| 人工审批 | 令牌哈希存储 + 一次性消费 | 敏感操作人工批准 | 未提及 | 单次执行授权凭证 | 独立闸门 |
| 执行后验证 | 独立验证，禁止复用调查证据 | 审计回放 | 故障注入验证 | 生命周期证据封存 | 要求 recovery |
| 审计防篡改 | SHA-256 哈希链（`prev_hash`） | 完整执行轨迹 | 签名上下文/收据 | 追加式证据 | 要求 auditability |
| 多主机 | 设计中（pull + Go Agent） | 本地单机 | 跨组件（非跨主机） | 跨主体 | 概念层面 |
| 评估 | 157 个测试 + 对抗用例（规划基线/消融） | 92.9% 策略执行准确率（14 项基准） | 2560 个攻击实例，0 有害外部效果 | 协议规范 | 综述 |

---

## 5. 差异化定位（可直接用于论文创新点）

1. **证据约束是运行时强制的**：结论中的 `claims` 必须引用真实存在的 `evidence_id`，否则被 grounding 模块丢弃——不是"提示词里要求模型引用证据"。
2. **风险定级从 L0 提升到 L1**：不采信模型自报的风险等级，改为由工具与参数确定性计算，修复"谎报 R0 绕过审批"的缺陷，并用修复前后对比量化。
3. **验证独立性**：验证阶段禁止复用调查阶段证据，避免"自己证明自己"。
4. **审批令牌一次性 + 哈希链审计**：审批不可重放，事件不可静默篡改。
5. **双重闸门（规划中）**：主控策略 + 目标机本地策略，主控被攻破时仍受限制。
6. **可复现的小规模评估**：明确报告任务集、指标与局限，不做无依据的"全面优于"表述。

> 诚实提示：本项目的"监控工具"本身不构成创新；创新在于**约束机制的设计与评估**。写作时应把这一句放在显眼位置，避免被误解为"又一个监控系统"。

---

## 6. 参考文献（GB/T 7714-2015 格式）

> 英文期刊条目中卷期页码缺失者标注"（卷期页码待补）"，请以 DOI 页面为准补全；arXiv 预印本按 `[EB/OL]` 著录。

```
[1] BILAL M, CROWCROFT J, WANG R, et al. Large language models for agentic NetOps and AIOps: architectures, evaluation, and safety[J/OL]. Computer Science Review, 2027, 63: 101075[2026-10-26]. https://doi.org/10.1016/j.cosrev.2026.101075.

[2] ZHANG L, JIA T, JIA M, et al. A survey of AIOps in the era of large language models[J/OL]. ACM Computing Surveys, 2025[2026-10-26]. https://doi.org/10.1145/3746635. （卷期页码待补）

[3] ARAVIND A. AgentWall: a runtime safety layer for local AI agents[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2605.16265.

[4] YIN Y J. The same zero: why identical ASR can imply different guarantees in LLM-agent security[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2610.04504.

[5] ZHENG C, YANG G. CONTINUITY: security-context contracts for composable LLM agent controls[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2609.05269.

[6] SIU V, HE J, MONTGOMERY K, et al. A framework for formalizing LLM agent security[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2603.19469.

[7] WANG P, LI Y, TIAN Y. Reframing LLM agent security as an agent-human interaction problem[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2605.24309.

[8] ZHANG K, SU Z, CHEN P Y, et al. LLM agents should employ security principles[EB/OL]. (2025)[2026-10-26]. https://arxiv.org/abs/2505.24019.

[9] SHAHID A, SUTTIE G, BLACK P. Collaborative human-agent protocol (CHAP)[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2606.09751.

[10] BLACKBURN J, HINES T, KOEPP M, et al. Provable authority: a protocol for verifiable authority resolution and control across human, organizational, and agentic transactions[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2610.22899.

[11] ZHANG Y. Agent libOS: a runtime substrate for capability-controlled self-evolving LLM agents[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2606.03895.

[12] ZHANG H, NIAN Y, ZHAO Y. Agent Audit: a security analysis system for LLM agent applications[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2603.22853.

[13] GASMI T, GUESMI R, BELHADJ I, et al. Bridging AI and software security: a comparative vulnerability assessment of LLM agent deployment paradigms[EB/OL]. (2025)[2026-10-26]. https://arxiv.org/abs/2507.06323.

[14] ZHANG L, JIA T, JIA M, et al. A survey of AIOps for failure management in the era of large language models[EB/OL]. (2024)[2026-10-26]. https://arxiv.org/abs/2406.11213.

[15] VITUI A, CHEN T H. Empowering AIOps: leveraging large language models for IT operations management[EB/OL]. (2025)[2026-10-26]. https://arxiv.org/abs/2501.12461.

[16] LING Y, YU S, CHEN Z, et al. Toward secure LLM agents: threat surfaces, attacks, defenses, and evaluation[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2606.10749.

[17] CUI T, MA S, CHEN Z, et al. LogEval: a comprehensive benchmark suite for large language models in log analysis[EB/OL]. (2024)[2026-10-26]. https://arxiv.org/abs/2407.01896.

[18] ZHAO J, WANG Y, ZHANG X, et al. CoE-Ops: collaboration of LLM-based experts for AIOps question-answering[EB/OL]. (2025)[2026-10-26]. https://arxiv.org/abs/2507.22937.

[19] ZHOU L, LIU A, LIU H, et al. Root cause analysis method based on large language models with residual connection structures[EB/OL]. (2026)[2026-10-26]. https://arxiv.org/abs/2602.08804.
```

---

## 7. 中文文献线索（需回知网补全）

> 以下条目来自 OpenAlex 的题录，作者与刊名未完整获取，**不可直接引用**，请到知网/万方按题名检索后补全。

| 题名 | 年份 | DOI |
| --- | --- | --- |
| AI大模型在智慧矿山智能运维中的应用与优化 | 2025 | 10.37155/2717-5316-0602-17 |
| 面向智能运维的数据中心垂类大模型构建与应用研究 | 2026 | — |
| 铁路信号智能运维大模型构建及应用探索 | 2026 | 10.37155/2717-5170-0801-7 |
| 基于DeepSeek大模型的高校智慧教室运维管理智能化转型新思路 | 2025 | 10.69979/3029-2735.25.10.076 |
| 面向运维运营效率提升的数字孪生与大小模型融合技术研究——以"人+AI智能体"协同为核心 | 2025 | 10.37155/2717-5170-0712-43 |
| AI与数字孪生赋能的客运站智能运维研究 | 2026 | 10.32629/ems.v8i3.18750 |

### 建议的知网检索策略

- 主题 = `大模型` AND `运维`；时间 = 2023 至今；来源 = 学位论文 + 核心期刊
- 主题 = `智能体` AND (`权限` OR `安全` OR `审批`)；用于找安全约束方向
- 主题 = `提示注入` OR `间接提示注入`；用于找攻击面
- 主题 = `操作审计` AND `哈希链`；用于找防篡改审计
- 主题 = `最小权限` AND `智能体`

---

## 8. 待办

- [ ] 用学校 VPN 检索知网，补全第 7 节条目的作者、刊名、卷期页码
- [ ] 精读 [1][3][4][5]，核对第 4 节对比表中标"未提及"的项
- [ ] 把第 5 节的 6 条差异化定位压缩成论文的 3 条创新点
- [ ] 为第 6.1 节补充"能力—保证落差"的原文表述与页码
