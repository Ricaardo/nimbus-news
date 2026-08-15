# 数据源总览

news 平台的全部数据来源、接口/RSS 链接、所需密钥与外部依赖。
按 `config.platform.vps.yaml` 实况核对（2026-08-16）。
配置模板见 `config.platform.yaml.example`;真实配置 `config.platform.vps.yaml`(gitignored,含密钥)。

## 一、采集源(19 个,全部启用)

### RSS 源(10 个,公开,无需密钥)

| 源名 | interval | 链接 |
|---|---|---|
| trump-rss | 300s | https://trump.fm/rss/analysis.xml |
| bwe-tradfi | 60s | https://ch2rss.fflow.net/BWEtradfi |
| kobeissi-letter | 300s | https://ch2rss.fflow.net/TheKobeissiLetter |
| fed-speeches | 900s | https://www.federalreserve.gov/feeds/speeches.xml |
| fed-press | 900s | https://www.federalreserve.gov/feeds/press_all.xml |
| bloomberg-markets | 300s | https://www.bloomberg.com/feeds/markets/news.rss |
| bloomberg-economics | 300s | https://www.bloomberg.com/feeds/economics/news.rss |
| bloomberg-politics | 300s | https://www.bloomberg.com/feeds/politics/news.rss |
| forexlive-breaking | 120s | https://investinglive.com/feed/news |
| eia-energy | 600s | https://www.eia.gov/rss/todayinenergy.xml |

> ch2rss.fflow.net 是第三方 RSS 代理(上游偶发超时,已有重试);bloomberg RSS 为公开 feed。
> 每源有关键词过滤 + 打分区(AI 分类),非命中不推送。

### 简报/聚合源(market-briefing,5 实例 / 9 个时段)

| 源名 | 时段 | 组成(内部脚本 + 数据) |
|---|---|---|
| news-aggregate-morning | 09:15 | BlockBeats 早报(`blockbeats_daily_reports.py --mode morning`)+ 平台 digest 资讯摘要 |
| news-aggregate-evening | 19:30 | BlockBeats 晚报(`--mode evening`)+ digest 资讯摘要 |
| pre-market-briefing | 09:15 11:35 13:05 15:00 | 行情 13 标(SPY/QQQ/VIX/HSI/CL/GC/BTC/A股大盘)+ 隔夜异动触发 + 日历 4 脚本(财报/IPO/港股IPO/解禁)+ 金银比 + 外汇 + AI 叙事;时段感知(开/午/收/收盘后) |
| closing-briefing | 16:10 | A股收盘复盘:`market_sentiment`(情绪/涨跌家数)+ `hot_sector_push`(热点板块)+ `capital_flow`(资金流/两融)+ `lhb`(龙虎榜)+ `closing_scan`(KNN 扫描)+ AI 复盘 |
| us-preview | 21:10 | `fedwatch`(FedWatch 利率概率)+ `vix_term`(VIX 期限结构)+ 5 标行情 + Nasdaq 美股事件日历 + AI 叙事 |

### 报告/日历源(4 个)

| 源名 | 类型 | 时间 | 说明 |
|---|---|---|---|
| guanfu-score | guanfu | 10:00 | 观复读盘(Go 直调 nimbus-os/guanfu 库) |
| us-macro-report | us-macro-report | 21:30 | 宏观指标报告(FRED 系列,**需 FRED_API_KEY**) |
| earnings-calendar | script | 08:00 | 当日港美股财报(美股段 Finnhub /calendar/earnings 免费档,**需 FINNHUB_API_KEY**;港股段 akshare 百度财报发行日) |
| econ-calendar | script | 21:00 | 明日高重要性经济事件(ecocal,fxstreet 日历 API) |

## 二、脚本资产(scripts/)

### 简报内部调用(12 个,`--json-only`,报错即跳过不阻断)

`blockbeats_daily_reports`(morning/evening) · `market_sentiment` · `hot_sector_push` ·
`capital_flow` · `lhb` · `closing_scan` · `closing_scan_backfill`(工具) · `fedwatch` ·
`vix_term` · `earnings_calendar` · `ipo_calendar` · `hk_ipo_calendar` · `unlock_calendar`

### config script 源命令行(2 个)

- `earnings_calendar.py --days 1`(08:00)
- `econ_calendar.py --days 1`(21:00)

### 未接入的遗留脚本(9 个,无对应源,勿删除——候选/归档)

`block_trade`(大宗) · `edgar_13f` · `fetch_symbols` · `filter_checker` · `institutional_research` ·
`intraday_breakout` · `limit_up_ladder`(涨停梯) · `superinvestors`(13F) · `utils`(公共工具库,被其他脚本 import)

## 三、密钥(全部经 env 注入 .env,gitignored;config 里以 `${VAR}` 引用,不落盘明文)

| 变量 | 用途 |
|---|---|
| `DEEPSEEK_API_KEY` | DeepSeek 翻译(外文→中文)+ 简评 + AI Filter(api.deepseek.com) |
| `FRED_API_KEY` | us-macro-report 的 FRED 宏观序列(series 见 config) |
| `WECHAT_WEBHOOK` | 企业微信推送(wechat-main 渠道) |
| `FEISHU_WEBHOOK` | 飞书自定义机器人推送 |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_ENCRYPT_KEY` / `FEISHU_VERIFICATION_TOKEN` | 飞书应用(仅健康告警用) |
| `DISCORD_PUSH_WEBHOOK` | Discord 推送(3 个 webhook 轮询) |

## 四、外部依赖

| 依赖 | 用途 |
|---|---|
| nimbus-os/datasources + market 模块(Go 直连) | 简报行情(marketService.GetMarketQuote)、Nasdaq 事件日历(nasdaq 包);**非 :8800** |
| Finnhub API(免费档) | earnings-calendar 美股段(FINNHUB_API_KEY) |
| akshare(venv) | earnings-calendar 港股段(百度财报发行日) |
| ecocal(PyPI,fxstreet 日历 API) | econ-calendar 经济事件日历 |
| BlockBeats(公开接口) | 早报/晚报聚合正文(blockbeats_daily_reports.py) |
| DeepSeek API(api.deepseek.com) | 翻译/简评/分类 |

## 五、接口

- 平台自身:`GET /api/health`、`GET /api/news`(store 查询)、`POST /api/sources/:name/trigger`、`/metrics`(Prometheus)
- 部署:VPS `/opt/news`(systemd news-platform.service),push 即部署(VPS 自构建)。nimbus 侧不依赖本平台接口,按本文档源清单直拉
