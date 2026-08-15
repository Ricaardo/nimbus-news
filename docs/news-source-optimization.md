# 新闻源优化方案（最终版 v2）

> 版本：v2 ·  日期：2026-06-13 ·  状态：待实施
> 范围：`config.platform.yaml` 的 `sources` 与 `internal/source/*`
> 所有候选/现有 endpoint 均经 `curl`/ws 真实探测（见文末附录）。

---

## 0. v2 相对 v1 的更新
- Trump 最佳源由 trumpstruth.org **升级为 trump.fm**（自带 `/rss.xml` + `/api/posts`，覆盖 X+Truth 双平台）。
- BWE 新发现**官方公共 RSS** `rss-public.bwe-ws.com`（免代理）。
- `bwenews-live` ws「收不到消息」定性：**非 bug，设计低频**。
- 新增两个被点名网站的结论：**Serenity 面板**（无 API）+ **Situational Awareness LP / Leopold 13F**（走 EDGAR）。
- 新增「机构 13F 追踪」为独立能力（非实时新闻）。

---

## 1. 现状盘点（实时源）

| 源 | 类型 | 通道 | 间隔 | 状态 |
|---|---|---|---|---|
| `bwenews-live` | bwenews-ws | WebSocket 直连 | 实时 | ✅ 连接正常（**低频，见 §3.2**） |
| `trump-rss` | rss | trumpstruth.org 直连 | 60s | ✅ 正常（拟升级，见 §3.1） |
| `bwe-tradfi` | rss | **ch2rss.fflow.net** | 60s | ✅ 正常 · **必保** |
| `kobeissi-letter` | rss | **ch2rss.fflow.net** | 120s | ✅ 正常 |
| `mms-news` | rss | **ch2rss.fflow.net** | 60s | ✅ 正常 |
| `kitco-precious` | rss | kitco 直连 | 300s | ✅ 正常 |
| `finnhub-market` | finnhub | finnhub API | 600s | ❌ 已禁用（key 已配） |

渠道：`wechat-main` / `discord-push`(forum) / `discord-webhook`(plain) / `feishu-bot`(双向)。Telegram 已移除。

---

## 2. 核心薄弱点
1. **ch2rss.fflow.net 单点依赖**：`bwe-tradfi`/`kobeissi`/`mms` 三源全挂在同一非官方代理上。
2. **缺一线/官方源**：无 Reuters/AP/Benzinga/WSJ；无 Fed/SEC 直连。
3. **现成资源闲置**：`finnhub-market` 代码+key 齐备却禁用。
4. **BWE 覆盖不全**：只用了 BWE 的低频 ws，没用其官方 RSS。

---

## 3. 逐源结论与建议

### 3.1 Trump → 升级到 trump.fm（首选）✅
trump.fm 不止归档，**有公开接口**：
- `https://trump.fm/rss.xml` → 200, `application/rss+xml`, 标准 RSS 2.0，实时更新。**可直接当 `rss` 源，零代码**。
- `https://trump.fm/api/posts?limit=N` → 干净 JSON（`id/platform("truth"|"x")/platformId/content/...`）。需要结构化可走 `generic_http`。
- **覆盖 X + Truth Social 双平台**（trumpstruth 仅 Truth），且自带 Listen/Analyze/Visualize。

→ **动作**：把 `trump-rss` 的 url 改为 `https://trump.fm/rss.xml`，加来源显示名 `trump.fm`。
→ factba.se 已商业化（免费 feed 301/302 关闭），不可用。付费实时见 §6。

### 3.2 BWEnews —— ws 保留 + 新增官方 RSS
- **`bwenews-live`（ws）收不到消息＝设计如此，非 bug**。官方文档：**ws 仅推 BWEnews 自有原创独家**（低频），**交易所公告等高频内容不走 ws**。实测连接/ping-pong/消息格式均正常。**保留，不改代码。**
- **新增 BWE 官方公共 RSS** ✅：`https://rss-public.bwe-ws.com/`（方程式新闻官方、免费、**直连不经 ch2rss**、标准 RSS）。定位「alpha only 精选」。→ 作为 `rss` 源新增，来源名 `BWEnews`。
- **`bwe-tradfi` 必保维持**：它是 BWE 的**传统金融/宏观**子频道（Telegram BWEtradfi），聚合 **Reuters/WSJ/Bloomberg/The Information** + 宏观地缘，中英双语，条目带 `Tradfin:`/`DB:` 前缀。**无官方 RSS 可替代**（探 `/tradfi`、`/rss/tradfi` 均回落默认 feed），该宏观流只能继续走 ch2rss 代理。

### 3.2.1 更好/更广的免费聚合器 → Tree of Alpha（Tree News）⭐
作为 `bwe-tradfi` 的**升级/补充**（覆盖面更广、可编程）：
- 规模：**1150+ 源 + 2300+ Twitter 账号**，覆盖 **TradFi + crypto + 交易所公告 + 经济指标 + 美政府**。
- **免费 REST** ✅：`https://news.treeofalpha.com/api/news` → 200，100 条实时 JSON（字段 `title/source("Blogs"|"Twitter"|…)/url/time(ms)/symbols/sourceName`）。实测刚抓到当分钟条目（如 BlackRock BTC income ETF、SP Global 评级、Lookonchain 推文）。
- 免费 ws（`wss://news.treeofalpha.com/ws`）**确认需登录鉴权**：连上即返回 `{"success":false,"error":"Invalid login syntax."}` → ws 是付费/账号档；**免费只有 REST**。
- **接入**：用 `generic_http`（或新写一个轻量源）轮询 `/api/news`（60s），映射 `title/url/time/symbols`。→ 列入 **P2**。

#### 同类源最终确认（均不可接，仅 Tree REST 可用）
- **PhoenixNews**：❌ 无可消费的新闻 API。查 docs 全清单（`llms.txt`）全是「连接你自己的交易所 API key 下单」的交易终端功能（`Command` 等限订阅/NFT）。其「API key」是下单用，非吐新闻。
- **aggr / aggrnews**：❌ `aggr.news`、`aggrnews.{com,io,app}` 均无法解析（域名不存在）；仅 `aggr.trade` 存在 = 成交/CVD 盘口工具，**非新闻**。无「aggrnews」新闻源。

### 3.3 第一梯队补强（纯配置）
| 源 | Endpoint / 动作 | 实测 |
|---|---|---|
| 启用 finnhub | `finnhub-market` 改 `enabled: true` | key 已配 |
| Fed 官方 | `https://www.federalreserve.gov/feeds/press_all.xml` | ✅ 200 |
| WSJ Markets | `https://feeds.a.dj.com/rss/RSSMarketsMain.xml` | ✅ 200 |
| BWE 官方 RSS | `https://rss-public.bwe-ws.com/` | ✅ 200 |
| trump.fm | `https://trump.fm/rss.xml` | ✅ 200 |

### 3.4 第二梯队（最佳实时，需开发）
- **Alpaca News API（= Benzinga，WebSocket，免费）**：补「宏观/股市一线突发」的最大空白；账号已接入。新增一个 ws 源（仿 `bwenews_ws.go`）。

### 受阻/不可用
- SEC EDGAR atom → 403（需自定义 UA，要改代码）。
- CoinDesk RSS → 308 改版（需换新地址）。
- Google News RSS → 带 `when:` 会 302（需规范 query）。

---

## 4. 个人 KOL 追踪：Serenity（白毛股神）
- 身份：`@aleabitoreddit`（X，76.9 万粉，AI/半导体供应链「瓶颈理论」）。**只在 X 发布**。
- **`analysissite.vercel.app`**：经查是一个 **Serenity 推文/选股分析面板**（含其招牌票 $AXTI/$SIVE）。但 **Next.js 静态站，无任何 API/RSS/JSON**（/api、/data.json、/rss 全 404），数据烘焙在 HTML 内，且为个人 Vercel 项目，**不可作为生产数据源**（随时可能下线、无稳定契约）。
- **结论**：Serenity **没有可订阅的稳定 feed**。唯一可靠路径仍是其 X：
  1. 自建 RSSHub + 注入 X cookie（免费稳定，需部署）；
  2. X API（官方付费 Basic 档）→ 塞进 `generic_http` 源。
  - 公共 `rsshub.app/twitter` 已失效（302）；analysissite 无 API。

---

## 5. 机构 13F 追踪（新能力 · 非实时新闻）
- 用户点名的 NASDAQ 页 `…/situational-awareness-lp-1324715` = **Situational Awareness LP**（Leopold Aschenbrenner 的基金）的 **13F 机构持仓**。
- **官方免费源 = SEC EDGAR**（已确认）：**CIK `0002045724`**，报 `13F-HR`，最新季度 `2025-09-30`。
- 性质：**季度数据 + ~45 天滞后**，是「机构持仓变动」而非实时新闻 → 不进实时推送，宜做**季度 13F 报告**（新文件出现时拉取并对比上期增减仓）。NASDAQ 自身 API 反爬（探测 404/142B），用 EDGAR。
- 实现选项：
  1. 新增定时源 `script` 类型，调用脚本查 EDGAR（CIK 0002045724）13F，有新文件则推送持仓变动摘要；
  2. 或纳入既有 `institutional-flow-tracker` skill 的工作流（手动/按需）。
- 可扩展：同框架追踪其它关注的基金（多 CIK 列表）。

---

## 6. 付费 / 重型选项（按需）
| 用途 | 方案 | 类型 |
|---|---|---|
| Trump 分秒级 | TweetStream / Apify truth-social-scraper | WebSocket（付费）|
| 一线财经突发 | Benzinga（直签） | WS/Webhook（付费）|
| 机构 13F 美化 | QuiverQuant / WhaleWisdom | 付费 API |

---

## 7. 噪音控制与频控路由（加源的前提）

加源必须同时做降噪+路由，否则刷屏并触发 Discord/微信频控。

### 7.1 现有过滤三层（已具备）
- **per-source `require_keywords`**（rss）：只推含关键词条目。
- **content `block_keywords` + `strict_sources`**：全局黑名单。
- **`ai_filter`**（LLM 0-10 打分，阈值 6.0，`block_categories`，`target_sources`）：主力降噪，已走 deepseek-flash。
- **finnhub 专属**：`symbols`（限自选）、`skip_market`、`max_per_fetch`。

### 7.2 最终决策（本轮拍板）
- **BWE 加密只保留 `BTC` + `HYPE`**：bwenews 源新增 `coin_whitelist: [BTC, HYPE]`——`coins_included` 命中白名单或为空(宏观)才推，山寨/meme 直接滤掉。（需小改代码）
- **ai_filter 暂不屏蔽** 个股/财报/评级（保持现状，阈值 6.0、`block_categories` 不动）。
- **可选** `important_only`（仅推 ⚠ 开头）暂不启用，保留为开关。

### 7.3 频控路由（Discord 是瓶颈）
- `discord-push`(forum bot) 限 **3 thread/15min**，每条建帖 → **高频源不能走它**。
- **高频实时源**（trump.fm / bwe-tradfi / kobeissi / mms / finnhub / WSJ / Fed / kitco / bwenews / Tree）→ sinks = **`[wechat-main, discord-webhook]`**（去掉 discord-push）。
- **低频报告源**（guanfu / market-summary / A股扫描系列 / us-macro / weekly-calendar）→ 保留 **`[wechat-main, discord-push, discord-webhook]`**（forum 归档好看）。
- 企业微信 webhook 实际 ~20 条/min，已有 ratelimit 规则 + 补推队列兜底。

---

## 8. 实施计划

| 阶段 | 内容 | 改动面 | 状态 |
|---|---|---|---|
| **P1（配置+路由）** | ①trump-rss→trump.fm/rss.xml ②启用 finnhub(限自选) ③加 Fed ④加 WSJ ⑤加 BWE 官方 RSS ⑥高频源 sinks 去 discord-push | `config.platform.yaml` + rss.go 加来源显示名 | 实施中 |
| **P1.5（小改代码）** | ⑦bwenews 加 `coin_whitelist:[BTC,HYPE]` + `important_only` 开关 | `bwenews_ws.go` + config | 实施中 |
| **P2** | ⑧Tree of Alpha 聚合源（REST `/api/news` 轮询，补 bwe-tradfi） | 新轻量源 + config | 实施中 |
| **P3** | ⑨机构 13F 追踪（`scripts/edgar_13f.py` 多 CIK，每日查、仅新文件推送、带 pro 解读） | script 源 + 脚本 | ✅ 完成（追踪 11 只名基金，见下）|
| **Serenity** | `@aleabitoreddit` 源脚手架已就位（config `serenity` disabled）；设 `SERENITY_RSS_URL`(自建 RSSHub/X API) + `enabled:true` 即开 | config 就绪 | ⏸ 待用户提供 key/URL |
| **降级** | Alpaca/Benzinga ws（不含 Reuters/BBG，Tree/bwe 已覆盖 wire 标题） | — | 暂缓 |

### 已上线源清单（26 源，2026-06-13）
实时(高频, sinks=wechat+discord-webhook)：bwenews-live(BTC/HYPE过滤) · trump-rss(→trump.fm) · bwe-tradfi · kobeissi · mms · kitco · finnhub(限自选) · bwenews-rss · tree-of-alpha · fed-press · wsj-markets
报告(低频, +discord-push)：guanfu · market-summary · A股扫描×6 · us-macro · weekly-calendar · edgar-13f
disabled：serenity（待 key）· finnhub 已启用
> 注：fed-press / bwenews-rss / tree-of-alpha 偶发 TLS/超时（慢服务器首连），健康监控自动重试，非阻断。

### 13F 追踪基金清单（11 只，CIK 已核实）
AI/科技派：Coatue `0001135730` · Whale Rock `0001387322` · Altimeter `0001541617` · Tiger Global `0001167483`
宏观派：Duquesne(Druckenmiller) `0001536411` · Soros `0001029160`
集中/逆向派：Scion(Burry) `0001649339` · Pershing(Ackman) `0001336528` · Appaloosa(Tepper) `0001656456` · Berkshire(Buffett) `0001067983`
+ Situational Awareness(Leopold) `0002045724`
> 已知小瑕疵：个别 filer（如 Duquesne）13F value 按千美元报，总市值显示偏小 1000×，但**持仓占比正确**（信号在占比）。Reuters/BBG 免费源经确认不加（bwe/Tree 已覆盖头条）。

---

## 附录 A：endpoint 实测记录（2026-06-13）

```
# Trump
trump.fm/rss.xml                         → 200 application/rss+xml 49KB  ✅ 标准RSS, 双平台
trump.fm/api/posts?limit=N               → 200 application/json         ✅ id/platform/content
trumpstruth.org/feed                     → 200 160KB                    （现用, 仅Truth）
factba.se/rss/trump                      → 301  / rollcall …/feed → 302 （免费feed已关）

# BWE
wss://bwenews-api.bwe-ws.com/ws          → 连上, ping→pong OK, 42s 0 新闻（低频原创, 设计如此）
rss-public.bwe-ws.com/                   → 200 application/xml, 10 items（官方公共RSS, 免代理）✅
rss-public.bwe-ws.com/{tradfi,…}         → 回落默认feed（无独立tradfi路径）
ch2rss.fflow.net/BWEtradfi               → 200, 聚合 Reuters/WSJ/BBG/The Information + 宏观地缘（中英双语）

# 聚合器替代/补充
news.treeofalpha.com/api/news            → 200 92KB, 100条实时JSON ✅ Tree of Alpha(1150+源/2300+推特) 免费无key
wss://news.treeofalpha.com/ws            → 需登录鉴权（返回"Invalid login syntax"）→ ws付费, 用REST
docs.phoenixnews.io/llms.txt             → 仅交易终端功能, 无新闻API ❌
aggr.news / aggrnews.{com,io,app}        → HTTP 000 域名不存在 ❌；aggr.trade=盘口工具非新闻

# 官方/一线（免代理）
federalreserve.gov/feeds/press_all.xml   → 200 15KB  ✅
feeds.a.dj.com/rss/RSSMarketsMain.xml    → 200 13KB  ✅ WSJ
sec.gov EDGAR atom (8-K)                 → 403       ⚠️ 需自定义UA
coindesk …/outboundfeeds/rss/            → 308       ⚠️ 已改版
finnhub.io/api/v1/news                   → 401（缺key）；env已有key, 源仅禁用

# Serenity
x.com/aleabitoreddit                     → 本人账号（76.9万粉）
rsshub.app/twitter/user/aleabitoreddit   → 302       ❌ 公共RSSHub失效
analysissite.vercel.app                  → 200 237KB HTML, 含Serenity/AXTI/SIVE；/api·/rss 全404 ❌ 无feed
ch2rss.fflow.net/aleabitoreddit          → 404       ❌

# 机构 13F
NASDAQ api.nasdaq.com/institutional-portfolio/1324715 → 404 / 反爬
EDGAR 全文检索 "Situational Awareness LP" → 7 hits；CIK 0002045724, 13F-HR, 季度 2025-09-30 ✅
```

## 附录 B：参考链接
- trump.fm（RSS + /api/posts）：https://trump.fm/rss.xml
- BWEnews 官方 RSS：https://rss-public.bwe-ws.com/ ·  ws 文档：https://telegra.ph/BWEnews-API-documentation-06-19
- Alpaca News API（Benzinga/WS）：https://alpaca.markets/blog/introducing-news-api-for-real-time-fiancial-news
- SEC EDGAR（Situational Awareness LP）：CIK 0002045724
- Serenity @aleabitoreddit：https://x.com/aleabitoreddit
- Factba.se 现状：https://en.wikipedia.org/wiki/Factba.se
