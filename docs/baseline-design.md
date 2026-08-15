# A 股基准数据底座 · 完整方案

**Version**: 1.0 · **Updated**: 2026-05-09 · **Status**: 规划锁定, 待实施

---

## 一、核心目标

建立一套"先过滤、再分类、再对比、再深度分析"的分层数据底座, 并支持动态热度主题标记 —
让 `morning-scan` / `closing-scan` / `intraday-breakout` 等现有源从重复拉取转向秒级查询。

## 二、四层架构

```
┌─────────────────────────────────────────────────────┐
│  Layer 0: 原始全市场 (~5300 只)                      │
│    akshare 拉全量元数据 + K 线                       │
└─────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────┐
│  Layer 1: 过滤层 (filter)                            │
│    ├─ 硬规则 (ST/次新/北交所/小市值/停牌/新股)        │
│    ├─ 基本面宽松过滤 (杀硬伤: ST+退市+资不抵债)       │
│    └─ AI 行业打标 (一年一次, 夕阳行业整体打标)        │
│    → stocks_pool (in_pool=1 共 ~3500-4000 只)       │
└─────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────┐
│  Layer 2: 分类层 (classify)                          │
│    ├─ 申万一级行业 (~30 个)                           │
│    ├─ 申万二级细分 (~100 个)                          │
│    ├─ 概念板块 (~500 个, 多对多)                      │
│    ├─ 指数成份 (沪深 300 / 中证 500)                  │
│    └─ 风格分类 (大/中/小盘 × 价值/成长)               │
│    → sector_map (一只股可能属多个分类)                │
└─────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────┐
│  Layer 3: 对比层 (rank & select)                     │
│    ├─ 同类内 RS 排名 (行业/板块/大盘)                 │
│    ├─ 龙头识别 (市值+成交额+涨幅 综合排名, 每类 Top 5) │
│    ├─ 跨分类共振检测 (成份交集 > 5 只)                │
│    └─ 行业轮动热度                                    │
│    → sector_stats + sector_leaders                    │
└─────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────┐
│  Layer 4: 技术分析层 (tech)                          │
│    ├─ 深度档 (龙头 ~500 只): 全量指标                 │
│    │    MACD/KDJ/RSI/BOLL/ATR + signal_tags           │
│    ├─ 轻量档 (普通池 ~3000 只): MA5/MA20/量比/60日涨幅 │
│    └─ 剪枝 (~1300 只过滤掉, 不计算)                   │
│    → technicals (两档)                                │
└─────────────────────────────────────────────────────┘
                    ↓
┌─────────────────────────────────────────────────────┐
│  Layer 5: 主题热度层 (themes) 🆕                     │
│    ├─ 周级 AI 识别活跃投资主题 (5-15 个)              │
│    ├─ 主题 → 板块 → 龙头个股 三层映射                 │
│    ├─ 日级热度衰减规则 (纯 Go, 无 AI)                 │
│    └─ 新闻触发提升 + 盘面反馈提升                     │
│    → themes + theme_sector_map + theme_leader_map    │
└─────────────────────────────────────────────────────┘
```

## 三、数据源选型

### 3.1 选型结论

- **主源**: AkShare (完全免费, 项目已全栈使用)
- **备源**: Baostock (首次回灌 120 日 K 线加速用)
- **不用**: Tushare Pro (2000 积分撑不住日常), Qlib (bin 文件格式整合成本高)

### 3.2 关键接口使用计划

```python
# 当日全市场快照 (一次 API 拉 5300 行, ~5s)
ak.stock_zh_a_spot_em()                              # OHLCV + 市值 + 换手率

# 当日资金流
ak.stock_individual_fund_flow_rank()                  # 5300 行主力净流入, ~3s

# 板块归属
ak.stock_board_industry_name_em()                     # ~100 个申万一级/二级
ak.stock_board_concept_name_em()                      # ~500 个概念板块
ak.stock_board_concept_cons_em(symbol=concept_name)   # 逐板, ~200 板 × 1s = 3 min

# 基本面 (宽松过滤用)
ak.stock_financial_abstract_ths(symbol=code)          # 单只 ~0.2s, 40 并发 × 5300 ≈ 30s

# 首次历史回灌 (120 日)
baostock.query_history_k_data_plus                    # 一次多年, 比 akshare 快
# 或 akshare fallback: ak.stock_zh_a_hist(symbol, start, end)
```

### 3.3 AI 使用策略

**只 2 处**:
| 场景 | 频率 | Token | 年成本 |
|---|---|---|---|
| 行业打标 (industry_tags 表) | 1/年 | ~3000 | ~0.01 美元 |
| 主题识别 (themes 表) | 1/周 | ~3000 | ~0.5 美元 |
| **总计** | | | **< 1 美元/年** |

**不做**: 个股 AI 评分 / 每日 AI 讨论 / AI 抽样筛查 (成本不可控)

## 四、完整数据表设计 (10 张)

```sql
-- ================ 数据层 (5 张) ================

-- 1. 股票元数据 (5300 行, 周级刷新)
CREATE TABLE stocks (
    code TEXT PRIMARY KEY,
    name TEXT,
    exchange TEXT,                    -- SH/SZ/BJ
    list_date TEXT,
    total_shares REAL,
    float_shares REAL,
    updated_at TEXT
);

-- 2. 日线 K 线 (5300 × 120 日 ≈ 63 万行)
CREATE TABLE daily_ohlcv (
    code TEXT,
    date TEXT,
    open REAL, high REAL, low REAL, close REAL,
    volume REAL, amount REAL,
    change_pct REAL,
    turnover_rate REAL,
    PRIMARY KEY (code, date)
);
CREATE INDEX idx_ohlcv_date ON daily_ohlcv(date);

-- 3. 技术指标 (两档, 每日覆盖)
CREATE TABLE technicals (
    code TEXT PRIMARY KEY,
    date TEXT,
    depth_level TEXT,                 -- "deep"/"light"/null
    -- 基础档 (全档都算)
    ma5 REAL, ma20 REAL,
    vol_ratio_5 REAL,
    return_20d REAL, return_60d REAL,
    -- 深度档独有
    ma10 REAL, ma60 REAL, ma120 REAL,
    macd_dif REAL, macd_dea REAL, macd_hist REAL,
    kdj_k REAL, kdj_d REAL, kdj_j REAL,
    rsi_6 REAL, rsi_14 REAL,
    boll_up REAL, boll_mid REAL, boll_low REAL,
    atr_14 REAL,
    volatility_20d REAL,
    high_60d REAL, low_60d REAL,
    signal_tags TEXT                  -- JSON array: ["MACD金叉", "突破60日高"]
);

-- 4. 分类归属 (多对多, ~16000 行)
CREATE TABLE sector_map (
    code TEXT,
    sector_name TEXT,
    sector_type TEXT,                 -- "industry_l1"/"industry_l2"/"concept"/"index"
    is_leader INTEGER DEFAULT 0,      -- 是否同类 Top 5
    leader_score REAL,
    PRIMARY KEY (code, sector_name)
);

-- 5. 板块聚合统计 (每日 ~500 行)
CREATE TABLE sector_stats (
    sector_name TEXT,
    date TEXT,
    sector_type TEXT,
    stock_count INT,
    avg_return_1d REAL,
    avg_return_5d REAL,
    avg_return_20d REAL,
    rs_rank_1d INT,
    rs_rank_20d INT,
    capital_flow REAL,
    leader_codes TEXT,                -- JSON: Top 5
    PRIMARY KEY (sector_name, date)
);
CREATE INDEX idx_sector_stats_date ON sector_stats(date);

-- ================ 过滤层 (2 张) ================

-- 6. 过滤池 (Layer 1 结果)
CREATE TABLE stocks_pool (
    code TEXT PRIMARY KEY,
    in_pool INTEGER,
    filter_layer TEXT,                -- "hard"/"fundamental"/"industry-ai"/"passed"
    filter_reason TEXT,
    industry TEXT,
    sub_industry TEXT,
    industry_tier TEXT,               -- 蓝筹/成长/周期/稳健/夕阳
    policy_risk TEXT,                 -- 高/中/低
    last_checked TEXT
);

-- 7. 行业 AI 标签 (年刷)
CREATE TABLE industry_tags (
    industry TEXT PRIMARY KEY,
    tier TEXT,
    policy_risk TEXT,
    structural_outlook TEXT,          -- 向上/平稳/向下
    ai_note TEXT,
    ai_updated_at TEXT
);

-- ================ 主题层 (3 张) 🆕 ================

-- 8. 活跃投资主题 (周刷)
CREATE TABLE themes (
    theme_id TEXT PRIMARY KEY,        -- "ai-compute-2026"
    theme_name TEXT,                  -- "AI 算力"
    category TEXT,                    -- "科技"/"政策"/"事件"
    hotness_score REAL,               -- 0-100 综合热度
    ai_summary TEXT,                  -- AI 一句话摘要
    trigger_sources TEXT,             -- JSON: 触发新闻源
    active_since TEXT,
    last_updated TEXT,
    status TEXT                       -- "hot"/"cooling"/"dormant"
);

-- 9. 主题 → 板块映射
CREATE TABLE theme_sector_map (
    theme_id TEXT,
    sector_name TEXT,
    relevance REAL,                   -- 0-1 AI 判断
    PRIMARY KEY (theme_id, sector_name)
);

-- 10. 主题 → 龙头个股映射
CREATE TABLE theme_leader_map (
    theme_id TEXT,
    code TEXT,
    name TEXT,
    reason TEXT,
    PRIMARY KEY (theme_id, code)
);
```

## 五、执行节奏

### 5.1 首次初始化 (~25-30 min, 全异步)

```
Phase 1 元数据 akshare          ~5s
Phase 2 K 线回灌 baostock 批量  ~8 min (120 日 × 5300)
Phase 3 基本面过滤 akshare      ~90s
Phase 4 分类归属 akshare        ~3 min
Phase 5 龙头识别 本地 SQL       ~30s
Phase 6 技术指标 本地 CPU       ~2 min
Phase 7 板块聚合 本地 SQL       ~20s
Phase 8 AI 行业打标 LLM         ~3s
Phase 9 AI 主题识别 LLM         ~30s
```

### 5.2 每日 16:30 (~5 min)

```
Phase 1 元数据 diff             ~3s
Phase 2 当日快照 spot_em        ~5s
Phase 3 过滤池 diff             ~30s
Phase 4 分类归属刷新            ~3 min
Phase 5 龙头识别                ~30s
Phase 6 技术指标增量            ~2 min
Phase 7 板块聚合                ~20s
Phase 10 主题热度衰减/匹配      ~1s (纯 Go 规则)
```

### 5.3 每周日 07:00 (~30s)

```
AI 主题识别刷新 + 覆盖 themes 表
```

### 5.4 每年 1 次 (~3s)

```
AI 行业打标刷新
```

## 六、主题热度层设计 (Layer 5 细节)

### 6.1 AI 主题识别 (周刷)

输入:
- 过去 7 天新闻源汇总 (trump-rss / bwe-tradfi / kobeissi / kitco / finnhub / morning-brief)
- 板块 RS 变化 Top 20 (从 sector_stats 查)
- 资金流向 Top 10 板块

Prompt 框架:
```
根据以下新闻和盘面, 列出当前 5-10 个活跃投资主题。
每个主题要求: 1 句话摘要 + 相关申万行业 + 相关概念板块 + 代表龙头股

输出 JSON:
{
  "themes": [{
    "name": "AI 算力",
    "summary": "大模型训练需求爆发, GPU/HBM/服务器需求激增",
    "sectors": [{"name": "半导体", "relevance": 0.9}, ...],
    "leaders": ["中际旭创", "新易盛", "工业富联"]
  }]
}
```

### 6.2 日级热度衰减规则 (纯 Go)

```go
// 每日 16:40 (baseline 后) 运行
func decayThemes(ctx context.Context) {
    for _, theme := range activeThemes {
        // 1. 自然衰减
        theme.HotnessScore *= 0.95

        // 2. 新闻触发提升
        todayNewsCount := countNewsMatchingTheme(theme)
        theme.HotnessScore += float64(todayNewsCount) * 10

        // 3. 盘面反馈提升
        avgRS := avgSectorRSToday(theme.Sectors)
        if avgRS > 5 {
            theme.HotnessScore += 15
        }

        // 4. 状态转移
        switch {
        case theme.HotnessScore >= 60:
            theme.Status = "hot"
        case theme.HotnessScore >= 20:
            theme.Status = "cooling"
        default:
            theme.Status = "dormant"
        }
    }
}
```

### 6.3 下游消费

| 场景 | 用法 |
|---|---|
| `morning-scan` 打分 | 候选股所属板块在 themes.hotness > 60 → +15 分 |
| `theme-daily` 新源 | 09:15 推 Top 5 主题 + 龙头表现 |
| API `/api/themes` | 前端 Reports UI "主题" tab |
| API `/api/themes/search?q=AI` | 回答 "AI 时代受益标的" 类问题 |
| evening-report AI 引用 | "今日 AI 算力主题回调 -2%, ..." |

## 七、文件清单

### 7.1 Python 新增 (6 个 / ~1350 行)

| 文件 | 行数 | 职责 |
|---|---|---|
| `scripts/build_baseline.py` | ~500 | 主流程 Phase 1-10 |
| `scripts/tech_indicators.py` | ~200 | 技术指标库 (从 stock_picker.py 抽离) |
| `scripts/baostock_kline_batch.py` | ~150 | 首次 120 日回灌专用 |
| `scripts/industry_tagger.py` | ~150 | AI 行业打标 (年刷) |
| `scripts/theme_detector.py` | ~250 | AI 主题识别 (周刷) |
| `scripts/baseline_query.py` | ~100 | CLI 调试工具 |

### 7.2 Go 新增 (6 个 / ~700 行)

| 文件 | 职责 |
|---|---|
| `internal/baseline/schema.go` | 10 张表 DDL + InitSchema |
| `internal/baseline/query.go` | GetStock/GetSector/GetTheme/RankByRS/GetLeaders |
| `internal/baseline/bootstrap.go` | 启动自检 + 异步 spawn |
| `internal/baseline/theme_decay.go` | 日级热度衰减引擎 (纯 Go) |
| `internal/api/analyze_handlers.go` | /api/analyze/:code + /api/sectors/rank + /api/themes |
| `internal/source/theme_daily.go` | 09:15 主题推送源 |

### 7.3 配置 / 文档

- `config.yaml.example`: 新增 baseline-build script 源 + theme-daily 源
- `config/sector_blacklist.yaml`: 黑名单模板
- `docs/baseline-design.md`: 本文档

## 八、下游消费接入

| 消费方 | 原来做法 | 改造后 |
|---|---|---|
| `morning-scan` 候选 | 调 hot_sector + stock_picker 脚本 | 查 `sector_map WHERE is_leader=1` + `technicals WHERE depth_level='deep'` + 主题加成 |
| `closing-scan` | 自己算涨停股板块归属 | 查 `sector_map` |
| `intraday-breakout` | 全市场遍历 5 分钟 | 查 `technicals WHERE signal_tags LIKE '%突破%'` 50ms |
| `evening-report` 板块段 | 调 hot_sector_push.py | 查 `sector_stats WHERE date=today ORDER BY rs_rank_1d` |
| **新** `theme-daily` | — | 09:15 推 Top 5 主题 + 龙头表现 |
| **新** `sector-rotation` | — | 周日推 baseline 板块 RS 周变化 |
| **新** API | — | `/api/analyze/:code`、`/api/sectors/rank`、`/api/themes` |

## 九、价值量化

| 指标 | 当前 | 落地后 |
|---|---|---|
| intraday-breakout 耗时 | 5 分钟 | 50 毫秒 |
| morning-scan 候选准确度 | 打分 4 维 | 打分 7 维 (加 RS + 龙头标记 + 主题加成) |
| 全市场 RS 排名 | 没有 | 日更 |
| 单标的技术分析 API | 没有 | 秒级 |
| 动态主题池 | 没有 | 周刷 AI + 日级衰减 |
| 回答 "AI 时代受益股" 等问题 | 人工 | `/api/themes/search?q=AI` |
| 夕阳行业污染候选池 | 有 | AI 打标自动剔除 |
| AI 成本 | 0 | < 1 美元/年 |

## 十、实施路线

### Week 1 - 底座 (4-5 天)

- **Day 1**: schema.go + build_baseline.py Phase 1-3 (元数据 + K 线 + 过滤池)
- **Day 2**: Phase 4-5 (分类 + 龙头识别)
- **Day 3**: Phase 6-7 (技术指标 + 板块聚合) + industry_tagger.py
- **Day 4**: Go bootstrap + query.go + 手动跑通验证
- **Day 5**: theme_detector.py + theme_decay.go (主题层)

### Week 2 - 消费层 (3-4 天)

- **Day 6**: intraday-breakout 改造 (性能验证)
- **Day 7**: morning-scan 接入 (RS + 龙头 + 主题加成)
- **Day 8**: 新增 sector-rotation 源 + theme-daily 源
- **Day 9**: /api/analyze/:code API + Reports UI 加 "主题" tab

### 工作量合计

- Python: ~1350 行
- Go: ~700 行
- **总计 ~2050 行新增, 7-9 天工作量**

## 十一、风险与应对

| 风险 | 应对 |
|---|---|
| akshare 某接口 503 | 单只 retry 3 次 + 降级继续跑 |
| 首次 120 日回灌失败 | Baostock 备胎, 自动 fallback |
| AI 主题识别 token 超限 | 限制输入新闻条数 + 1 周只 1 次 |
| 主题池过时 | 日级衰减 hotness *= 0.95, 跌破 20 自动 dormant |
| 黑名单 yaml 与 AI 标签冲突 | 黑名单优先级 > AI |
| 小市值阈值误伤优质次新 | 保留 raw pool (stocks 表全量) + 用户可配阈值 |
| SQLite 并发写锁 | build_baseline 单实例 + sync.Once |

## 十二、部署依赖

- **新增 Python 依赖**: `baostock` (首次回灌用; `pip install baostock`)
- **已有**: akshare / pandas / sqlite3
- **Go 无新依赖**: 复用 `modernc.org/sqlite`

## 十三、决策锁定清单

| # | 决策项 | 选定 |
|---|---|---|
| 1 | 数据源 | akshare 主 + baostock 备 |
| 2 | 过滤严格度 | 宽松 (杀硬伤) |
| 3 | Layer 3 AI 深度 | 仅行业打标 |
| 4 | 主题热度层 | 完整版 (周 AI + 日规则衰减) |
| 5 | AI 预算 | < 1 美元/年 |
| 6 | 存储 | SQLite 单文件 `data/baseline.db` |
| 7 | 启动自检 | 全自动异步 |
| 8 | 实施范围 | Week 1 底座 + Week 2 消费层 (7-9 天) |

---

## 附录 · 关键 SQL 查询示例

```sql
-- 今日最强板块 Top 10
SELECT sector_name, avg_return_1d, rs_rank_1d, leader_codes
FROM sector_stats
WHERE date = date('now') AND sector_type = 'industry_l1'
ORDER BY rs_rank_1d ASC LIMIT 10;

-- 某股综合画像
SELECT s.*, sp.industry_tier, sp.policy_risk,
       t.signal_tags, t.macd_hist, t.rsi_14,
       (SELECT GROUP_CONCAT(sector_name) FROM sector_map WHERE code = s.code) as sectors
FROM stocks s
LEFT JOIN stocks_pool sp USING (code)
LEFT JOIN technicals t USING (code)
WHERE s.code = '300750';

-- 某主题的受益标的
SELECT tlm.code, tlm.name, tlm.reason,
       t.return_20d, t.signal_tags
FROM theme_leader_map tlm
JOIN themes thm ON tlm.theme_id = thm.theme_id
LEFT JOIN technicals t ON tlm.code = t.code
WHERE thm.theme_name = 'AI 算力' AND thm.status = 'hot'
ORDER BY t.return_20d DESC;

-- 板块共振检测 (两板块成份交集 > 5)
SELECT a.sector_name || ' × ' || b.sector_name as pair,
       COUNT(*) as overlap_count
FROM sector_map a
JOIN sector_map b ON a.code = b.code
WHERE a.sector_name < b.sector_name
GROUP BY a.sector_name, b.sector_name
HAVING overlap_count > 5
ORDER BY overlap_count DESC LIMIT 20;
```
