# 功能路线图

**Version**: 2.0.0 | **Updated**: 2026-05-09

本文档记录后续待办功能点。完成后划掉 (不删除条目, 保留历史)。

---

## 当前系统快照 (v2)

- **14 个源**: 实时新闻 6 + 定点快照 2 + 聚合报告 6
- **聚合报告源**: morning-scan (09:00), intraday-breakout (10:30/14:00),
  closing-scan (14:45), evening-report (15:30), us-macro-report (21:30),
  weekly-calendar (周一 08:00)
- **LLM**: 3 报告接入 (morning-scan / us-macro-report / evening-report)
- **双模推送**: wechat 用 ShortContent / discord 用 Content
- **Metrics**: 4 个聚合报告维度 + LLM 成功率
- **API**: `GET /api/reports?type=X&limit=N` 查询历史报告
- **启动自检**: closing_scan.db 不足 100 样本自动异步回灌 60 日

---

## Tier 1 — 上线验证 (仅剩真实数据验证)

### T1.1 首次真实跑, 收集字段问题

**背景**: 14 处 schema 对齐是读 Python 代码推导的, 虽已做一轮对齐,
真实 akshare 数据类型细节仍可能踩坑。

**建议工作流 (首次运行后做)**:
- [ ] 09:00 对比 hot_sector / stock_picker / research_report 日志
- [ ] 14:45 对比 closing_scan 日志
- [ ] 15:30 对比 market_sentiment / lhb / capital_flow 日志
- [ ] 周一 08:00 对比 5 个 weekly 脚本日志

若发现字段不匹配, 修改对应 Go struct json tag。

---

## Tier 4 — 可选优化 (非阻塞)

### T4.1 LLM prompt 调优 (首次运行后)

**背景**: 三个 LLM prompt 未经 A/B 验证, 可能输出不够精炼

**待做**:
- [ ] 收集 3-5 天真实 AI 输出, 人工标注质量
- [ ] 根据反馈重写 prompt
- [ ] 引入 few-shot 示例

### T4.2 闲时脚本降级

**背景**: 非交易日 intraday-breakout / closing-scan 也会跑, 浪费 API 配额

**待做**:
- [ ] Go 源内加交易日判断 (周末 / 国定假日)
- [ ] 非交易日直接返回 nil, 跳过 Python 调用

### T4.3 更多实时异动维度

**当前**: 只有"突破 60 日新高 + 量比 > 2"

**可扩展**:
- [ ] 涨停板梯队实时推送 (出现 5 板以上立即通知)
- [ ] 大宗交易异动 (机构席位)
- [ ] 隔夜美股同板块爆发触发次日盘前扫描

### T4.4 报告回看 UI

**背景**: `/api/reports` API 有了, 但没前端界面

**待做**:
- [ ] admin UI 加 "历史报告" 页, 选 type + 日期回看

---

## 已完成 (保留历史)

- ✅ 20 源精简 → 14 源 (`5a1f49c` + `838ef86`)
- ✅ 5 个聚合报告源框架 (`457475d` + `04a1397`)
- ✅ finnhub 事件面 + 截断可配 (`04e5f87` + `6343ed4`)
- ✅ 脚本 + IBKR 清理, 30 → 18 脚本 (`8d4747b`)
- ✅ 14 处 schema 字段对齐 (`bf8f094`)
- ✅ LLM 接入 3 报告 — AI 叙事/分析/复盘 (`dcecee4`)
- ✅ guanfu 包升级 + roadmap 初版 (`7102df9`)
- ✅ T2.2 morning-scan 硬过滤 (filter_checker.py) (`8eb4ded`)
- ✅ T2.3 closing-scan 数据补全 (龙虎榜 + 美股联动) (`8eb4ded`)
- ✅ T2.4 渠道差异化 (ShortContent / renderShort) (`8eb4ded` + 本轮)
- ✅ T3.1 报告历史归档 + `/api/reports` API (`b141491`)
- ✅ T3.2 Metrics (report_script / llm_summarize 维度) (`ba15bfb`)
- ✅ T3.3 closing-scan 风险分析 (天梯 / 孤票 / 断板 / 封单) (`f1b4c7b`)
- ✅ T3.4 intraday-breakout 实时异动 (`f1b4c7b`)
- ✅ T1.2 closing_scan.db 启动自检 + 自动 bootstrap (本轮)
- ✅ filter_checker.py 并发化 (8 线程, 20 只股 ~15s → ~3s) (本轮)
- ✅ T2.4 补齐: evening/us-macro/weekly 也加 renderShort (本轮)
