#!/usr/bin/env python3
"""
经济事件日历 — 明日高重要性经济事件预告(ecocal:fxstreet 日历 API)。

替代已退役的 futu-econ-calendar(依赖 Futu OpenD):无本地服务依赖、无 API key。
ecocal 1.2.1 查询 calendar-api.fxstreet.com(公开日历接口);字段
Id/Start/Name/Impact/Currency,Start 为 UTC(实测:US Empire State 12:30 UTC =
8:30 EDT 发布),显示层转北京时间。

ScriptSource 契约:
- stdout 首行 = 消息标题
- 空结果静默退出 (exit 0),无输出即无推送
- 失败 stderr + exit 1(HealthMonitor 兜底计 unhealthy)

用法: python3 scripts/econ_calendar.py [--days N] [--importance HIGH|MEDIUM|ALL] [--json]
"""

import argparse
import contextlib
import io
import json
import sys
from datetime import date, datetime, timedelta, timezone

# 北京时区(显示层;ecocal Start 为 UTC)
_TZ_BJ = timezone(timedelta(hours=8), name="Asia/Shanghai")

# 货币 → 国旗+中文名(固定显示顺序;未知货币追加末尾不丢弃)
_CURRENCY_ORDER = [
    ("USD", "🇺🇸 美国"), ("CNY", "🇨🇳 中国"), ("CNH", "🇨🇳 中国"), ("HKD", "🇭🇰 中国香港"),
    ("JPY", "🇯🇵 日本"), ("KRW", "🇰🇷 韩国"), ("SGD", "🇸🇬 新加坡"), ("AUD", "🇦🇺 澳大利亚"),
    ("NZD", "🇳🇿 新西兰"), ("CAD", "🇨🇦 加拿大"), ("GBP", "🇬🇧 英国"), ("EUR", "🇪🇺 欧元区"),
    ("CHF", "🇨🇭 瑞士"), ("INR", "🇮🇳 印度"),
]
_CURRENCY_LABEL = dict(_CURRENCY_ORDER)

_IMPORTANCE_LEVEL = {"LOW": 1, "MEDIUM": 2, "HIGH": 3}
_STAR = {"LOW": "⭐", "MEDIUM": "⭐⭐", "HIGH": "⭐⭐⭐"}


def _load_calendar(start: str, end: str):
    """拉取 [start, end] 区间(UTC 日期)的事件;异常向上抛。
    ecocal 构造时会 print 日志到 stdout(Calendar.py:36/98),必须吞掉,
    否则污染 ScriptSource 输出流(首行标题契约)。"""
    # ecocal 1.2.1 源码用 np.NaN(numpy 2.0 已移除);新版 numpy 下 monkeypatch 兼容
    import numpy as np
    if not hasattr(np, "NaN"):
        np.NaN = np.nan
    from ecocal import Calendar

    with contextlib.redirect_stdout(io.StringIO()):
        cal = Calendar(startHorizon=start, endHorizon=end, nbThreads=5)
    return cal.calendar  # DataFrame: Id / Start / Name / Impact / Currency


def _parse_start(raw: str) -> datetime:
    """MM/DD/YYYY HH:MM:SS(UTC)→ aware datetime。"""
    return datetime.strptime(raw, "%m/%d/%Y %H:%M:%S").replace(tzinfo=timezone.utc)


def main():
    parser = argparse.ArgumentParser(description="明日经济事件日历(ecocal)")
    parser.add_argument("--days", type=int, default=1, help="预告未来 N 天 (默认 1)")
    parser.add_argument("--importance", default="HIGH",
                        choices=["LOW", "MEDIUM", "HIGH", "ALL"],
                        help="最低重要性 (默认 HIGH=只显示高重要性)")
    parser.add_argument("--json", action="store_true", dest="output_json", help="输出 JSON")
    args = parser.parse_args()
    if args.days < 1:
        print("错误: --days 至少为 1", file=sys.stderr)
        sys.exit(1)
    min_level = _IMPORTANCE_LEVEL.get(args.importance, 1)

    # 北京「明日」起 N 天;ecocal 按 UTC 日期索引,事件按北京日期过滤
    today = date.today()
    start_bj = today + timedelta(days=1)
    end_bj = start_bj + timedelta(days=args.days - 1)

    # 1. 拉取。北京日期覆盖 UTC [前一日 16:00, 当日 16:00),故 fetch 起点提前
    # 一整个 UTC 日,否则北京 00:00-08:00 的事件(FOMC 等)会漏拉;过滤按北京日期做。
    fetch_start = (start_bj - timedelta(days=1)).isoformat()
    try:
        df = _load_calendar(fetch_start, end_bj.isoformat())
    except Exception as e:
        print(f"错误: 拉取经济事件日历失败: {e}", file=sys.stderr)
        sys.exit(1)
    if df is None or len(df) == 0:
        print("错误: 经济事件日历无返回(数据源异常)", file=sys.stderr)
        sys.exit(1)

    # 2. 过滤:北京时间日期落在目标区间 + 重要性达标
    items, skipped = [], 0
    for _, row in df.iterrows():
        try:
            start_utc = _parse_start(str(row["Start"]).strip())
        except (ValueError, TypeError):
            skipped += 1
            continue
        bj_dt = start_utc.astimezone(_TZ_BJ)
        if not (start_bj <= bj_dt.date() <= end_bj):
            continue
        impact = str(row.get("Impact", "")).upper()
        if _IMPORTANCE_LEVEL.get(impact, 1) < min_level:
            continue
        currency = str(row.get("Currency", "")).upper()
        if not currency:
            skipped += 1
            continue
        items.append({
            "time_bj": bj_dt,
            "title": str(row.get("Name", "")).strip(),
            "impact": impact,
            "currency": currency,
        })
    if skipped:
        print(f"警告: {skipped} 条事件被跳过(时间戳无法解析或缺少币种)",
              file=sys.stderr)

    # 3. 空结果 → 静默退出(ScriptSource 对空输出不会推送)
    if not items:
        return

    # 4. 渲染
    if args.output_json:
        print(json.dumps({
            "date": today.isoformat(),
            "target_date": start_bj.isoformat(),
            "range": {"begin": start_bj.isoformat(), "end": end_bj.isoformat()},
            "importance": args.importance,
            "total": len(items),
            "items": [{**it, "time_bj": it["time_bj"].strftime("%m-%d %H:%M")}
                      for it in sorted(items, key=lambda x: x["time_bj"])],
        }, ensure_ascii=False, default=str))
        return

    multi_day = args.days > 1
    date_label = (f"{start_bj.month:02d}-{start_bj.day:02d}"
                  if not multi_day else
                  f"{start_bj.month:02d}-{start_bj.day:02d}~{end_bj.month:02d}-{end_bj.day:02d}")
    lines = [f"🌏 经济事件 {date_label}", ""]

    # 按货币顺序分段,未知货币追加末尾
    seen = set()
    groups = [c for c in _CURRENCY_ORDER if (c[0] in {it["currency"] for it in items})]
    for c, _ in groups:
        seen.add(c)
    for cur in sorted({it["currency"] for it in items} - seen):
        groups.append((cur, f"🌐 {cur}"))

    for cur, label in groups:
        rows = sorted([it for it in items if it["currency"] == cur], key=lambda x: x["time_bj"])
        lines.append(f"{label} — 共 {len(rows)} 条")
        for it in rows:
            t = it["time_bj"].strftime("%m-%d %H:%M" if multi_day else "%H:%M")
            star = _STAR.get(it["impact"], "")
            lines.append(f"  {t} | {it['title']} | {star}")
        lines.append("")

    print("\n".join(lines).rstrip())


if __name__ == "__main__":
    main()
