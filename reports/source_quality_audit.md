# News Platform 源质量审计
**周期**: 2026-07-29 ~ 2026-07-31 (50h) | **数据**: 45,724 次抓取, ~44,000 条原始条目

---

## 核心发现

**26 个源中，10 个 RSS 源产出了 ~44,000 条原始条目，但 AI 过滤器放行 0 条到实时推送。** 唯一送达用户的 Digest 投递（盘前全景、美盘前瞻）来自 market-briefing 类型源，不走 RSS 管线。

这意味着 RSS 管线目前的作用是：**为 Digest briefing 提供素材池**（bloomberg/energy 源的去重标为 `__digest__`），而非独立推送。

---

## 逐源评估

### 🔴 建议关闭 (ROI 极低)

| 源 | 抓取/50h | 条目 | AI拦截 | 均分 | 理由 |
|---|---|---|---|---|---|
| **hellenic-shipping** | 322 | 3,184 | 67 | 2.6 | 船舶拍卖/港口周报/废船回收，零交易信号。已降频到1800s，仍无产出 |
| **trump-rss** | 1,932 | 11,696 | 22 | 1.6 | 99.8% 是政治集会花絮/竞选言论。已加 `exclude_keywords`，仍是最低信号源 |

### 🟡 建议降频或缩小范围

| 源 | 抓取/50h | 条目 | AI拦截 | 均分 | 理由 |
|---|---|---|---|---|---|
| **kobeissi-letter** | 1,047 | 4,470 | 20 | 3.9 | 多为市场反应描述（"SPX dropped 2%"），无增量信息。与 bwe-tradfi 覆盖重叠 |
| **bwe-tradfi** | 2,067 | 11,722 | 30 | 3.5 | 量最大但信号密度最低（0.3% 触发 AI），大量机构八卦/人事变动。保留但加 `require_keywords` 过滤 |
| **kitco-precious** | 505 | 2,649 | 24 | 4.0 | 市场评论/机构观点为主，即时交易信号少。已有 `require_keywords`，可收紧关键词列表 |

### 🟢 保留（为 Digest 提供素材）

| 源 | 抓取/50h | 条目 | AI拦截 | 均分 | 理由 |
|---|---|---|---|---|---|
| **bloomberg-markets** | 397 | 3,347 | 78 | 5.3 | 并购/私募/CDS 等，AI 评分接近阈值（大量 7 分），Digest 去重 6 次 |
| **bloomberg-economics** | 397 | 2,592 | 22 | 5.0 | 宏观数据/央行/经济指标，Digest 去重 10 次。核心 Digest 素材 |
| **forexlive-breaking** | 856 | 4,696 | 17 | 5.7 | 突发快讯/数据 vs 预期，信号质量中等但符合"快讯"定位 |
| **energy-connects** | 371 | 593 | 5 | 6.0 | 最低拦截率 + 最高均分，能源地缘直接可交易。但产量低（1.6条/次） |
| **fed-press** | 48 | 48 | 0 | — | 低频 + `require_keywords` 预筛，Fed 官方消息不可替代 |

### ⚪ 事件触发型（无需评估）

blockbeats-morning/evening, a-morning-scan, a-intraday-breakout, a-limit-up-ladder, a-block-trade, guanfu-score, market-summary, closing-briefing, pre-market-briefing, us-preview, us-macro-report, weekly-calendar, edgar-13f, superinvestors, closing-scan-backfill — 这些是定时/条件触发源，非持续轮询，两日内无触发属正常。

---

## AI 过滤器阈值分析

| 分数 | 含义 | 示例 |
|---|---|---|
| 0-3 | 纯噪音 | "船舶拍卖公告" "竞选集会花絮" "政治言论" |
| 4-5 | 弱信号 | "行业合作" "市场评论" "机构观点" |
| 6-7 | 灰色地带 | "利率预期转变" "大额风险转移" "地缘推升油价" |
| 8+ | 通过阈值 | "胡塞袭击沙特原油设施" "消费者信心低于预期" |

**问题**：score 7 的条目大量堆积（AI 拦截但理由合理），说明当前阈值（~7.5）过于保守。许多 7 分条目（"利率预期影响定价"、"CDS 反映融资成本"）对交易者有参考价值，但被一刀切。

---

## 建议操作

| 优先级 | 操作 | 预期效果 |
|---|---|---|
| 🔴 P0 | **关闭 hellenic-shipping** | 消除 3,184 条目/50h 的纯噪音，省 ~10% AI 调用 |
| 🔴 P0 | **暂停 trump-rss** 或改为 `delivery_mode: silent` | 消除 11,696 条目/50h，省 ~8% AI 调用。`exclude_keywords` 已经在拦截 99.8%，剩下 0.2% 也全是政治 |
| 🟡 P1 | **bwe-tradfi 加 `require_keywords`** | 收紧到真正 TradFi 信号（"rate" "Fed" "inflation" "position" "flow"），11,722→~500 条目 |
| 🟡 P1 | **kitco-precious 收紧关键词** | 当前 `require_keywords` 过于宽泛（"price" "rally" "drop"），命中率太高 |
| 🟢 P2 | **降 AI 阈值从 7.5→6.5 观察一周** | 让更多 6-7 分条目通过，观察用户反馈；若噪音增加则回退 |
| 🟢 P2 | **kobeissi-letter 加 `delivery_mode: silent`** | 与 bwe-tradfi 覆盖重叠，改为仅进 Digest 素材池 |

### 预期效果

执行 P0+P1 后：每 50h 减少 ~15,000 次 RSS 条目处理 + ~100 次 AI 调用，零信号损失。
