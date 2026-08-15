#!/usr/bin/env python3
"""
财报日历 — 当日港美股发布财报的公司(开源数据源,无需 OpenD)。

美股段: Finnhub /calendar/earnings(免费档,需 FINNHUB_API_KEY env)
港股段: akshare news_report_time_baidu(百度股市通财报发行日,含港股)
替代已退役的 futu-earnings-calendar(依赖 Futu OpenD)。

ScriptSource 契约:
- stdout 首行 = 消息标题
- 空结果静默退出 (exit 0),无输出即无推送
- 全部市场失败 → stderr + exit 1;部分失败记 stderr 警告、有数据则继续

用法: python3 scripts/earnings_calendar.py [--days N] [--json]
"""

import argparse
import json
import os
import sys
import time
import urllib.request
from datetime import date, timedelta

FINNHUB_URL = "https://finnhub.io/api/v1/calendar/earnings"

# 时段映射(Finnhub hour 字段,实测 bmo/amc/dmh):盘前/盘后/盘中
_SESSION_MAP = {"BMO": "盘前", "AMC": "盘后", "DMH": "盘中"}


def _http_get_json(url: str):
    """GET 并解析 JSON;重试 2 次应对间歇性截断(如 EIA 源)。"""
    for attempt in range(3):
        try:
            with urllib.request.urlopen(url, timeout=30) as resp:  # noqa: S310 — 白名单 API
                return json.loads(resp.read().decode())
        except Exception:
            if attempt == 2:
                raise
            time.sleep(0.8 * (attempt + 1))


# ── 美股段:Finnhub ──

def _fetch_us(begin: str, end: str):
    """Finnhub 美股财报日历;失败返回 None,空结果返回 []。
    Finnhub 无公司名/市值排序,免费档单日可返回上百条(含大量壳股),
    返回全量并按「有 EPS 预期优先 + |EPS| 降序」排序,截断由调用方做。"""
    key = os.environ.get("FINNHUB_API_KEY", "").strip()
    if not key:
        print("错误: 未设置 FINNHUB_API_KEY,美股财报日历不可用", file=sys.stderr)
        return None
    url = f"{FINNHUB_URL}?from={begin}&to={end}&token={key}"
    try:
        data = _http_get_json(url)
    except Exception as e:
        print(f"错误: 获取美股财报日历失败: {e}", file=sys.stderr)
        return None
    rows = []
    for d in data.get("earningsCalendar", []):
        sym = str(d.get("symbol", "")).strip()
        if not sym:
            continue
        eps = d.get("epsEstimate")
        try:
            eps_num = float(eps)
        except (ValueError, TypeError):
            eps_num = 0.0
            eps = None
        rows.append({
            "symbol": sym,
            "session": _SESSION_MAP.get(str(d.get("hour", "")).upper(), ""),
            "eps": eps,
            "_eps_num": eps_num,
        })
    # 有 EPS 的按 |EPS| 降序在前,无 EPS 的按 symbol 字母序排后
    rows.sort(key=lambda r: (r["_eps_num"] == 0.0, -abs(r["_eps_num"]), r["symbol"]))
    return rows


# ── 港股段:akshare(百度股市通财报发行日,含港股) ──

def _fetch_hk(day: date):
    """单日港股财报;失败返回 None,空结果返回 []。"""
    try:
        import akshare as ak
    except ImportError:
        print("错误: 未安装 akshare,港股财报日历不可用(pip install akshare)", file=sys.stderr)
        return None
    try:
        df = ak.news_report_time_baidu(date=day.strftime("%Y%m%d"))
    except Exception as e:
        print(f"错误: 获取港股财报日历失败: {e}", file=sys.stderr)
        return None
    if df is None or df.empty:
        return []
    rows = []
    for _, r in df.iterrows():
        if "HK" not in str(r.get("交易所", "")).upper():
            continue
        code = str(r.get("股票代码", "")).strip()
        name = str(r.get("股票简称", "")).strip()
        period = str(r.get("财报期", "")).strip()
        if not code or not name:
            continue
        rows.append({"code": code, "name": name, "period": period})
    rows.sort(key=lambda r: r["code"])
    return rows


# ── 渲染 ──

def _fmt_eps(eps) -> str:
    """格式化预期 EPS;非有效数值返回空串。"""
    if eps is None or eps == "":
        return ""
    try:
        return f"预期 EPS {float(eps):.2f}"
    except (ValueError, TypeError):
        return ""


def _render_text(us_rows: list, hk_rows: list, today: date,
                 us_total: int, hk_total: int) -> str:
    """中文纯文本输出,按市场分段(对齐 futu_earnings_calendar.py 风格)。
    us_total/hk_total 为截断前的原始条数,超出时标题标注。"""
    date_label = f"{today.month:02d}-{today.day:02d}"
    lines = [f"📅 财报日历 {date_label}", ""]

    def section(emoji: str, label: str, rows: list, total: int) -> None:
        if not rows:
            return
        suffix = f"({total} 家中列前 {len(rows)})" if total > len(rows) else ""
        lines.append(f"{emoji} {label} — 共 {len(rows)} 家{suffix}")

    section("🇺🇸", "美股", us_rows, us_total)
    for r in us_rows:
        parts = [r["symbol"]]
        if r["session"]:
            parts.append(r["session"])
        eps = _fmt_eps(r.get("eps"))
        if eps:
            parts.append(eps)
        lines.append("  " + " · ".join(parts))
    if us_rows:
        lines.append("")

    section("🇭🇰", "港股", hk_rows, hk_total)
    for r in hk_rows:
        parts = [f"{r['code']}({r['name']})"]
        if r["period"]:
            parts.append(r["period"])
        lines.append("  " + " · ".join(parts))
    if hk_rows:
        lines.append("")

    if not us_rows and not hk_rows:
        return ""
    return "\n".join(lines).rstrip()


# ── 主入口 ──

def main():
    parser = argparse.ArgumentParser(description="财报日历 — 当日港美股财报(Finnhub + akshare)")
    parser.add_argument("--days", type=int, default=1, help="拉取未来 N 天 (默认 1)")
    parser.add_argument("--max-us", type=int, default=40, help="美股最多展示条数 (0=不限制)")
    parser.add_argument("--max-hk", type=int, default=40, help="港股最多展示条数 (0=不限制)")
    parser.add_argument("--json", action="store_true", dest="output_json", help="输出 JSON")
    args = parser.parse_args()
    if args.days < 1:
        print("错误: --days 至少为 1", file=sys.stderr)
        sys.exit(1)

    today = date.today()
    begin_date = today.isoformat()
    end_date = (today + timedelta(days=args.days - 1)).isoformat()

    # 1. 美股段(一次区间查询,截断前先记总数)
    us_rows, us_error = [], ""
    us_total = 0
    result = _fetch_us(begin_date, end_date)
    if result is None:
        us_error = "美股获取失败"
    else:
        us_rows = result
        us_total = len(us_rows)
        if args.max_us > 0 and len(us_rows) > args.max_us:
            us_rows = us_rows[:args.max_us]

    # 2. 港股段(百度接口按单日查询,逐日合并)
    hk_rows, hk_error = [], ""
    hk_total = 0
    for i in range(args.days):
        day = today + timedelta(days=i)
        result = _fetch_hk(day)
        if result is None:
            hk_error = "港股获取失败"
            break
        hk_rows.extend(result)
    hk_total = len(hk_rows)
    if args.max_hk > 0 and len(hk_rows) > args.max_hk:
        hk_rows = hk_rows[:args.max_hk]

    errors = [e for e in (us_error, hk_error) if e]

    # 3. 全部失败 → 报错退出(HealthMonitor 计 unhealthy)
    if errors and not us_rows and not hk_rows:
        print(f"错误: {', '.join(errors)},无可用数据", file=sys.stderr)
        sys.exit(1)

    # 4. 空结果 → 静默退出(ScriptSource 对空输出不会推送)
    if not us_rows and not hk_rows:
        return

    # 5. 输出
    if args.output_json:
        output = {
            "date": today.isoformat(),
            "range": {"begin": begin_date, "end": end_date},
            "total": us_total + hk_total,
            "us": [{k: v for k, v in r.items() if not k.startswith("_")} for r in us_rows],
            "hk": hk_rows,
        }
        if errors:
            output["warnings"] = errors
        print(json.dumps(output, ensure_ascii=False, default=str))
    else:
        text = _render_text(us_rows, hk_rows, today, us_total, hk_total)
        if text:
            print(text)


if __name__ == "__main__":
    main()
