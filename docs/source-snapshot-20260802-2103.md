# 源实况快照 · 2026-08-02 21:03

> 通过 `POST /api/sources/:name/fetch` 主动拉取全部 16 个源(同步执行 Fetch,不推送)。
> 周六窗口:pre-market/closing 受 A 股交易日门禁,us-macro-report 周末跳过,RSS 源多为 304/内容陈旧。
> 本快照为新版 news-aggregate 源(早报/晚报)首次实跑。

## 汇总

| 源 | 类型 | 定位 | 产出 | 耗时 |
|---|---|---|---|---|
| trump-rss | rss | 政治/政策 | 2 | 3442ms |
| bwe-tradfi | rss | 突发快讯 | 0 | 1928ms |
| kobeissi-letter | rss | 量化分析 | 0 | 675ms |
| fed-press | rss | 美联储官方 | 0 | 1367ms |
| bloomberg-markets | rss | 美股市场 | 5 | 2698ms |
| bloomberg-economics | rss | 宏观 | 0 | 4000ms |
| bloomberg-politics | rss | 政策/地缘 | 2 | 722ms |
| forexlive-breaking | rss | 投行快讯 | 0 | 1353ms |
| pre-market-briefing | 定时 | 盘前全景(周六门禁) | 0 | 0ms |
| guanfu-score | 定时 | 观复读盘 | 1 | 14335ms |
| market-summary | 定时 | 市场速览 | 1 | 38751ms |
| closing-briefing | 定时 | 收盘总览(周六门禁) | 0 | 0ms |
| us-preview | 定时 | 美盘前瞻 | 1 | 147462ms |
| us-macro-report | 定时 | 美国宏观(周末跳过) | 0 | 0ms |
| news-aggregate-morning | 定时 | 新闻聚合早报 | 1 | 19469ms |
| news-aggregate-evening | 定时 | 新闻聚合晚报 | 1 | 85802ms |

---

## trump-rss(rss)

**定位**:政治/政策 | **产出**:2 条 | **耗时**:3442ms

### 1. Trump's new NIH chief outlines sweeping reforms like new pandemic playbook, vaccine injury body: htt...

```
Trump's new NIH chief outlines sweeping reforms like new pandemic playbook, vaccine injury body: https:// justthenews.com/government/fed eral-agencies/trumps-new-nih-chief-outlines-sweeping-reforms-such-new-pandemic

── AI 分析 ──
Post from Truth Social 1h ago Aug 2, 2026 at 11:20 AM Trump's new NIH chief outlines sweeping reforms like new pandemic playbook, vaccine injury body: https:// justthenews.com/government/fed eral-agencies/trumps-new-nih-chief-outlines-sweeping-reforms-such-new-pandemic 0:00 0:00 1x Analysis Danger Level None Narcissistic State Grandiose Authorship Aide-Written Intensity 10% Overview Complete Authorship Analysis Aide-Written Indicators: Third-person self-reference ('Trump's new NIH chief') via pasted headline Bare headline + URL format with zero appended commentary or reaction Identical format and source (justthenews.com) as adjacent same-day post, indicating batch curation Clean grammar/spelling with no idiosyncratic Trump errors No first-person voice, ALL CAPS, or emotional drift Psycholo
```

### 2. Trump administration creating ‘incubators’ to develop new tech to counter drone terror attacks: http...

```
Trump administration creating ‘incubators’ to develop new tech to counter drone terror attacks: https:// justthenews.com/government/sec urity/combatting-fury-skies-drone-warfare-necessitates-new-protection-technology

── AI 分析 ──
Post from Truth Social 1h ago Aug 2, 2026 at 11:20 AM Trump administration creating ‘incubators’ to develop new tech to counter drone terror attacks: https:// justthenews.com/government/sec urity/combatting-fury-skies-drone-warfare-necessitates-new-protection-technology 0:00 0:00 1x Analysis Danger Level None Narcissistic State Grandiose Authorship Uncertain Intensity 12% Overview Complete Authorship Analysis Aide-Written Indicators: Third-person institutional framing ('Trump administration') rather than first-person voice Headline-plus-URL structure typical of curated/aide content shares Clean grammar and spelling with no idiosyncratic errors, homophones, or ALL-CAPS Dry policy/technology subject matter rather than reactive grievance Sharp stylistic contrast with the adjacent authentic fi
```

---

## bwe-tradfi(rss)

**定位**:突发快讯 | **产出**:0 条 | **耗时**:1928ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## kobeissi-letter(rss)

**定位**:量化分析 | **产出**:0 条 | **耗时**:675ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## fed-press(rss)

**定位**:美联储官方 | **产出**:0 条 | **耗时**:1367ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## bloomberg-markets(rss)

**定位**:美股市场 | **产出**:5 条 | **耗时**:2698ms

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

**定位**:宏观 | **产出**:0 条 | **耗时**:4000ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## bloomberg-politics(rss)

**定位**:政策/地缘 | **产出**:2 条 | **耗时**:722ms

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

**定位**:投行快讯 | **产出**:0 条 | **耗时**:1353ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## pre-market-briefing(定时)

**定位**:盘前全景(周六门禁) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## guanfu-score(定时)

**定位**:观复读盘 | **产出**:1 条 | **耗时**:14335ms

### 1. 🔭 观复读盘 · 防守倾向 | BTC $63158

```
现在市场整体偏冷，大伙儿都挺恐慌的，适合多看少动，别急着抄底。主要是最近钱在往外跑，大资金连续流出，加上价格一直沿着短期均线往下走，说明抛压还在，多头还没缓过劲来。不过也别太悲观，从估值角度看，比特币并不算贵，甚至慢慢接近历史上比较适合定投的区域。需要警惕的是，如果哪天真放量涨回关键位置，同时资金开始回流，那就要重新评估是不是趋势变了，到时候再跟也不迟。

── 数据摘要 ──
BTC $63158 | 恐贪 27 | 信号 -1/8 | 置信 低 | MVRV-Z 0.52 | AHR999 0.44
⚠️ 失效: 价格突破 mayer=1.0 + ETF 30d 转正 → 趋势可能已反转

```

---

## market-summary(定时)

**定位**:市场速览 | **产出**:1 条 | **耗时**:38751ms

### 1. 🌐 晚间 市场速览 · 08-02 20:58

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
🟢 Bitcoin USD: 63051.75 (+0.46%)

【汇率数据】
🔹 USD/CNY: 6.7700
🔹 USDT/CNY: 6.7700
🔹 EUR/CNY: 7.7906
🔹 HKD/CNY: 0.8635

更新时间: 2026-08-02 20:58
```

---

## closing-briefing(定时)

**定位**:收盘总览(周六门禁) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## us-preview(定时)

**定位**:美盘前瞻 | **产出**:1 条 | **耗时**:147462ms

### 1. 🌎 美盘前瞻 · 08-02

```
── 关键标的 ──
🟢 SPY: 747.03 (+0.72%)
🟢 QQQ: 687.99 (+0.65%)
🔴 Gold Aug 26: 4107.00 (-1.29%)
🟢 Crude Oil Se: 84.67 (+1.29%)
🟢 Bitcoin USD: 63068.96 (+0.49%)

── VIX ──
深度 Contango  9D:13.1  VIX:16.0  3M:19.0  6M:21.3  R:0.84

── 美股事件日历 ──
拆股: ASTN 1:4 DAMD 1:10

── 🤖 AI 前瞻研判 ──
盘前核心矛盾在于风险偏好高涨与通胀预期抬头并存：SPY、QQQ走高，原油涨1.29%强化利率“更高更久”忧虑，而黄金重挫1.29%暗示实际利率上行压力正在积聚。市场暂由盈利韧性叙事支撑科技股，但利率预期修正若持续，高估值科技动能将遭挑战，价值与防御板块相对受益，风格切换或一触即发。VIX仅15.99且9日水平低至13.05，期限结构深度Contango，暴露极度自满与贪婪信号，波动率反弹风险处高位。今晚紧盯美联储官员讲话及初请数据，原油续涨与鹰派言论或成恐慌导火索。

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

**定位**:新闻聚合早报 | **产出**:1 条 | **耗时**:19469ms

### 1. 🌅 新闻聚合 · 08-02

```
## 资讯摘要
- 特朗普新任 NIH 负责人概述全面改革，如新疫情应对手册、疫苗伤害机构（trump-rss）

2026-08-02 13:01 UTC

**🔗 BTC**
- BTC 跌破 **63,000** 美元，Polymarket 显示 8 月涨至 **7 万美元**概率仅 **26%**，跌至 6 万概率 **56%**。
- Saylor 再发 Bitcoin Tracker 信息，Strategy 或延续买入节奏。
- Coldcard 攻击发酵，小额 BTC 转账激增至 FTX 暴雷以来最高，自托管安全讨论升温；攻击者已聚集 **1,159 枚 BTC**（约 **7,271 万美元**）。
- Trump Media（DJT）疑似将 **2,628 枚 BTC**（约 **1.65 亿美元**）转入 Crypto.com，准备减持。
- 中国公安大学开发 AI 框架，追踪非法加密货币交易准确率约 **90%**。

**🤖 AI**
- 国家超算互联网上线 DeepSeek-V4-Flash 正式版 API，可一键接入调用。
- 苹果因 AI 生成漏洞报告激增，限制研究人员提交数量并设 **30 天**冷静期。
- 美国 AI 初创竞相打造中国廉价模型替代品，但面临融资阻力。
- 24 岁 AI 明星 Leopold 婚礼前举办研讨会，曾差点卖掉 **35 亿美元** Anthropic 股权后撤回。
- LAB 内幕地址时隔三周向 KuCoin 转移 **580 万枚** LAB（约 **83.4 万美元**）。

**📈 股票宏观**
- CME 数据显示美联储 9 月加息 **25 个基点**概率达 **67%**。
- 美国 ETF 上市狂潮：两月推 **1,001 支**，超半数含衍生品，三成为杠杆型。
- 韩国拟引入「紧急措施权」，可在剧烈波动时将单股杠杆 ETF 倍数从 2 倍降至 **1.5 倍**。
- 下周关注 SpaceX 首份财报、美国 7 月非农，AMD、闪迪、西数等集体放榜。
- 中国 VC 经历三年寒冬后竞相募资，至少 **60 只**美元基金拟筹 **350 亿美元**。
```

---

## news-aggregate-evening(定时)

**定位**:新闻聚合晚报 | **产出**:1 条 | **耗时**:85802ms

### 1. 🌆 新闻聚合 · 08-02

```
2026-08-02 13:02 UTC

**🔗 BTC**
- Michael Saylor再度发布比特币Tracker信息，市场关注Strategy后续动向；BTC跌破**63,000美元**后回升至**63,563美元**，资金费率摆脱看跌区间但未普遍看多。
- Coldcard攻击持续发酵，小额BTC转账升至FTX暴雷以来最高水平；攻击者地址持有**1,159枚BTC**（约**7,271万美元**），且已出现模仿攻击者。
- 某鲸鱼从BitMEX提取**105枚BTC**（约**666万美元**）；Trump Media疑似间隔两月再减持**2,628枚BTC**（约**1.65亿美元**）。
- 中国公安大学团队开发AI框架，检测非法加密货币交易准确率约**90%**；Polymarket显示BTC本月涨至**7万美元**概率为**26%**。
- BTC ETF单日净流出**2.654亿美元**，累计净流入**793.32亿美元**。

**🤖 AI**
- 中国国家超算互联网上线DeepSeek-V4-Flash正式版API，可一键接入调用。
- 苹果因AI辅助生成的漏洞报告激增，限制研究人员提交数量并设**30天**冷静期。
- 美国初创公司Arcee AI、Reflection AI等竞相打造中国模型低成本替代品，担忧Kimi、Qwen、DeepSeek压缩利润空间。
- 24岁前OpenAI研究员Leopold婚礼前先开研讨会，曾同意出售**35亿美元**Anthropic股权，次日撤回。

**📈 股票宏观**
- CME数据显示美联储9月加息**25个基点**概率为**67%**；美国7月非农将成利率路径关键参考。
- 美国ETF上市狂潮：两个月推**1,001支**，**54%**含衍生品，约三成为杠杆型。
- 韩国拟引入“紧急措施权”，市场剧烈波动时可临时将单股杠杆ETF倍数从2倍降至**1.5倍**或1倍。
- 下周SpaceX首份财报叠加AMD、闪迪、西数放榜；字节系用户时长占比升至**40.1%**，首超腾讯系。
- 中国VC经历三年低迷后加速募资，至少**60只**美元基金拟筹**350亿美元**。
```

---
