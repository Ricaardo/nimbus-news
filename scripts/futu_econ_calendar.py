#!/usr/bin/env python3
"""
富途经济事件日历 — 每日推送明日高重要性经济事件预告。

通过 Futu OpenD (127.0.0.1:11111) 获取指定市场的经济事件日历数据，
过滤指定重要性的事件，按国家分段输出中文摘要到 stdout，供 news 平台 script 源推送。
空结果时静默退出 (exit 0)，无输出即无推送。

用法: python3 scripts/futu_econ_calendar.py [--days N] [--importance HIGH] [--markets US,HK,JP,SH,SG] [--json]
"""

import argparse
import json
import os
import socket
import sys
from datetime import date, datetime, timedelta, timezone

# OpenD 登录态只检查一次，避免重复查询
_login_checked = False

# 本地时区 (北京时间)
_TZ_LOCAL = timezone(timedelta(hours=8), name="Asia/Shanghai")

# Market 枚举会被 _init_enums() 填充
MARKET_MAP = {}
IMPORTANCE_MAP = {}

# 国家名 → emoji 映射（API 返回中文国家名）
COUNTRY_EMOJI = {
    "美国": "\U0001f1fa\U0001f1f8",
    "中国香港": "\U0001f1ed\U0001f1f0",
    "日本": "\U0001f1ef\U0001f1f5",
    "中国": "\U0001f1e8\U0001f1f3",
    "新加坡": "\U0001f1f8\U0001f1ec",
    "澳大利亚": "\U0001f1e6\U0001f1fa",
    "马来西亚": "\U0001f1f2\U0001f1fe",
    "加拿大": "\U0001f1e8\U0001f1e6",
}

COUNTRY_ORDER = ["美国", "中国", "中国香港", "日本", "新加坡", "澳大利亚", "加拿大", "马来西亚"]


def _init_enums():
    """延迟导入 Market/EconomicImportance 枚举，避免提前触发 SDK 日志。"""
    from futu import Market, EconomicImportance
    MARKET_MAP.update({
        "US": Market.US,
        "HK": Market.HK,
        "SH": Market.SH,
        "SG": Market.SG,
        "JP": Market.JP,
        "AU": Market.AU,
        "MY": Market.MY,
        "CA": Market.CA,
    })
    IMPORTANCE_MAP.update({
        "ALL": EconomicImportance.ALL,
        "LOW": EconomicImportance.LOW,
        "MEDIUM": EconomicImportance.MEDIUM,
        "HIGH": EconomicImportance.HIGH,
    })


# ── OpenD 探测 ──

def _probe_opend(host: str = "127.0.0.1", port: int = 11111, timeout: float = 2.0) -> None:
    """socket 探测 OpenD 是否可达；不通则打印中文错误到 stderr 并 exit 1。
    ScriptSource 有 120s 超时兜底，但脚本自身要干净退出，绝不挂起。"""
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(timeout)
    try:
        sock.connect((host, port))
    except (ConnectionRefusedError, OSError) as e:
        print(f"错误: 无法连接 OpenD ({host}:{port}): {e}。请确认 OpenD 已启动并登录。",
              file=sys.stderr)
        sys.exit(1)
    finally:
        sock.close()


# ── 工具函数 ──

def _safe_val(val, default=""):
    """安全获取标量值：处理 NaN / None / NaT。"""
    if val is None:
        return default
    try:
        import math
        if isinstance(val, float) and math.isnan(val):
            return default
    except TypeError:
        pass
    try:
        import pandas as pd
        if pd.isna(val):
            return default
    except (ImportError, TypeError):
        pass
    return val


def _fmt_time(timestamp) -> str:
    """将 Unix 时间戳转为 HH:MM 北京时间格式。"""
    ts = _safe_val(timestamp)
    if ts == "" or ts == 0:
        return "?"
    try:
        ts = float(ts)
        dt = datetime.fromtimestamp(ts, tz=timezone.utc).astimezone(_TZ_LOCAL)
        return dt.strftime("%H:%M")
    except (ValueError, OSError, TypeError):
        return str(ts)[:5]


def _fmt_date(timestamp) -> str:
    """将 Unix 时间戳转为 MM-DD 北京时间日期格式（多日模式前缀用）。"""
    ts = _safe_val(timestamp)
    if ts == "" or ts == 0:
        return "?"
    try:
        ts = float(ts)
        dt = datetime.fromtimestamp(ts, tz=timezone.utc).astimezone(_TZ_LOCAL)
        return dt.strftime("%m-%d")
    except (ValueError, OSError, TypeError):
        return str(ts)[:5]


def _fmt_val(val) -> str:
    """格式化经济指标值（前值/预期/实际）。"""
    v = _safe_val(val)
    if v == "":
        return ""
    try:
        fv = float(v)
        if abs(fv) >= 1000:
            return f"{fv:,.0f}"
        elif abs(fv) >= 1:
            return f"{fv:.2f}"
        else:
            return f"{fv:.4f}"
    except (ValueError, TypeError):
        return str(v)[:20]


def _star_label(star: str) -> str:
    """重要性转星星标记。"""
    s = str(star).upper().strip()
    if s == "HIGH":
        return "⭐⭐⭐"
    elif s == "MEDIUM":
        return "⭐⭐"
    elif s == "LOW":
        return "⭐"
    return ""


# ── 数据获取 ──

def _fetch_econ_calendar(begin_date, end_date, market_list, importance, host, port):
    """单次 API 调用获取经济事件，返回 dict 列表或 None（连接/权限错误）。
    空结果返回空列表 []。"""
    from futu import OpenQuoteContext, RET_OK

    ctx = None
    try:
        # 构造 OpenQuoteContext；ai_type 仅高版本 SDK 支持，低版本回退
        try:
            ctx = OpenQuoteContext(host=host, port=port, ai_type=1)
        except TypeError:
            ctx = OpenQuoteContext(host=host, port=port)

        # OpenD 登录态检查：日历类接口依赖行情登录，socket 探测只验 TCP 不验登录
        global _login_checked
        if not _login_checked:
            _login_checked = True
            login_ret, global_state = ctx.get_global_state()
            if login_ret != RET_OK or not global_state.get("qot_logined"):
                print(
                    "错误: OpenD 未登录行情（qot_logined=false），请在 OpenD 客户端登录后重试",
                    file=sys.stderr,
                )
                return None

        ret, data, next_page, has_more = ctx.get_economic_calendar(
            begin_date=begin_date, end_date=end_date,
            market_list=market_list, importance=importance,
            count=100,  # API 上限 100，超限直接报错
        )
        if ret != RET_OK:
            print(f"错误: 获取经济事件日历失败: {data}", file=sys.stderr)
            return None

        if data is None or (hasattr(data, "empty") and data.empty):
            return []

        rows = []
        for i in range(len(data)):
            row = data.iloc[i]
            rows.append({
                "title": str(_safe_val(row.get("title", ""))),
                "timestamp": _safe_val(row.get("timestamp"), 0),
                "country": str(_safe_val(row.get("country", ""))),
                "star": str(_safe_val(row.get("star", ""))),
                "previous": str(_safe_val(row.get("previous", ""))),
                "consensus": str(_safe_val(row.get("consensus", ""))),
                "actual": str(_safe_val(row.get("actual", ""))),
            })
        return rows

    except Exception as e:
        print(f"错误: 获取经济事件日历异常: {e}", file=sys.stderr)
        return None
    finally:
        if ctx is not None:
            try:
                ctx.close()
            except Exception:
                pass


# ── 渲染输出 ──

def _render_text(all_rows: list) -> str:
    """中文纯文本输出，按国家分段，段内按时间排序。emoji 风格对齐第一期。"""
    if not all_rows:
        return ""

    # 按国家分组，段内按时间排序
    groups: dict[str, list] = {}
    for r in all_rows:
        country = r.get("country", "其他")
        if country not in groups:
            groups[country] = []
        groups[country].append(r)

    for rows in groups.values():
        rows.sort(key=lambda r: float(r["timestamp"]) if r["timestamp"] else 0)

    tomorrow = date.today() + timedelta(days=1)
    date_label = f"{tomorrow.month:02d}-{tomorrow.day:02d}"
    # 多日模式（--days > 1）：存在跨北京日期事件时，每条前缀日期避免混淆
    multi_day = len({_fmt_date(r.get("timestamp", 0)) for r in all_rows}) > 1
    lines = [f"\U0001f30f 明日经济事件 {date_label}", ""]

    # 按预设顺序输出市场段；未知国家（含 country 为空的"其他"行）追加在末尾，不静默丢弃
    render_order = list(COUNTRY_ORDER) + sorted(set(groups) - set(COUNTRY_ORDER))
    for country in render_order:
        if country not in groups:
            continue
        rows = groups[country]
        emoji = COUNTRY_EMOJI.get(country, "")
        lines.append(f"{emoji} {country} — 共 {len(rows)} 条")
        for r in rows:
            ts_raw = r.get("timestamp", 0)
            ts = f"{_fmt_date(ts_raw)} {_fmt_time(ts_raw)}" if multi_day else _fmt_time(ts_raw)
            title = r.get("title", "")
            star = _star_label(r.get("star", ""))
            parts = [ts, title]
            if star:
                parts.append(star)

            # 前值 / 预期 / 实际
            extras = []
            prev = _fmt_val(r.get("previous", ""))
            cons = _fmt_val(r.get("consensus", ""))
            actual = _fmt_val(r.get("actual", ""))
            if actual:
                extras.append(f"实际 {actual}")
            if cons and not actual:
                extras.append(f"预期 {cons}")
            if prev:
                extras.append(f"前值 {prev}")
            if extras:
                parts.append(" · ".join(extras))

            lines.append("  " + " | ".join(p for p in parts if p))
        lines.append("")

    if not groups:
        return ""
    return "\n".join(lines).rstrip()


# ── 主入口 ──

def main():
    parser = argparse.ArgumentParser(description="富途经济事件日历 — 明日高重要性经济事件预告")
    parser.add_argument("--days", type=int, default=1, help="拉取未来 N 天 (默认 1)")
    parser.add_argument("--importance", type=str, default="HIGH",
                        choices=["ALL", "LOW", "MEDIUM", "HIGH"],
                        help="重要性过滤 (默认 HIGH)")
    parser.add_argument("--markets", type=str, default="US,HK,JP,SH,SG",
                        help="市场列表，逗号分隔 (默认 US,HK,JP,SH,SG)")
    parser.add_argument("--json", action="store_true", dest="output_json", help="输出 JSON")
    args = parser.parse_args()

    # 0. 前置守卫：--days 必须 >= 1
    if args.days < 1:
        print("错误: --days 必须 >= 1", file=sys.stderr)
        sys.exit(1)

    # 0. OpenD 连通性探测（必须在 import futu 之前做，避免 SDK 层卡住）
    host = os.environ.get("FUTU_OPEND_HOST", "127.0.0.1")
    port = int(os.environ.get("FUTU_OPEND_PORT", "11111"))
    _probe_opend(host, port)

    # 0.1 抑制 futu SDK stdout 日志（FTLog 默认 INFO→stdout，会污染 ScriptSource 输出流）
    try:
        from futu.common.ft_logger import FTLog
        FTLog().debug_model = False
    except (ImportError, AttributeError):
        # SDK 内部模块变动时降级：最多日志污染，不能让源直接死掉
        pass

    # 0.2 SDK 版本守卫: get_economic_calendar 需 futu-api >= 10.9.6908，降级时报可操作错误
    try:
        import futu
        cur = getattr(futu, "__version__", "0")
        try:
            version_ok = tuple(int(x) for x in str(cur).split(".")) >= (10, 9, 6908)
        except ValueError:
            version_ok = False
        if not version_ok:
            print(
                f"错误: 需要 futu-api >= 10.9.6908（当前 {cur}），请 pip install --upgrade 'futu-api>=10.9.6908'",
                file=sys.stderr,
            )
            sys.exit(1)
    except ImportError:
        print("错误: 未安装 futu-api，请 pip install 'futu-api>=10.9.6908'", file=sys.stderr)
        sys.exit(1)

    # 0.3 初始化枚举
    _init_enums()

    # 1. 日期范围：明日开始，向前拉取 days 天
    tomorrow = date.today() + timedelta(days=1)
    begin_date = tomorrow.isoformat()
    end_date = (tomorrow + timedelta(days=args.days - 1)).isoformat()

    # 2. 解析市场列表
    market_list = []
    for m in args.markets.split(","):
        m = m.strip().upper()
        enum_val = MARKET_MAP.get(m)
        if enum_val is None:
            print(f"错误: 不支持的市场: {m}，可选: {list(MARKET_MAP.keys())}",
                  file=sys.stderr)
            sys.exit(1)
        market_list.append(enum_val)

    # 3. 解析重要性
    importance = IMPORTANCE_MAP.get(args.importance.upper())
    if importance is None:
        print(f"错误: 不支持的重要性级别: {args.importance}", file=sys.stderr)
        sys.exit(1)

    # 4. 获取数据
    all_rows = _fetch_econ_calendar(begin_date, end_date, market_list, importance, host, port)
    if all_rows is None:
        sys.exit(1)

    # 5. 空结果 → 静默退出（ScriptSource 对空输出不会推送）
    if len(all_rows) == 0:
        return

    # 6. 输出
    if args.output_json:
        output = {
            "date": date.today().isoformat(),
            "target_date": tomorrow.isoformat(),
            "range": {"begin": begin_date, "end": end_date},
            "importance": args.importance.upper(),
            "markets": args.markets.upper(),
            "total": len(all_rows),
            "items": all_rows,
        }
        print(json.dumps(output, ensure_ascii=False, default=str))
    else:
        text = _render_text(all_rows)
        if text:
            print(text)


if __name__ == "__main__":
    main()
