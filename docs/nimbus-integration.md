# 投顾机器人整合方案（以 nimbus 为中枢）

> 日期：2026-06-13 ·  **更正**：早前 `consolidation-plan.md` 误把 `news` 当中枢、提议 go.mod 内嵌 guanfu——错误，已作废。
> **真正的投顾机器人是 `nimbus`**（Claude Agent SDK 常驻 agent，Discord/Cici）。整合早已通过 **skill 机制**完成，不需要把代码塞进 news。

---

## 1. nimbus 现状（已验证 / 来自其 ROADMAP 与代码）

**本质**：基于 Claude Agent SDK 的常驻多渠道投顾 agent，复用 CC 订阅鉴权 / MCP / skill / 记忆 / 缓存。USAGE 北极星：「**投资逻辑全走现成 skill；AI 绝不下单**」。

- **对话**：Discord(Cici#8105)+TG 独占；四档路由 **L0 直连(futu 行情秒回) / Haiku / Sonnet / Opus**；adaptive 思考深度；模型跟随最新别名。
- **37 个投资 skill 已 vendored**：`btc-guanfu`(=guanfu 引擎) · `ah-stock-screener`(=选股引擎) · research · market-pulse · portfolio-manager · valuation · us-stock-analysis · value-perspective · macro-perspective · trade-execution · trade-journal · thesis-tracker · options-strategy-advisor · sector-analyst · event-calendar · institutional-flow-tracker · news-dashboard · 5×futu-anomaly · futuapi · hyperliquid/polymarket(crypto) …
- **投顾模块(cron)**：`opportunity`(每日主动找赚钱机会) · `reports`(日报) · `alerts`(止损/集中度/论点 decay) · `portfolio-refresh`(futu+IBKR 真仓) · `reflection`(周反思自进化) · `paper` · `quote` · `guardrail`。
- **真实持仓感知**：futu + IBKR（订阅托管连接器，实测可读），**下单工具双闸 deny**。
- **安全/可问责**：AI 不下单（trade-guard hook + canUseTool deny）；决策台账；免责声明；记忆（`记住`）；回撤告警。

> 结论：**投顾的"大脑+对话+主动盯盘+安全"已经成型**。guanfu/ah-screener 不需要再"整合进"任何东西——它们已是 skill。

## 2. nimbus 缺口（来自其 ROADMAP，按优先级）

| 缺口 | 说明 | 优先级 |
|---|---|---|
| **省额度** | 大固定上下文 × 多步工具 × Opus → 上下文瘦身 + 模型分层 | Phase 1 |
| **记忆/自进化** | ★最大短板：Agent SDK 不加载 CC 的 11 条 feedback 记忆 → bot「没读到你的教训/偏好」；需自建 SQLite 三层记忆 + reflection 回灌 | Phase 2 核心 |
| **持仓刷新管线** | portfolio_state.json 现由**外部 CC cron** 生成，nimbus 只读不写 → 需自有 refresh 管线 | Phase 3 |
| **吸收外部 launchd cron** | 4 个 `com.cici.*` 外部 cron（止损/日报/行为监控/日志）→ 并进 nimbus scheduler，停外部防双跑 | Phase 3 |
| **用户知识迁移** | 11 条 feedback + MEMORY.md 迁进项目记忆层 | Phase 2/3 合流 |
| **skill 合并** | 5 个 `futu-*-anomaly` → 合一个多维异动 skill | 可选 |
| **文件独立** | 精简项目专属 CLAUDE.md + hooks 搬进项目，去 settingSources 'user' | Phase 3 |

> nimbus ROADMAP **未提 news** —— news 不在它的整合视野里。它的「news」靠 `news-dashboard` skill（on-demand）+ research + websearch。

## 3. 五项目角色定位（更正后）

```
        nimbus (TS·Agent SDK) = 投顾机器人/对话/主动盯盘 = 中枢
          ├─ skill: btc-guanfu(=guanfu) · ah-stock-screener(=选股) · research · valuation …
          └─ module(cron): opportunity · reports · alerts · reflection · portfolio-refresh
                                  │（唯一值得做的桥）
        news (Go) = 实时新闻 FEED 管道 ──────┘
          firehose: bwe-ws / trump.fm / finnhub / kitco + 13F/A股扫描 + DeepSeek 翻译 + 多渠道推送
        guanfu / ah-screener = 引擎，已被 nimbus 以 skill 复用（不进 news）
        btcdca-miniprogram = 微信前端    btcdca.me = 第三方数据(外部)
```

**一句话**：nimbus 是「该不该买/盯盘/复盘」的**投顾**；news 是「现在发生了什么」的**实时新闻 feed**。两者互补、不是一个东西。

## 4. 真正的重复（要去重的，是 news↔nimbus，不是 guanfu↔news）

| 重复点 | 现状 | 去重决策 |
|---|---|---|
| **大师/投顾逻辑** | news 有 Go `investor` 模块(5 师) | ❌ **冻结/废弃 news investor** —— 与 nimbus 的 value/macro-perspective + research 完全重复。投顾归 nimbus |
| **定时报告推 Discord** | news 推 A股扫描/market-summary/guanfu-score；nimbus reports/opportunity | 分流：**客观结构化报告(A股扫描/13F/市场速览)= news**；**主观投顾(机会/日报/复盘)= nimbus**。发不同频道 |
| **BTC 读盘** | news guanfu-score 源 + nimbus btc-guanfu skill | 各保留：news 推快照、nimbus 对话深答（同源 guanfu，可接受） |
| **futu-anomaly×5** | nimbus 内 5 个 | nimbus 内部合并（其 ROADMAP 已列） |

> news 和 nimbus 是**不同 Discord bot**（我是小川普#3177 vs Cici#8105），无 409 单消费者冲突；只需频道分流避免互刷。

## 5. news 的独有价值 + 唯一值得做的桥

**news 不该废，它有 nimbus 没有的：**
1. **实时新闻 firehose**：bwe-ws(websocket) / trump.fm(60s) / finnhub —— nimbus 没有持续 feed（只有 on-demand + 日 cron）。
2. **多渠道推送**：企业微信（nimbus 仅 Discord/TG）。
3. **结构化独家数据**：13F 持仓变动、A股扫描候选、（拟加）国会交易。

**唯一值得做的整合 = 数据桥（news → nimbus），而非合并代码：**
- 把 news 的**结构化独家产出**（13F 变动 / A股扫描候选 / 国会交易 / 重大突发）落成 **JSON 或一个小 MCP/skill**，让 nimbus 的 `opportunity` 引擎能读 → 投顾基于 news 的 feed 主动找机会、回答「这条新闻对我持仓什么影响」。
- 这样：news 当 nimbus 的**实时事件/数据源**，nimbus 当**会推理的投顾**，各自最强、零重写。

## 6. 整合执行顺序（更正后）

| 阶段 | 内容 | 归属 |
|---|---|---|
| **N1** | 冻结 news 的 Go `investor` 模块（投顾归 nimbus） | news |
| **N2** | news↔nimbus **频道分流**：news→feed 频道，nimbus→投顾/DM | 两边配置 |
| **N3** | news 结构化数据（13F/扫描/国会）落 **JSON/MCP** → nimbus opportunity 可读（数据桥） | news 产出 + nimbus 读 |
| **(nimbus 自身)** | 按其 ROADMAP：省额度 → 记忆/自进化(核心) → 独立化(持仓刷新+吸收外部cron+用户知识) → skill 合并 | nimbus |

> 优先 **N1+N2**（消除重复、明确分工，零风险）；N3 是有价值的桥（中等工作量）。nimbus 自身的省额度/记忆/独立化是它本来就要做的，与本整合并行。

## 7. 边界
- guanfu / ah-screener **保持独立仓库**（还要给 skill/MCP/小程序用），nimbus 以 skill 复用，**不内嵌、不重写**。
- btcdca.me 第三方，仅外部数据兜底。
- AI 不下单红线（nimbus 双闸）整合后不变。
