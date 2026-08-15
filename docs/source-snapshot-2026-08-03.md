# 源实况快照 · 2026-08-03 02:20

> 通过 `POST /api/sources/:name/fetch` 主动拉取全部 20 个源(同步执行 Fetch,不推送)。
> 周日窗口:定时源正常实跑;futu-earnings-calendar 首拉遇 OpenD 瞬时网络抖动失败,手动重试成功(134 家)。
> 本次为富途双源(futu-earnings-calendar / futu-econ-calendar)加入后的首次全源快照。

## 汇总

| 源 | 类型 | 定位 | 产出 | 耗时 |
|---|---|---|---|---|
| trump-rss | rss | 政治/政策 | 1 | 3s |
| bwe-tradfi | rss | 突发快讯 | 0 | 1s |
| kobeissi-letter | rss | 量化分析 | 2 | 1s |
| fed-speeches | rss | 美联储官员讲话 | 0 | 1s |
| fed-press | rss | 美联储官方 | 0 | 0s |
| bloomberg-markets | rss | 美股市场 | 0 | 0s |
| bloomberg-economics | rss | 宏观 | 1 | 1s |
| bloomberg-politics | rss | 政策/地缘 | 2 | 0s |
| forexlive-breaking | rss | 投行快讯 | 0 | 1s |
| eia-energy | rss | 美国能源官方 | 0 | 1s |
| guanfu-score | 定时 | 观复读盘 | 1 | 10s |
| market-summary | 定时 | 市场速览 | 1 | 46s |
| pre-market-briefing | 定时 | 盘前全景 | 1 | 62s |
| closing-briefing | 定时 | 收盘总览 | 1 | 192s |
| us-preview | 定时 | 美盘前瞻 | 1 | 115s |
| us-macro-report | 定时 | 美国宏观 | 1 | 136s |
| futu-earnings-calendar | 定时 | 富途财报日历 | 1 | 4s |
| futu-econ-calendar | 定时 | 富途经济事件日历 | 1 | 5s |
| news-aggregate-morning | 定时 | 新闻聚合早报 | 1 | 12s |
| news-aggregate-evening | 定时 | 新闻聚合晚报 | 1 | 79s |

---

## trump-rss(rss)

**定位**:政治/政策 | **产出**:1 条 | **耗时**:3s

### 1. https://www.energy.gov/articles/secretary-wright-directs-sable-offshore-restore-santa-ynez-unit-and-...

```
https://www. energy.gov/articles/secretary- wright-directs-sable-offshore-restore-santa-ynez-unit-and-pipeline

── AI 分析 ──
Post from Truth Social 1h ago Aug 2, 2026 at 4:22 PM https://www. energy.gov/articles/secretary- wright-directs-sable-offshore-restore-santa-ynez-unit-and-pipeline Analysis Dan…
```

来源: trump-rss · https://trump.fm/post/ts_117026832680116899/analysis

---

## bwe-tradfi(rss)

**定位**:突发快讯 | **产出**:0 条 | **耗时**:1s

*(无产出 — 304 条件请求/源级过滤/无新内容)*

---

## kobeissi-letter(rss)

**定位**:量化分析 | **产出**:2 条 | **耗时**:1s

### 1. BREAKING: Global household net worth surged +7.3% YoY, to a record $570 trillion in 2025. This was primarily driven by equities which accounted for 57% of the increase, while real estate contributed just 15%. Since 2000, this figure has risen...

```
BREAKING: Global household net worth surged +7.3% YoY, to a record $570 trillion in 2025. This was primarily driven by equities which accounted for 57% of the increase, while real estate contributed just 15%. Since 2000, this figure has risen +338%, with an average annual increase of +5.9%. As a res…
```

来源: kobeissi-letter · https://t.me/thekobeissiletter/7351

### 2. AI-exposed companies are delivering unprecedented earnings. S&P 500 companies are beating earnings estimates by an average of +27% so far in Q2 2026, on track for the strongest quarter in decades. Nasdaq 100 firms are exceeding expectations by...

```
AI-exposed companies are delivering unprecedented earnings. S&P 500 companies are beating earnings estimates by an average of +27% so far in Q2 2026, on track for the strongest quarter in decades. Nasdaq 100 firms are exceeding expectations by more than double that margin, at +55%. The Bloomberg AI …
```

来源: kobeissi-letter · https://t.me/thekobeissiletter/7352

---

## fed-speeches(rss)

**定位**:美联储官员讲话 | **产出**:0 条 | **耗时**:1s

*(无产出 — 304 条件请求/源级过滤/无新内容)*

---

## fed-press(rss)

**定位**:美联储官方 | **产出**:0 条 | **耗时**:0s

*(无产出 — 304 条件请求/源级过滤/无新内容)*

---

## bloomberg-markets(rss)

**定位**:美股市场 | **产出**:0 条 | **耗时**:0s

*(无产出 — 304 条件请求/源级过滤/无新内容)*

---

## bloomberg-economics(rss)

**定位**:宏观 | **产出**:1 条 | **耗时**:1s

### 1. Australia’s Housing Market Worsens With Falls Getting Steeper

```
Australia’s housing market decline worsened, with prices declining in June and July by the most since December 2022 as rising interest rates and tax changes hit demand for homes.
```

来源: bloomberg-economics · https://www.bloomberg.com/news/articles/2026-08-02/australia-s-housing-market-worsens-with-falls-getting-steeper

---

## bloomberg-politics(rss)

**定位**:政策/地缘 | **产出**:2 条 | **耗时**:0s

### 1. Chicago Mayor’s Race Heats Up as Democrats Ready to Oust Johnson

```
The field of candidates running to replace Chicago Mayor Brandon Johnson next year is growing, setting the stage for a contentious election that will see progressive and moderate Democrats battle to lead the third-largest city in the US.
```

来源: bloomberg-politics · https://www.bloomberg.com/news/articles/2026-08-02/chicago-mayor-s-race-heats-up-as-democrats-ready-to-oust-johnson

### 2. Republican Senator Calls on Ex-Son-in-Law to Resign House Seat

```
US Republican Senator Bernie Moreno called on his ex-son-in-law, Representative Max Miller, to resign his US House seat amid swirling allegations of physical violence against Miller’s former wife and daughter.
```

来源: bloomberg-politics · https://www.bloomberg.com/news/articles/2026-08-02/republican-senator-calls-on-ex-son-in-law-to-resign-house-seat

---

## forexlive-breaking(rss)

**定位**:投行快讯 | **产出**:0 条 | **耗时**:1s

*(无产出 — 304 条件请求/源级过滤/无新内容)*

---

## eia-energy(rss)

**定位**:美国能源官方 | **产出**:0 条 | **耗时**:1s

*(无产出 — 304 条件请求/源级过滤/无新内容)*

---

## guanfu-score(定时)

**定位**:观复读盘 | **产出**:1 条 | **耗时**:10s

### 1. 🔭 观复读盘 · 防守倾向 | BTC $63109

```
这会儿市场偏冷，大伙儿都挺谨慎的，适合多看少动、防守为主。盘面上短期资金还在往外流，技术形态也偏弱，所以价格一时半会儿缺冲劲，更像是慢慢找底的过程。不过也别太悲观，从长期估值角度看，比特币不算贵，甚至有点便宜，这位置再往下空间可能有限。需要留意的是，万一买盘突然回来了，比如大资金重新持续流入，那这波防守逻辑就站不住了，得及时调整想法。总之现在别急着抄底，耐心等信号明朗。

── 数据摘要 ──
BTC $63109 | 恐贪 27 | 信号 -1/8 | 置信 低 | MVRV-Z 0.52 | AHR999 0.44
⚠️ 失效: 价格突破 mayer=1.0 + ETF 30d 转正 → …
```

---

## market-summary(定时)

**定位**:市场速览 | **产出**:1 条 | **耗时**:46s

### 1. 🌐 夜间 市场速览 · 08-03 02:02

```
【行情数据】
🟢 SPY: 747.03 (+0.72%)
🟢 QQQ: 687.99 (+0.65%)
🔴 Gold Dec 26: 4107.00 (-1.29%)
🔴 Silver Sep 2: 57.79 (-2.09%)
🟢 HANG SENG IN: 25884.43 (+0.10%)
🟢 HSTECH ETF: 4.82 (+0.42%)
🔴 ^VIX: 15.99 (-6.88%)
🟢 Crude Oil Se: 84.67 (+1.29%)
🔴 IBIT: 35.64 (-2.89%)
🟢 Bitcoin USD: 63252.05 (+0.78%)

【汇率数据】
🔹 US…
```

---

## pre-market-briefing(定时)

**定位**:盘前全景 | **产出**:1 条 | **耗时**:62s

### 1. 🌅 盘前全景 · 08-03

```
── 隔夜行情 ──
🟢 SPY: 747.03 (+0.72%)
🟢 QQQ: 687.99 (+0.65%)
🔴 ^VIX: 15.99 (-6.88%)
🟢 HANG SENG IN: 25884.43 (+0.10%)
🟢 HSTECH ETF: 4.82 (+0.42%)
🟢 Crude Oil Se: 84.67 (+1.29%)
🔴 Gold Dec 26: 4107.00 (-1.29%)
🔴 Silver Sep 2: 57.79 (-2.09%)
🟢 Bitcoin USD: 63255.96 (+0.79%)
🟢 平安银行: 11.63 (+0.17%)
🟢 深证成指: …
```

---

## closing-briefing(定时)

**定位**:收盘总览 | **产出**:1 条 | **耗时**:192s

### 1. 📊 收盘总览 · 08-03

```
── 市场总览 ──
情绪: 乐观 (70.0)

── 资金面 ──
两融余额: 20352亿 (+0.25%)

── 打板预测 ──
市场温度: 涨停 99 / 跌停 0 / 炸板率 52% / 主线 广告营销、IT服务Ⅱ
• [000820] 神雾节能 2板 | 评分 33 | 置信度 低（样本不足，仅... | KNN次日开盘中位数 +5.3%、胜率 85%（20样本） | 风险 断板环境 (1 板占比 90%), 连板承压
• [003001] 中岩大地 1板 | 评分 29 | 置信度 高 | 分桶次日开盘中位数 +0.9%、胜率 56%（39样本）
• [603120] 肯特催…
```

---

## us-preview(定时)

**定位**:美盘前瞻 | **产出**:1 条 | **耗时**:115s

### 1. 🌎 美盘前瞻 · 08-03

```
── 关键标的 ──
🟢 SPY: 747.03 (+0.72%)
🟢 QQQ: 687.99 (+0.65%)
🔴 Gold Dec 26: 4107.00 (-1.29%)
🟢 Crude Oil Se: 84.67 (+1.29%)
🟢 Bitcoin USD: 63219.35 (+0.73%)

── 利率路径 ──
EFFR 3.63%  🔴 09-16  3.81%  ⚪ 10-28  3.88%  ⚪ 12-09  4.00%  ⚪ 01-27  4.05%  ⚪ 03-17  4.13%  ⚪ 04-28  4.17%

── VIX ──
深度 Contango  9D:1…
```

---

## us-macro-report(定时)

**定位**:美国宏观 | **产出**:1 条 | **耗时**:136s

### 1. 🌎 美国宏观 · 盘前

```
📅 2026-08-03 02:11

── FRED 关键指标 ──
【利率】联邦基金利率 3.63% (较前值 +0.00%) | SOFR 3.65% (较前值 +0.00%) | 2年期国债 4.23% (较前值 +0.24%) | 10年期国债 4.68% (较前值 +0.21%) | 30年期国债 5.21% (较前值 +0.19%)
【流动性】M2 货币供应 23.16万亿美元 (较前值 +0.43%) | Fed 总资产 6.74万亿美元 (较前值 -0.14%) | RRP 逆回购 0.002万亿美元 (较前值 +99.91%) | 银行储备金 0.00万亿美元 (较前值 -…
```

---

## futu-earnings-calendar(定时)

**定位**:富途财报日历 | **产出**:1 条 | **耗时**:4s

### 1. 📅 财报日历 08-03

```
🇺🇸 美股 — 共 134 家
  ABTC(American Bitcoin) · 08-03 · 预期 EPS 0.00
  ADEA(Adeia) · 08-03 · 预期 EPS 0.13
  ADTN(亚川) · 08-03
  ADUS(爱德斯) · 08-03 · 预期 EPS 1.52
  AEIS(先进能源工业) · 08-03 · 预期 EPS 1.79
  AESI(Atlas Energy Solutions) · 08-03 · 预期 EPS -0.17
  ALG(阿拉莫) · 08-03 · 预期 EPS 2.74
  ALSN(艾里逊变速箱) · 08-03 ·…
```

---

## futu-econ-calendar(定时)

**定位**:富途经济事件日历 | **产出**:1 条 | **耗时**:5s

### 1. 🌏 明日经济事件 08-04

```
🇺🇸 美国 — 共 3 条
  20:30 | 美国6月贸易帐(亿美元) | ⭐⭐⭐ | 预期 -710.00 · 前值 -776.00
  22:00 | 美国6月工厂订单月率 | ⭐⭐⭐ | 前值 -1.30
  22:00 | 美国6月JOLTs职位空缺(万人) | ⭐⭐⭐ | 前值 759.40
```

---

## news-aggregate-morning(定时)

**定位**:新闻聚合早报 | **产出**:1 条 | **耗时**:12s

### 1. 🌅 新闻聚合 · 08-03

```
## 资讯摘要
- 外国投资者正大举重返韩国股市（kobeissi-letter）
- 突发：2025年全球家庭净资产同比激增7.3%，创下570万亿美元历史新高（kobeissi-letter）
- 特朗普新任 NIH 负责人概述全面改革，如新疫情应对手册、疫苗伤害机构（trump-rss）

2026-08-02 18:11 UTC

**🔗 BTC**
- 链上数据显示，BTC 在 **6.3005 万美元** 单一价位堆积高达 **89 万枚**，极端筹码分布预示剧烈变盘临近。
- Glassnode 指 BTC 期货基差持续低于 2 年期美债，仅 2022 年底出现过类似形态，当时处…
```

---

## news-aggregate-evening(定时)

**定位**:新闻聚合晚报 | **产出**:1 条 | **耗时**:79s

### 1. 🌆 新闻聚合 · 08-03

```
2026-08-02 18:11 UTC

**🔗 BTC**
- BTC在$63,000堆积**89万枚**，筹码分布极端，市场面临剧烈方向选择。
- 3个月期货基差持续低于2年期美债，Glassnode指为周期底部特征。
- Polymarket预测8月升至$70,000概率**26%**，跌至$60,000概率**56%**。
- Trump Media疑似将**2,628枚BTC**（约$1.65亿）转入Crypto.com，准备再度减持。
- BTC ETF单日净流出**$2.654亿**，USDT市值$183.3B，USDC市值$71.9B。

**🤖 AI**
- 中国国家超算互…
```

---

