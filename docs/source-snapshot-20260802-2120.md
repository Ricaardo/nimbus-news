# 源实况快照 · 2026-08-02 21:20

> 通过 `POST /api/sources/:name/fetch` 主动拉取全部 16 个源(同步执行 Fetch,不推送)。
> 周六窗口:pre-market/closing 受 A 股交易日门禁,us-macro-report 周末跳过,RSS 源多为 304/内容陈旧。
> 本快照为新版 news-aggregate 源(早报/晚报)首次实跑。

## 汇总

| 源 | 类型 | 定位 | 产出 | 耗时 |
|---|---|---|---|---|
| trump-rss | rss | 政治/政策 | 3 | 4992ms |
| bwe-tradfi | rss | 突发快讯 | 0 | 1072ms |
| kobeissi-letter | rss | 量化分析 | 0 | 875ms |
| fed-press | rss | 美联储官方 | 0 | 527ms |
| bloomberg-markets | rss | 美股市场 | 5 | 2506ms |
| bloomberg-economics | rss | 宏观 | 0 | 1042ms |
| bloomberg-politics | rss | 政策/地缘 | 2 | 1275ms |
| forexlive-breaking | rss | 投行快讯 | 0 | 373ms |
| pre-market-briefing | 定时 | 盘前全景(周六门禁) | 0 | 0ms |
| guanfu-score | 定时 | 观复读盘 | 1 | 35862ms |
| market-summary | 定时 | 市场速览 | 1 | 33229ms |
| closing-briefing | 定时 | 收盘总览(周六门禁) | 0 | 0ms |
| us-preview | 定时 | 美盘前瞻 | 1 | 91600ms |
| us-macro-report | 定时 | 美国宏观(周末跳过) | 0 | 0ms |
| news-aggregate-morning | 定时 | 新闻聚合早报 | 1 | 66017ms |
| news-aggregate-evening | 定时 | 新闻聚合晚报 | 1 | 53968ms |

---

## trump-rss(rss)

**定位**:政治/政策 | **产出**:3 条 | **耗时**:4992ms

### 1. Why I Put President Trump's Name on Palm Beach's Airport: https://townhall.com/columnists/meg-weinbe...

```
Why I Put President Trump's Name on Palm Beach's Airport: https:// townhall.com/columnists/meg-we inberger/2026/07/14/why-i-put-president-trumps-name-on-palm-beachs-airport-n2679374

── AI 分析 ──
Post from Truth Social 16m ago Aug 2, 2026 at 12:58 PM Why I Put President Trump's Name on Palm Beach's Airport: https:// townhall.com/columnists/meg-we inberger/2026/07/14/why-i-put-president-trumps-name-on-palm-beachs-airport-n2679374 Analysis Danger Level None Narcissistic State Grandiose Authorship Uncertain Intensity 20% Overview Complete Low clinical intensity. This is a bare link share — headline plus URL, no commentary — of a Townhall column by a Palm Beach County commissioner explaining why she named Palm Beach's airport after Trump. It sits within a same-day batch of similarly templated policy links, but its self-referential subject sets it apart. Psychologically the post reads as routine grandiose narcissistic supply-seeking of the maintenance variety: the subject re-broadcasts
```

### 2. Trump's new NIH chief outlines sweeping reforms like new pandemic playbook, vaccine injury body: htt...

```
Trump's new NIH chief outlines sweeping reforms like new pandemic playbook, vaccine injury body: https:// justthenews.com/government/fed eral-agencies/trumps-new-nih-chief-outlines-sweeping-reforms-such-new-pandemic

── AI 分析 ──
Post from Truth Social 1h ago Aug 2, 2026 at 11:20 AM Trump's new NIH chief outlines sweeping reforms like new pandemic playbook, vaccine injury body: https:// justthenews.com/government/fed eral-agencies/trumps-new-nih-chief-outlines-sweeping-reforms-such-new-pandemic 0:00 0:00 1x Analysis Danger Level None Narcissistic State Grandiose Authorship Aide-Written Intensity 10% Overview Complete Authorship Analysis Aide-Written Indicators: Third-person self-reference ('Trump's new NIH chief') via pasted headline Bare headline + URL format with zero appended commentary or reaction Identical format and source (justthenews.com) as adjacent same-day post, indicating batch curation Clean grammar/spelling with no idiosyncratic Trump errors No first-person voice, ALL CAPS, or emotional drift Psycholo
```

### 3. Trump administration creating ‘incubators’ to develop new tech to counter drone terror attacks: http...

```
Trump administration creating ‘incubators’ to develop new tech to counter drone terror attacks: https:// justthenews.com/government/sec urity/combatting-fury-skies-drone-warfare-necessitates-new-protection-technology

── AI 分析 ──
Post from Truth Social 1h ago Aug 2, 2026 at 11:20 AM Trump administration creating ‘incubators’ to develop new tech to counter drone terror attacks: https:// justthenews.com/government/sec urity/combatting-fury-skies-drone-warfare-necessitates-new-protection-technology 0:00 0:00 1x Analysis Danger Level None Narcissistic State Grandiose Authorship Uncertain Intensity 12% Overview Complete Authorship Analysis Aide-Written Indicators: Third-person institutional framing ('Trump administration') rather than first-person voice Headline-plus-URL structure typical of curated/aide content shares Clean grammar and spelling with no idiosyncratic errors, homophones, or ALL-CAPS Dry policy/technology subject matter rather than reactive grievance Sharp stylistic contrast with the adjacent authentic fi
```

---

## bwe-tradfi(rss)

**定位**:突发快讯 | **产出**:0 条 | **耗时**:1072ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## kobeissi-letter(rss)

**定位**:量化分析 | **产出**:0 条 | **耗时**:875ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## fed-press(rss)

**定位**:美联储官方 | **产出**:0 条 | **耗时**:527ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## bloomberg-markets(rss)

**定位**:美股市场 | **产出**:5 条 | **耗时**:2506ms

### 1. Yen Traders Brace for More Intervention With US at Japan’s Side

```
Currency traders are on high alert for more joint intervention by Japan and the US when trading gets underway in Asia on Monday after coordinated operations in Tokyo and New York last week triggered a dramatic rebound in the yen.
```

### 2. Ugly Month in Emerging Markets May Be a Taste of What’s Ahead

```
Investors’ hopes of a stellar year in emerging markets are being tested by a bruising July that may offer a taste of the headwinds ahead.
```

### 3. Bomb Kills Three, Injures 21 at Restaurant on Moscow’s Kudrinskaya Square

```
Three people were killed and 21 injured after a bomb was detonated in an upscale restaurant in central Moscow on Saturday night, according to Russia’s National Anti-Terrorism Committee.
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

**定位**:宏观 | **产出**:0 条 | **耗时**:1042ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## bloomberg-politics(rss)

**定位**:政策/地缘 | **产出**:2 条 | **耗时**:1275ms

### 1. Trump Holds Off Iran Strikes on Pledge a Hormuz Deal Is Close

```
US President Donald Trump said he’s holding off from new strikes on Iran, after Tehran and other Middle Eastern powers told him they’re working on an agreement that might quickly reopen the Strait of Hormuz.
```

### 2. Morocco Says Trump Highway Naming Is Mark of ‘Deep Appreciation’

```
Moroccan King Mohammed VI said his decision to give US President Donald Trump’s name to a $1 billion highway that crosses into the disputed Western Sahara expresses his “deep appreciation” of the American leader.
```

---

## forexlive-breaking(rss)

**定位**:投行快讯 | **产出**:0 条 | **耗时**:373ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## pre-market-briefing(定时)

**定位**:盘前全景(周六门禁) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## guanfu-score(定时)

**定位**:观复读盘 | **产出**:1 条 | **耗时**:35862ms

### 1. 🔭 观复读盘 · 防守倾向 | BTC $63158

```
现在市场整体偏冷，情绪有点慌，短期适合先稳住、别急着抄底。因为资金还在持续流出，技术面也是空头占优，盘面没什么进攻欲望。不过估值角度看，比特币并不算贵，所以也别太悲观，更像是磨底的阶段。需要注意的一个风险是：如果哪天价格重新站上关键均线，同时资金又转为持续流入，那这波调整可能就悄悄结束了，到时候要及时调整思路。

── 数据摘要 ──
BTC $63158 | 恐贪 27 | 信号 -1/8 | 置信 低 | MVRV-Z 0.52 | AHR999 0.44
⚠️ 失效: 价格突破 mayer=1.0 + ETF 30d 转正 → 趋势可能已反转

```

---

## market-summary(定时)

**定位**:市场速览 | **产出**:1 条 | **耗时**:33229ms

### 1. 🌐 晚间 市场速览 · 08-02 21:15

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
🟢 Bitcoin USD: 63030.02 (+0.43%)

【汇率数据】
🔹 USD/CNY: 6.7700
🔹 USDT/CNY: 6.7700
🔹 EUR/CNY: 7.7906
🔹 HKD/CNY: 0.8635

更新时间: 2026-08-02 21:15
```

---

## closing-briefing(定时)

**定位**:收盘总览(周六门禁) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## us-preview(定时)

**定位**:美盘前瞻 | **产出**:1 条 | **耗时**:91600ms

### 1. 🌎 美盘前瞻 · 08-02

```
── 关键标的 ──
🟢 SPY: 747.03 (+0.72%)
🟢 QQQ: 687.99 (+0.65%)
🔴 Gold Aug 26: 4107.00 (-1.29%)
🟢 Crude Oil Se: 84.67 (+1.29%)
🟢 Bitcoin USD: 63013.92 (+0.40%)

── 利率路径 ──
EFFR 3.63%  🔴 09-16  3.81%  ⚪ 10-28  3.88%  ⚪ 12-09  4.00%  ⚪ 01-27  4.05%  ⚪ 03-17  4.13%  ⚪ 04-28  4.17%

── VIX ──
深度 Contango  9D:13.1  VIX:16.0  3M:19.0  6M:21.3  R:0.84

── 美股事件日历 ──
拆股: ASTN 1:4 DAMD 1:10


```

---

## us-macro-report(定时)

**定位**:美国宏观(周末跳过) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## news-aggregate-morning(定时)

**定位**:新闻聚合早报 | **产出**:1 条 | **耗时**:66017ms

### 1. 🌅 新闻聚合 · 08-02

```
2026-08-02 13:17 UTC

**🔗 BTC**
- BTC在**6.3万美元**附近震荡，资金费率摆脱看跌但未转普遍看多；Polymarket预测8月涨至**7万美元**概率仅**26%**。
- Coldcard攻击持续发酵，小额转账活跃度升至FTX暴雷以来最高，超**1159枚BTC**（约**7271万美元**）仍在攻击者地址。
- Saylor发「Bitcoin Drive engaged」暗示Strategy或加仓；Trump Media疑似减持**2628枚BTC**（约**1.65亿美元**）。
- 中国公安大学开发AI框架检测非法加密交易，整体准确率约**90%**。
- 新鲸鱼从BitMEX提取**105枚BTC**（约**666万美元**）。

**🤖 AI**
- 国家超算互联网上线DeepSeek-V4-Flash正式版API，一键接入即可调用。
- 苹果因AI生成漏洞报告激增，限制提交数量并设**30天**冷静期。
- 美国AI初创Arcee AI、Reflection AI竞相打造中国开源模型廉价替代品，仍面临融资阻力。
- 24岁Leopold卖约**160亿美元**股票组合给Citadel后，婚礼前先开研讨会；此前还险些出售**35亿美元**Anthropic股权。

**📈 股票宏观**
- CME「美联储观察」：9月加息**25bp**概率**67%**，维持不变概率**33%**。
- 本周美国**7月非农**、SpaceX首份财报、AMD/闪迪/西数集体放榜。
- 伯克希尔股价创**八个月新高**，苹果、可口可乐、美银三大重仓股年内涨幅可观。
- 美国ETF两月上市**1001支**，**54%**含衍生品，三成为杠杆型。
- 中国VC募资回暖，至少**60只**美元基金拟筹**350亿美元**。
```

---

## news-aggregate-evening(定时)

**定位**:新闻聚合晚报 | **产出**:1 条 | **耗时**:53968ms

### 1. 🌆 新闻聚合 · 08-02

```
2026-08-02 13:18 UTC

**🔗 BTC**
- Michael Saylor 再次发布比特币 Tracker，市场预期 Strategy 或重启买入。
- 比特币震荡跌破 **63,000** 美元，资金费率显示 BTC/ETH 均已脱离看跌区间，但尚未全面看多。
- Coldcard 遭攻击后，比特币小额转账激增至 FTX 暴雷以来最高水平，自托管担忧升温。
- Trump Media 疑似将 **2,628** 枚 BTC（约 **1.65 亿美元**）转入 Crypto.com，准备减持。
- Polymarket 显示，比特币 8 月升至 **70,000** 美元概率为 **26%**，跌至 **60,000** 美元概率为 **56%**。

**🤖 AI**
- 国家超算互联网上线 DeepSeek-V4-Flash 正式版 API，可一键接入调用。
- 苹果因 AI 生成的漏洞报告激增，限制研究人员提交数量并设 **30** 天冷静期。
- 美国 AI 初创公司竞相打造中国模型（Kimi、Qwen、DeepSeek）的廉价替代品，但融资阻力仍存。
- 24 岁前 OpenAI 研究员 Leopold Aschenbrenner 婚礼前先开 AI 研讨会，还曾一度同意出售价值 **35 亿美元**的 Anthropic 股权，次日撤回。

**📈 股票宏观**
- CME 数据显示，美联储 9 月加息 **25bp** 概率现报 **67%**。
- 伯克希尔股价创八个月新高，重仓苹果、可口可乐、美国银行年内涨幅可观。
- 美国 ETF 上市狂潮：两个月 **1,001** 支新品，超半数含衍生品，三成为杠杆型。
- 伊朗称将打击位于巴林的亚马逊数据中心，地缘风险升温。
- 下周聚焦美国 7 月非农、SpaceX 首份财报，AMD/闪迪/西数等密集放榜。
```

---
