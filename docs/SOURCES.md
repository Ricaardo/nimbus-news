# 数据源总览

news 平台的全部数据来源、接口/RSS 链接、所需密钥与外部依赖。
配置模板见 `config.platform.yaml.example`;真实配置 `config.platform.yaml`(gitignored,含密钥)。

## 一、采集源(写入 store / 推送)

### RSS 源(10 个,公开,无需密钥)

| 源名 | 链接 |
|---|---|
| trump-rss | https://trump.fm/rss/analysis.xml |
| bwe-tradfi | https://ch2rss.fflow.net/BWEtradfi |
| kobeissi-letter | https://ch2rss.fflow.net/TheKobeissiLetter |
| fed-speeches | https://www.federalreserve.gov/feeds/speeches.xml |
| fed-press | https://www.federalreserve.gov/feeds/press_all.xml |
| bloomberg-markets | https://www.bloomberg.com/feeds/markets/news.rss |
| bloomberg-economics | https://www.bloomberg.com/feeds/economics/news.rss |
| bloomberg-politics | https://www.bloomberg.com/feeds/politics/news.rss |
| forexlive-breaking | https://investinglive.com/feed/news |
| eia-energy | https://www.eia.gov/rss/todayinenergy.xml |

> 注:ch2rss.fflow.net 是第三方 RSS 代理(上游偶发超时,已有重试);bloomberg RSS 为公开 feed。

### 简报/聚合源(本地生成)

| 源名 | 类型 | 时间表 | 说明 |
|---|---|---|---|
| news-aggregate-morning / -evening | market-briefing | 09:15 / 19:30 | 早报/晚报聚合(数据来自 dataplane :8800) |
| pre-market-briefing | market-briefing | 09:15 11:35 13:05 15:00 | 盘前全景,时段感知 + AI 叙事 |
| closing-briefing | market-briefing | 16:10 | 收盘复盘 |
| us-preview | market-briefing | 21:10 | 美股盘前预览 |
| us-macro-report | us-macro-report | 21:30 | 宏观指标报告,**需 FRED_API_KEY** |
| guanfu-score | guanfu | 10:00 | 观复读盘(调 ~/nimbus-os/guanfu) |
| earnings-calendar | script | 08:00 | 当日港美股财报(美股段 Finnhub /calendar/earnings 免费档,**需 FINNHUB_API_KEY**;港股段 akshare 百度财报发行日) |
| econ-calendar | script | 21:00 | 明日高重要性经济事件(ecocal,fxstreet 日历 API) |

## 二、密钥(全部经 env 注入 .env,gitignored;config 里以 `${VAR}` 引用,不落盘明文)

| 变量 | 用途 |
|---|---|
| `DEEPSEEK_API_KEY` | DeepSeek 翻译(外文→中文)+ 简评 + AI Filter(api.deepseek.com) |
| `FRED_API_KEY` | us-macro-report 的 FRED 宏观序列(series 见 config) |
| `WECHAT_WEBHOOK` | 企业微信推送(wechat-main 渠道) |
| `FEISHU_WEBHOOK` | 飞书自定义机器人推送 |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_ENCRYPT_KEY` / `FEISHU_VERIFICATION_TOKEN` | 飞书应用(仅健康告警用) |
| `DISCORD_PUSH_WEBHOOK` | Discord 推送(3 个 webhook 轮询) |

## 三、外部依赖

| 依赖 | 用途 |
|---|---|
| Finnhub API(免费档) | earnings-calendar 美股段(FINNHUB_API_KEY) |
| akshare(venv) | earnings-calendar 港股段(百度财报发行日) |
| ecocal(PyPI,fxstreet 日历 API) | econ-calendar 经济事件日历 |
| dataplane :8800(nimbus-os 仓) | 简报/聚合源的行情与宏观数据 |
| DeepSeek API(api.deepseek.com) | 翻译/简评/分类 |

## 四、接口

- 平台自身:`GET /api/health`、`GET /api/news`(store 查询)、`POST /api/sources/:name/trigger`、`/metrics`(Prometheus)
- 部署:VPS `/opt/news`(systemd news-platform.service)。nimbus 侧不依赖本平台接口,按本文档源清单直拉
