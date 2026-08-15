# 源实况快照 · 2026-08-03 00:02

> 通过 `POST /api/sources/:name/fetch` 主动拉取全部 16 个源(同步执行 Fetch,不推送)。
> 周六窗口:pre-market/closing 受 A 股交易日门禁,us-macro-report 周末跳过,RSS 源多为 304/内容陈旧。
> 本快照为新版 news-aggregate 源(早报/晚报)首次实跑。

## 汇总

| 源 | 类型 | 定位 | 产出 | 耗时 |
|---|---|---|---|---|
| trump-rss | rss | 政治/政策 | 0 | 7729ms |
| bwe-tradfi | rss | 突发快讯 | 0 | 1908ms |
| kobeissi-letter | rss | 量化分析 | 1 | 602ms |
| fed-press | rss | 美联储官方 | 0 | 8449ms |
| bloomberg-markets | rss | 美股市场 | 5 | 3053ms |
| bloomberg-economics | rss | 宏观 | 1 | 1200ms |
| bloomberg-politics | rss | 政策/地缘 | 1 | 2776ms |
| forexlive-breaking | rss | 投行快讯 | **ERROR** {"error":"RSS request failed:  | 12922ms |
| pre-market-briefing | 定时 | 盘前全景(周六门禁) | 0 | 0ms |
| guanfu-score | 定时 | 观复读盘 | 1 | 39795ms |
| market-summary | 定时 | 市场速览 | 1 | 32291ms |
| closing-briefing | 定时 | 收盘总览(周六门禁) | 0 | 0ms |
| us-preview | 定时 | 美盘前瞻 | 1 | 68647ms |
| us-macro-report | 定时 | 美国宏观(周末跳过) | 0 | 0ms |
| news-aggregate-morning | 定时 | 新闻聚合早报 | 1 | 52458ms |
| news-aggregate-evening | 定时 | 新闻聚合晚报 | 1 | 114586ms |
| fed-speeches | rss | 美联储官员讲话 | 0 | 828ms |
| eia-energy | rss | 美国能源官方 | 0 | 1506ms |

---

## trump-rss(rss)

**定位**:政治/政策 | **产出**:0 条 | **耗时**:7729ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## bwe-tradfi(rss)

**定位**:突发快讯 | **产出**:0 条 | **耗时**:1908ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## kobeissi-letter(rss)

**定位**:量化分析 | **产出**:1 条 | **耗时**:602ms

### 1. Key Events This Week: 1. Markets React to Trump Cancelling US Strikes on Iran - Today, 6 PM ET 2. July ISM Manufacturing PMI data - Monday 3. June JOLTS Job Openings data - Tuesday 4. AMD, $AMD, and SpaceX, $SPCX, Report Earnings - Tuesday 5. July...

```
Key Events This Week: 1. Markets React to Trump Cancelling US Strikes on Iran - Today, 6 PM ET 2. July ISM Manufacturing PMI data - Monday 3. June JOLTS Job Openings data - Tuesday 4. AMD, $AMD, and SpaceX, $SPCX, Report Earnings - Tuesday 5. July ADP Nonfarm Employment Change data - Wednesday 6. SanDisk, $SNDK, Reports Earnings - Wednesday 7. July Jobs Report - Friday 8. ~20% of S&P 500 companies report earnings this week We have a huge week ahead of us. ( @TheKobeissiLetter )
```

---

## fed-press(rss)

**定位**:美联储官方 | **产出**:0 条 | **耗时**:8449ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## bloomberg-markets(rss)

**定位**:美股市场 | **产出**:5 条 | **耗时**:3053ms

### 1. Bond Traders Flying Blind on Fed See Risk Yields Spiral Higher

```
Bond investors including Brandywine Global Investment Management and Wellington Management say the risk of a deeper Treasury rout is rising as Federal Reserve Chairman Kevin Warsh keeps investors in the dark about how officials will respond to the evolving economy.
```

### 2. Wall Street’s Favorite Bet Comes Undone as Chips Whipsaw Market

```
The one-way trade in semiconductor stocks that has defined equity markets this year is coming unglued, triggering stomach-churning volatility as investors grow increasingly concerned that the fire-hose of artificial intelligence spending won’t continue.
```

### 3. Yen Traders Brace for More Intervention With US at Japan’s Side

```
Currency traders are on high alert for more joint intervention by Japan and the US when trading gets underway in Asia on Monday after coordinated operations in Tokyo and New York last week triggered a dramatic rebound in the yen.
```

### 4. Ugly Month in Emerging Markets May Be a Taste of What’s Ahead

```
Investors’ hopes of a stellar year in emerging markets are being tested by a bruising July that may offer a taste of the headwinds ahead.
```

### 5. Banks Offload Risk from Leveraged ETFs With Exotic ‘Crash Puts’

```
Leveraged ETFs that offer the tantalizing prospect of doubling or tripling the daily returns of an individual stock are famously risky for investors who buy them.
```

---

## bloomberg-economics(rss)

**定位**:宏观 | **产出**:1 条 | **耗时**:1200ms

### 1. Australia’s Housing Market Worsens With Falls Getting Steeper

```
Australia’s housing market decline worsened, with prices declining in June and July by the most since December 2022 as rising interest rates and tax changes hit demand for homes.
```

---

## bloomberg-politics(rss)

**定位**:政策/地缘 | **产出**:1 条 | **耗时**:2776ms

### 1. Trump Holds Off Iran Strikes on Pledge a Hormuz Deal Is Close

```
US President Donald Trump said he’s holding off from new strikes on Iran, after Tehran and other Middle Eastern powers told him they’re working on an agreement that might quickly reopen the Strait of Hormuz.
```

---

## forexlive-breaking(rss)

**定位**:投行快讯 | **产出**:ERROR 条 | **耗时**:12922ms

*(拉取失败 — {"error":"RSS request failed: Get \"https://investinglive.com/feed/news\": net/http: TLS handshake timeout"})*

---

## pre-market-briefing(定时)

**定位**:盘前全景(周六门禁) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## guanfu-score(定时)

**定位**:观复读盘 | **产出**:1 条 | **耗时**:39795ms

### 1. 🔭 观复读盘 · 防守倾向 | BTC $63109

```
现在市场整体偏弱，空头占上风，这阶段更适合多看少动、别急着抄底。主要是最近一个月资金一直在往外流，盘面走得也比较疲软，情绪上大家普遍还有点怕。不过有个好迹象是，从估值角度看比特币已经不算贵了，甚至开始有点便宜的苗头，所以也不用太过悲观。需要留心的是，万一哪天价格重新站上关键位置，同时资金面又转回持续流入，那这轮调整可能就结束了，到时候思路得及时跟上来。

── 数据摘要 ──
BTC $63109 | 恐贪 27 | 信号 -1/8 | 置信 低 | MVRV-Z 0.52 | AHR999 0.44
⚠️ 失效: 价格突破 mayer=1.0 + ETF 30d 转正 → 趋势可能已反转

```

---

## market-summary(定时)

**定位**:市场速览 | **产出**:1 条 | **耗时**:32291ms

### 1. 🌐 夜间 市场速览 · 08-02 23:58

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
🟢 Bitcoin USD: 63041.42 (+0.44%)

【汇率数据】
🔹 USD/CNY: 6.7700
🔹 USDT/CNY: 6.7700
🔹 EUR/CNY: 7.7906
🔹 HKD/CNY: 0.8635

更新时间: 2026-08-02 23:58
```

---

## closing-briefing(定时)

**定位**:收盘总览(周六门禁) | **产出**:0 条 | **耗时**:0ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## us-preview(定时)

**定位**:美盘前瞻 | **产出**:1 条 | **耗时**:68647ms

### 1. 🌎 美盘前瞻 · 08-02

```
── 关键标的 ──
🟢 SPY: 747.03 (+0.72%)
🟢 QQQ: 687.99 (+0.65%)
🔴 Gold Aug 26: 4107.00 (-1.29%)
🟢 Crude Oil Se: 84.67 (+1.29%)
🟢 Bitcoin USD: 63040.09 (+0.44%)

── 利率路径 ──
EFFR 3.63%  🔴 09-16  3.81%  ⚪ 10-28  3.88%  ⚪ 12-09  4.00%  ⚪ 01-27  4.05%  ⚪ 03-17  4.13%  ⚪ 04-28  4.17%

── VIX ──
深度 Contango  9D:13.1  VIX:16.0  3M:19.0  6M:21.3  R:0.84

── 美股事件日历 ──
拆股: ASTN 1:4 DAMD 1:10

── 🤖 AI 前瞻研判 ──
盘前风险资产普涨而黄金重挫，核心矛盾在于“不着陆”预期与加息预期急速升温的拉锯。FedWatch显示9月加息概率高达70%，实际利率抬升压制黄金，但SPY、QQQ仍受盈利韧性支撑。若利率预期持续陡峭，长久期科技股估值压力将显性化，谨防风格向能源、金融等价值板块快速轮动。VIX仅15.99且9天波动率13.05，期限结构深度Contango，反映市场极度自满，警惕波动率突然飙升。今晚关注油价能否站稳84上方及美联储官员讲话，若通胀预期进一步强化，加息概率重定价可能触发股指高位回撤，黄金避险功能丧失亦暗示流动性冲击风险。

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

**定位**:新闻聚合早报 | **产出**:1 条 | **耗时**:52458ms

### 1. 🌅 新闻聚合 · 08-03

```
## 资讯摘要
- 特朗普新任 NIH 负责人概述全面改革，如新疫情应对手册、疫苗伤害机构（trump-rss）

2026-08-02 15:59 UTC

**🔗 BTC**
- BTC **63,563**、ETH **1,878**，24h **+0.7%/+0.5%**；资金费率脱离看跌但未全面转多。
- **63,000** 美元堆积 **89 万枚 BTC**，极端分布下市场面临剧烈方向选择。
- Glassnode：期货基差持续低于 2 年期美债，历史类似阶段对应周期底部。
- Polymarket：BTC 8 月涨至 7 万概率 **26%**，6.5 万 **80%**，跌至 6 万 **56%**。
- Saylor 再发 Tracker 暗示买入；Trump Media 疑似转出 **2,628 BTC**（**1.65 亿美元**），信号分化。

**🤖 AI**
- Hugging Face CEO：应「加速前进」，已用开源模型完成防御测试。
- 英伟达 Rubin「减配」系误读：标准版已交付，秋季放量；Ultra 规格未定。
- 国家超算互联网上线 DeepSeek-V4-Flash 正式版 API，一键调用。
- AI 辅助致苹果漏洞报告泛滥：设 **30 天冷静期**限流。
- 美国初创抢做中国模型的平价替代，仍面临融资阻力。

**📈 股票宏观**
- CME：美联储 9 月加息 **25bp** 概率 **67%**；7 月非农是下周关键变量。
- 下周看点：SpaceX 首份财报+解禁，AMD、闪迪、西数放榜。
- 野村上调三星盈利预测；伯克希尔创 **8 个月新高**。
- 字节系用户时长占比 **40.1%**，首超腾讯。
- 提前定价：周一海力士/三星或低开超 **5%**，美股盘前偏暖。
```

---

## news-aggregate-evening(定时)

**定位**:新闻聚合晚报 | **产出**:1 条 | **耗时**:114586ms

### 1. 🌆 新闻聚合 · 08-03

```
2026-08-02 16:00 UTC

**📊 市场数据**
  BTC ETF 净流入: 🔴 -265.4M (累计 79,332M)
  USDT 市值: $183.3B
  USDC 市值: $71.9B

**🔗 BTC**
**23:37** 🔗 分析：BTC在6.3万美元堆积筹码创极端峰值，市场随时可能发生剧烈方向选择 — 8 月 2 日，链上分析师 Murphy 指出，比特币在 63,000 美元单一价位已堆积高达 89 万枚 BTC，呈现极端分布。若不计入 Coinbase 在 83,000 至 84,000 美元区间锁仓的 55 万枚，该价位堆积量可能已…
**23:04** 🔗 Glassnode：比特币期货基差呈现周期底部特征，持续低于2年期美债 — 8 月 2 日，加密分析机构 Glassnode 表示，自今年 2 月以来，3 个月比特币期货基差的收益率一直低于 2 年期美国国债收益率。历史上只有一次类似这么长的时期：2022 年 8 月到 2023 年 1 月，那段时间最终为周期底部…
**20:39** 🔗 Michael Saylor再次发布比特币Tracker信息 — 8 月 2 日，Strategy 创始人 Michael Saylor 再次发布比特币 Tracker 相关信息，配文「Bitcoin Drive engaged.」(比特币驱动已连接。)根据此前规律，Strategy 通常在相关消息发布后…
**15:53** 🔗 比特币本月涨至7万美元概率为26% — 8 月 2 日，预测市场平台 Polymarket 上预测「比特币 8 月涨至 7 万美元」概率为 26%。此外，涨至 6.5 万美元概率为 80%，跌至 6 万美元概率为 56%。
**14:26** 🔗 某鲸鱼从BitMEX合计提取105.055枚BTC，约合666万美元 — 8 月 2 日，据 Onchain Lens 监测，某新建钱包从 BitMEX 提取 100 枚 BTC，价值约 634 万美元。与同一实体关联的另一钱包收到 5.055 枚 BTC，价值约 31.853 万美元。两笔合计提取 105.05…
**12:59** 🔗 加密市场小幅上涨，资金费率显示BTC、ETH均已摆脱看跌区间，但尚未进入普遍看多区间 — 8 月 2 日，据 HTX 行情数据，比特币现报 63,563.03 美元，24 小时涨幅 0.71%；以太坊现报 1,878.80 美元，24 小时涨幅 0.46%。两大加密资产同步回升，永续合约市场情绪有所改善。CoinGlass 最新…
**08:51** 🔗 Trump Media疑似再次减持2628枚BTC，约合1.65亿美元 — 8 月 2 日，据链上分析师余烬监测，时隔两个月，特朗普旗下上市公司 Trump Media（DJT）疑似再次准备减持 BTC。约 6 小时前，该公司将 2628 枚 BTC 转入 Crypto.com，价值约 1.65 亿美元。将代币转入…
**08:32** 🔗 Coldcard攻击者开始转移资金，逾1159枚BTC仍在原始攻击者地址 — 8 月 2 日，据 Onchain Lens 监测，与 Coldcard 随机数生成器漏洞相关的攻击地址集群，已从 870 个遭攻击地址收到 1159.42 枚 BTC，价值约 7271 万美元。攻击者近日向一个新地址转移 0.06 枚 B…

**🤖 AI**
**22:03** 🤖 Hugging Face CEO谈AI安全：加速前进！已使用开源模型完成防御尝试，风险之外不能忽视更大远景 — 据 动察 Beating 监测，Hugging Face CEO Clem 表示，现在不是放慢脚步的时候，而是要加速前进！最近由 AI 驱动的网络攻击让所有人都在讨论 AI 的风险。我们确实应该讨论，但不能忽视更大的图景。如果我们努力去做，…
**21:44** 🤖 详解英伟达Rubin减配传闻：标准版Vera Rubin已向数十家客户交付且秋季放量，Rubin Ultra规格尚未锁定 — 8 月 2 日，分析师 qinbafrank 发文澄清市场关于英伟达 Rubin HBM 减配的讨论，指出实际情况并非已交付的标准版 Vera Rubin NVL72 减配，而是原计划 2027 年下半年推出的升级版 Rubin Ultra…
**16:35** 🤖 中国国家超算互联网上线DeepSeek-V4-Flash正式版API，一键接入即可快速调用 — 据 动察 Beating 监测，中国国家超算互联网正式上线 DeepSeek-V4-Flash 正式版（DeepSeek-V4-Flash-0731）模型 API 调用服务和模型文件。据介绍，DeepSeek-V4-Flash-0731 经…
**13:42** 🤖 AI辅助令漏洞报告激增，苹果限制研究人员提交数量 — 8 月 2 日，据《金融时报》报道，苹果因大量研究人员使用 AI 模型寻找软件漏洞，内部安全团队收到的报告激增，已于 6 月限制研究人员同时提交的漏洞数量，并设置 30 天冷静期。苹果表示，部分 AI 生成报告会虚构安全风险，导致审核系统承…
**12:18** 🤖 美国AI初创公司竞相打造中国廉价AI替代品，但仍面临融资阻力 — 8 月 2 日，据《华尔街日报》报道，随着 Kimi、Qwen、DeepSeek 等中国开放权重模型以较低成本逼近美国顶级模型，硅谷与华盛顿日益担忧中国模型可能长期压缩美国 AI 企业利润。Arcee AI、Reflection AI 和 …
**11:57** 🤖 24岁AI股神Leopold婚礼前先开研讨会：圆桌讨论、分组交流，还谢绝份子钱 — 据动察 Beating 监测，刚把约 160 亿美元公开股票组合中的大部分卖给 Citadel 后，24 岁前 OpenAI 研究员 Leopold Aschenbrenner
…(截断,全文 4001 字符)
```

---

## fed-speeches(rss)

**定位**:美联储官员讲话 | **产出**:0 条 | **耗时**:828ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---

## eia-energy(rss)

**定位**:美国能源官方 | **产出**:0 条 | **耗时**:1506ms

*(无产出 — 304 条件请求/源级过滤/交易日门禁)*

---
