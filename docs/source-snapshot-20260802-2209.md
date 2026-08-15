# 源实况快照 · 2026-08-02 22:09

> 通过 `POST /api/sources/:name/fetch` 主动拉取全部 16 个源(同步执行 Fetch,不推送)。
> 周六窗口:pre-market/closing 受 A 股交易日门禁,us-macro-report 周末跳过,RSS 源多为 304/内容陈旧。
> 本快照为新版 news-aggregate 源(早报/晚报)首次实跑。

## 汇总

| 源 | 类型 | 定位 | 产出 | 耗时 |
|---|---|---|---|---|
| trump-rss | rss | 政治/政策 | 4 | 22455ms |
| bwe-tradfi | rss | 突发快讯 | 0 | 601ms |
| kobeissi-letter | rss | 量化分析 | 1 | 2880ms |
| fed-press | rss | 美联储官方 | 0 | 3681ms |
| bloomberg-markets | rss | 美股市场 | 5 | 7229ms |
| bloomberg-economics | rss | 宏观 | 0 | 1946ms |
| bloomberg-politics | rss | 政策/地缘 | 0 | 3752ms |
| forexlive-breaking | rss | 投行快讯 | 0 | 3543ms |
| pre-market-briefing | 定时 | 盘前全景(周六门禁) | 0 | 0ms |
| guanfu-score | 定时 | 观复读盘 | 1 | 14528ms |
| market-summary | 定时 | 市场速览 | 1 | 35738ms |
| closing-briefing | 定时 | 收盘总览(周六门禁) | 0 | 0ms |
| us-preview | 定时 | 美盘前瞻 | 1 | 137864ms |
| us-macro-report | 定时 | 美国宏观(周末跳过) | 0 | 0ms |
| news-aggregate-morning | 定时 | 新闻聚合早报 | 1 | 21417ms |
| news-aggregate-evening | 定时 | 新闻聚合晚报 | 1 | 50208ms |
| fed-speeches | rss | 美联储官员讲话 | 0 | 5100ms |

---

## trump-rss(rss)

**定位**:政治/政策 | **产出**:4 条 | **耗时**:22455ms

### 1. Why I Put President Trump's Name on Palm Beach's Airport: https://townhall.com/columnists/meg-weinbe...

```
Why I Put President Trump's Name on Palm Beach's Airport: https:// townhall.com/columnists/meg-we inberger/2026/07/14/why-i-put-president-trumps-name-on-palm-beachs-airport-n2679374
```

### 2. Trump’s Energy Triumph: https://www.wsj.com/opinion/trumps-energy-triumph-82e4b953

```
Trump’s Energy Triumph: https://www. wsj.com/opinion/trumps-energy- triumph-82e4b953
```

### 3. Mexico's Problem: https://www.billoreilly.com/b/Mexicos-Problem/-45344169968241838.html

```
Mexico's Problem: https://www. billoreilly.com/b/Mexicos-Prob lem/-45344169968241838.html

── AI 分析 ──
Post from Truth Social 1h ago Aug 2, 2026 at 12:57 PM Mexico's Problem: https://www. billoreilly.com/b/Mexicos-Prob lem/-45344169968241838.html 0:00 0:00 1x Analysis Danger Level None Narcissistic State Grandiose Authorship Uncertain Intensity 15% Overview Complete Authorship Analysis Uncertain Indicators: Mid-morning local time (~8:57 AM ET) — business hours, weakly aide-leaning Clean formatting, no typos, ALL-CAPS, or stream-of-consciousness — weakly aide-leaning Bare 'Title: URL' amplification of a friendly commentator (O'Reilly) is a long-standing authentic Trump behavior Part of a same-day cluster of similar link-drops consistent with personal scroll-and-share pattern No first-person voice or emotional markers to disambiguate Psychological Profile ▶ State Grandiose State Trigger: Main
```

### 4. 'PO'd' Evers vows to resist any FBI effort to seize Milwaukee ballots: https://www.jsonline.com/stor...

```
'PO'd' Evers vows to resist any FBI effort to seize Milwaukee ballots: https://www. jsonline.com/story/news/politi cs/2026/03/11/tony-evers-vows-to-resist-any-effort-to-seize-2020-milwaukee-ballots/89097930007/

── AI 分析 ──
Post from Truth Social 1h ago Aug 2, 2026 at 12:57 PM 'PO'd' Evers vows to resist any FBI effort to seize Milwaukee ballots: https://www. jsonline.com/story/news/politi cs/2026/03/11/tony-evers-vows-to-resist-any-effort-to-seize-2020-milwaukee-ballots/89097930007/ 0:00 0:00 1x Analysis Danger Level Elevated Narcissistic State Mixed Authorship Uncertain Intensity 40% Overview Complete Low-text amplification post: the subject re-shares a March-2026 headline about Wisconsin Gov. Evers resisting an FBI effort to seize 2020 Milwaukee ballots, appending no commentary. Psychological signal lies in the curatorial choice, not prose. The post exemplifies durable election-denial perseveration — a six-year-old, evidence-poor grievance kept alive as an unhealed narcissistic injury. Dominant motive is a
```

---

## bwe-tradfi(rss)

**定位**:突发快讯 | **产出**:0 条 | **耗时**:601ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## kobeissi-letter(rss)

**定位**:量化分析 | **产出**:1 条 | **耗时**:2880ms

### 1. BREAKING: Iran says reports stating that it has agreed to a deal with the US to reopen the Strait of Hormuz are false, per Iran's Fars News. Iran also says that the Strait of Hormuz remains closed to any ship that does not coordinate with the...

```
BREAKING: Iran says reports stating that it has agreed to a deal with the US to reopen the Strait of Hormuz are false, per Iran's Fars News. Iran also says that the Strait of Hormuz remains closed to any ship that does not coordinate with the IRGC. President Trump said the "perimeters of a deal" were reached last night. US stock market futures open in 9 hours. ( @TheKobeissiLetter )
```

---

## fed-press(rss)

**定位**:美联储官方 | **产出**:0 条 | **耗时**:3681ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## bloomberg-markets(rss)

**定位**:美股市场 | **产出**:5 条 | **耗时**:7229ms

### 1. Wall Street’s Favorite Bet Comes Undone as Chips Whipsaw Market

```
The one-way trade in semiconductor stocks that has defined equity markets this year is coming unglued, triggering stomach-churning volatility as investors grow increasingly concerned that the fire-hose of artificial intelligence spending won’t continue.
```

### 2. Yen Traders Brace for More Intervention With US at Japan’s Side

```
Currency traders are on high alert for more joint intervention by Japan and the US when trading gets underway in Asia on Monday after coordinated operations in Tokyo and New York last week triggered a dramatic rebound in the yen.
```

### 3. Ugly Month in Emerging Markets May Be a Taste of What’s Ahead

```
Investors’ hopes of a stellar year in emerging markets are being tested by a bruising July that may offer a taste of the headwinds ahead.
```

### 4. Banks Offload Risk from Leveraged ETFs With Exotic ‘Crash Puts’

```
Leveraged ETFs that offer the tantalizing prospect of doubling or tripling the daily returns of an individual stock are famously risky for investors who buy them.
```

### 5. ANZ, NAB Among Banks Funding Blackstone HSBC Book Buy, AFR Says

```
ANZ Group Holdings Ltd. and National Australia Bank Ltd. are among lenders bankrolling Blackstone Inc.’s A$36 billion ($25.3 billion) purchase of HSBC Holdings Plc’s Australian retail loan portfolio, the Australian Financial Review reported Sunday, citing unnamed sources.
```

---

## bloomberg-economics(rss)

**定位**:宏观 | **产出**:0 条 | **耗时**:1946ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## bloomberg-politics(rss)

**定位**:政策/地缘 | **产出**:0 条 | **耗时**:3752ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## forexlive-breaking(rss)

**定位**:投行快讯 | **产出**:0 条 | **耗时**:3543ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## pre-market-briefing(定时)

**定位**:盘前全景(周六门禁) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## guanfu-score(定时)

**定位**:观复读盘 | **产出**:1 条 | **耗时**:14528ms

### 1. 🔭 观复读盘 · 防守倾向 | BTC $63158

```
现在这行情明显是防守阶段，不太适合激进操作，等信号明朗再动手更稳妥。

市场最近被持续流出的资金压着，短期价格动能也偏弱，所以盘面给出的判断是偏防守的。不过有个估值指标反而显示现在没那么贵，算是多空在拉扯。需要注意，如果价格稳稳站回一个关键均线，并且资金开始回头往里冲，那这轮调整可能就结束了——但现在还早，别急着抄底。

── 数据摘要 ──
BTC $63158 | 恐贪 27 | 信号 -1/8 | 置信 低 | MVRV-Z 0.52 | AHR999 0.44
⚠️ 失效: 价格突破 mayer=1.0 + ETF 30d 转正 → 趋势可能已反转

```

---

## market-summary(定时)

**定位**:市场速览 | **产出**:1 条 | **耗时**:35738ms

### 1. 🌐 晚间 市场速览 · 08-02 22:04

```
【行情数据】
🟢 SPY: 747.03 (+0.72%)
🟢 QQQ: 687.99 (+0.65%)
🔴 Gold Aug 26: 4107.00 (-1.29%)
🔴 Silver Sep 2: 57.79 (-2.09%)
🟢 HANG SENG IN: 25884.43 (+0.10%)
🟢 HSTECH ETF: 4.82 (+0.42%)
🔴 ^VIX: 15.99 (-6.88%)
🟢 Crude Oil Se: 84.67 (+1.29%)
🔴 IBIT: 35.64 (-2.89%)
🟢 Bitcoin USD: 63021.58 (+0.41%)

【汇率数据】
🔹 USD/CNY: 6.7700
🔹 USDT/CNY: 6.7700
🔹 EUR/CNY: 7.7906
🔹 HKD/CNY: 0.8635

更新时间: 2026-08-02 22:04
```

---

## closing-briefing(定时)

**定位**:收盘总览(周六门禁) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## us-preview(定时)

**定位**:美盘前瞻 | **产出**:1 条 | **耗时**:137864ms

### 1. 🌎 美盘前瞻 · 08-02

```
── 关键标的 ──
🟢 SPY: 747.03 (+0.72%)
🟢 QQQ: 687.99 (+0.65%)
🔴 Gold Aug 26: 4107.00 (-1.29%)
🟢 Crude Oil Se: 84.67 (+1.29%)
🟢 Bitcoin USD: 63008.77 (+0.39%)

── VIX ──
深度 Contango  9D:13.1  VIX:16.0  3M:19.0  6M:21.3  R:0.84

── 美股事件日历 ──
拆股: ASTN 1:4 DAMD 1:10

── 🤖 AI 前瞻研判 ──
盘前核心矛盾在于再通胀交易升温与降息预期回摆的角力。原油涨1.29%与黄金跌1.29%形成鲜明对比，暗示市场定价强劲需求而非避险，推升周期价值；同时VIX期限深度Contango（VIX9D仅13.05）显示现货市场极度放松，短期几无恐慌。

利率预期方面，隐含再通胀令宽松路径存疑，若美债收益率维持高位，科技/成长股高估值承压（QQQ涨幅落后SPY），资金向能源、工业等价值板块轮动，防御性资产被冷落。

VIX恐慌信号：绝对读数15.99偏低，配合VIX/VIX3M比率0.841的深度Contango，反映市场处在自满期，需警惕尾部风险定价不足。Bitcoin小幅上涨亦显示风险偏好修复。

今晚关键风险：关注ISM服务业PMI及美联储官员讲话，若数据超预期强劲或言论偏鹰，易触发利率敏感型资产的抛售；原油若持续上行，将加剧风格切换，压制纳指表现。

## 资讯摘要
- 片山表示日本与美国正协调遏制日元疲软（bloomberg-markets）
- ANZ、NAB等银行据称出资支持黑石收购汇丰澳洲零售贷款组合，《澳洲金融评论》援引消息人士称（bloomberg-markets）
- 特朗普称若快速达成协议，美国将取消对伊朗的打击（bloomberg-markets）
- 中国经济正在失去动力：7月制造业PMI降至49.2，为2023年以来最低之一（kobeissi-letter）
- 突发：美国个人储蓄率6月降至2.7%，创2022年6月以来新低（kobeissi-letter）
- Tradfin：*特朗普：取消袭击的前提是能够迅速达成协议 *特朗普：以色列与我一道做出这项承诺（bwe-tradfi）
```

---

## us-macro-report(定时)

**定位**:美国宏观(周末跳过) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## news-aggregate-morning(定时)

**定位**:新闻聚合早报 | **产出**:1 条 | **耗时**:21417ms

### 1. 🌅 新闻聚合 · 08-02

```
2026-08-02 14:07 UTC

**🔗 BTC**
- 比特币在 **63,000 美元**附近震荡，现报 **63,563 美元**，资金费率显示 BTC/ETH 已脱离看跌区间，但未全面转多。
- Strategy 创始人 Michael Saylor 再次发布比特币 Tracker 信息，市场关注其潜在买入动向。
- Polymarket 预测比特币 8 月涨至 **70,000 美元**概率为 **26%**，跌至 **60,000 美元**概率为 **56%**。
- 特朗普旗下 Trump Media 疑似将 **2,628 枚 BTC**（约 **1.65 亿美元**）转入 Crypto.com，或再度减持。
- 中国公安大学团队开发 AI 框架，以约 **90%** 准确率追踪非法加密货币交易。

**🤖 AI**
- 英伟达 Rubin 减配传闻澄清：已交付标准版 Vera Rubin 未减配，秋季放量；Rubin Ultra 规格未锁定。
- 中国国家超算互联网上线 DeepSeek-V4-Flash 正式版 API，可一键调用。
- 苹果因 AI 生成漏洞报告激增，限制研究人员提交数量并设 **30 天**冷静期。
- 美国 AI 初创公司竞相打造中国廉价模型替代品，但面临融资阻力。
- Hugging Face CEO 称 AI 安全应“加速前进”，不能忽视更大远景。

**📈 股票宏观**
- CME 数据显示，美联储 9 月加息 **25 个基点**概率升至 **67%**。
- 伯克希尔股价创 **8 个月**新高，重仓股苹果、可口可乐、美国银行年内涨幅可观。
- 字节系移动互联网用户时长占比升至 **40.1%**，首超腾讯系。
- 下周焦点：美国 7 月非农、SpaceX 首份财报及 AMD、闪迪、西数业绩。
- Trade.xyz 永续合约提前定价周一美股韩股：三星、海力士预计低开超 **5%**，美股盘前略走高。
```

---

## news-aggregate-evening(定时)

**定位**:新闻聚合晚报 | **产出**:1 条 | **耗时**:50208ms

### 1. 🌆 新闻聚合 · 08-02

```
2026-08-02 14:08 UTC

**🔗 BTC**
- 比特币跌破63,000美元后回升至63,563美元，资金费率显示BTC/ETH已脱离看跌区间
- Saylor再次发布比特币Tracker信号，市场关注Strategy后续买入动向
- Polymarket：8月比特币涨至7万美元概率**26%**，触及6.5万美元概率**80%**
- Trump Media疑似将**2628枚BTC**转入Crypto.com，价值约**1.65亿美元**
- BTC ETF单日净流出**2.65亿美元**（累计793.3亿）；USDT市值1833亿，USDC市值720亿

**🤖 AI**
- 英伟达Rubin减配传闻澄清：标准版Vera Rubin已交付数十家客户，秋季放量，Rubin Ultra规格未锁定
- Hugging Face CEO：AI驱动攻击频发，但更应加速前行，开源防御尝试已在进行
- LAB内幕地址时隔三周向KuCoin充值**580万枚LAB**（约83.4万美元），仍持有7470万枚

**📈 股票宏观**
- 美联储9月加息25bp概率现报**67%**，利率路径仍是核心变量
- 伯克希尔股价创八个月新高，苹果、可口可乐、美国银行年内涨幅可观
- 字节系移动互联网用户时长首超腾讯系，占比升至**40.1%**
- 下周聚焦美国7月非农、SpaceX首份财报，AMD/闪迪/西数集体放榜
- Coldcard攻击持续发酵，已出现更多小型攻击者与模仿者
```

---

## fed-speeches(rss)

**定位**:美联储官员讲话 | **产出**:0 条 | **耗时**:5100ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---
