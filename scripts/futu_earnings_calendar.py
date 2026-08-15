#!/usr/bin/env python3
"""
富途财报日历 — 每日推送港美股当日发布财报的公司。

通过 Futu OpenD (127.0.0.1:11111) 获取 HK/US 市场的财报日历数据，
按市场分段输出中文摘要到 stdout，供 news 平台 script 源推送。
空结果时静默退出 (exit 0)，无输出即无推送。

用法: python3 scripts/futu_earnings_calendar.py [--days N] [--json]
"""

import argparse
import json
import os
import socket
import sys
from datetime import date, datetime, timedelta, timezone
from typing import Optional

# OpenD 登录态只检查一次（首个市场连接后），避免重复查询
_login_checked = False


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


def _fmt_date(val) -> str:
    """将 Unix 时间戳(秒)或 yyyy-MM-dd 字符串转为 MM-DD 简洁格式。"""
    result = _safe_val(val)
    if result == "" or result in ("NaT", "nan", "None", "?"):
        return "?"
    # Unix 时间戳路径：earnings_date 在 API 返回中是 float/int 秒级时间戳
    if isinstance(val, (int, float)) and val > 1000000000:
        try:
            dt = datetime.fromtimestamp(float(val), tz=timezone.utc)
            return dt.strftime("%m-%d")
        except (OSError, ValueError):
            pass
    # 字符串路径：可能是 yyyy-MM-dd 或已转为数字字符串
    s = str(result)
    try:
        if "-" in s:
            parts = s.strip().split("-")
            if len(parts) >= 3:
                return f"{parts[1]}-{parts[2]}"
        # 可能是数字字符串（已在 _fetch_one_market 转为 str 的 Unix 时间戳）
        ts = float(s)
        if ts > 1000000000:
            dt = datetime.fromtimestamp(ts, tz=timezone.utc)
            return dt.strftime("%m-%d")
    except (ValueError, OSError):
        return s[:5] if len(s) >= 5 else s
    return s[:5] if len(s) >= 5 else s


# 盘前/盘后/盘中 映射
_SESSION_MAP = {
    "BEFORE_MARKET": "盘前",
    "PRE_MARKET": "盘前",
    "AFTER_MARKET": "盘后",
    "POST_MARKET": "盘后",
    "DURING_MARKET": "盘中",
}


def _fmt_session(pub_type: str) -> str:
    """将 API 返回的 pub_type 转为中文时段简称。"""
    if not pub_type:
        return ""
    upper = str(pub_type).upper()
    for k, v in _SESSION_MAP.items():
        if k in upper:
            return v
    return ""


def _fmt_eps(eps_predict, eps_actual) -> str:
    """格式化 EPS 信息：预期 / 去年同期(若为有效数值)。"""
    parts = []
    p = _safe_val(eps_predict, None)
    a = _safe_val(eps_actual, None)
    if p is not None:
        try:
            v = float(p)
            parts.append(f"预期 EPS {v:.2f}")
        except (ValueError, TypeError):
            pass
    if a is not None:
        try:
            v = float(a)
            parts.append(f"去年同期 {v:.2f}")
        except (ValueError, TypeError):
            pass
    return " · ".join(parts) if parts else ""


# ── 数据获取 ──

def _fetch_one_market(market_enum, market_label, begin_date, end_date, host, port):
    """单个市场的单次 API 调用，返回 dict 列表或 None（连接/权限错误）。
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

        ret, data = ctx.get_earnings_calendar(
            market_enum, begin_date=begin_date, end_date=end_date
        )
        if ret != RET_OK:
            print(f"错误: 获取{market_label}财报日历失败: {data}", file=sys.stderr)
            return None

        if data is None or (hasattr(data, "empty") and data.empty):
            return []

        rows = []
        for i in range(len(data)):
            row = data.iloc[i]
            rows.append({
                "security": str(_safe_val(row.get("security", ""))),
                "name": str(_safe_val(row.get("name", ""))),
                "earnings_date": _safe_val(row.get("earnings_date"), 0),
                "pub_type": str(_safe_val(row.get("pub_type", ""))),
                "eps_predict": _safe_val(row.get("eps_predict")),
                "eps_actual": _safe_val(row.get("eps_actual")),
            })
        return rows

    except Exception as e:
        print(f"错误: 获取{market_label}财报日历异常: {e}", file=sys.stderr)
        return None
    finally:
        if ctx is not None:
            try:
                ctx.close()
            except Exception:
                pass


# ── 渲染输出 ──

def _render_text(all_rows: list) -> str:
    """中文纯文本输出，按市场分段，emoji 风格对齐 blockbeats/fedwatch 脚本。"""
    us_rows = [r for r in all_rows if r["security"].startswith("US.")]
    hk_rows = [r for r in all_rows if r["security"].startswith("HK.")]

    # 按代码字母序排序
    for rows in (us_rows, hk_rows):
        rows.sort(key=lambda r: r["security"])

    today = date.today()
    date_label = f"{today.month:02d}-{today.day:02d}"
    lines = [f"📅 财报日历 {date_label}", ""]

    for emoji, label, rows in [("🇺🇸", "美股", us_rows), ("🇭🇰", "港股", hk_rows)]:
        if not rows:
            continue
        lines.append(f"{emoji} {label} — 共 {len(rows)} 家")
        for r in rows:
            code = r["security"].split(".")[-1] if "." in r["security"] else r["security"]
            name = r["name"]
            dt = _fmt_date(r["earnings_date"])
            session = _fmt_session(r.get("pub_type", ""))
            eps = _fmt_eps(r.get("eps_predict"), r.get("eps_actual"))
            parts = [f"{code}({name})", dt]
            if session:
                parts.append(session)
            if eps:
                parts.append(eps)
            lines.append("  " + " · ".join(parts))
        lines.append("")

    if not us_rows and not hk_rows:
        return ""
    return "\n".join(lines).rstrip()


# ── 主入口 ──

def main():
    parser = argparse.ArgumentParser(description="富途财报日历 — 港美股当日财报")
    parser.add_argument("--days", type=int, default=1, help="拉取未来 N 天 (默认 1)")
    parser.add_argument("--json", action="store_true", dest="output_json", help="输出 JSON")
    args = parser.parse_args()

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

    # 0.2 SDK 版本守卫: get_earnings_calendar 需 futu-api >= 10.9.6908，降级时报可操作错误
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

    # 1. 日期范围
    today = date.today()
    begin_date = today.isoformat()
    end_date = (today + timedelta(days=args.days - 1)).isoformat()

    # 2. 获取数据
    from futu import Market

    all_rows = []
    errors = []
    for market_enum, label in [(Market.US, "美股"), (Market.HK, "港股")]:
        result = _fetch_one_market(market_enum, label, begin_date, end_date, host, port)
        if result is None:
            errors.append(f"{label}获取失败")
        else:
            all_rows.extend(result)

    # 3. 全部失败 → 报错退出
    if errors and len(all_rows) == 0:
        print(f"错误: {', '.join(errors)}，无可用数据", file=sys.stderr)
        sys.exit(1)

    # 4. 空结果 → 静默退出（ScriptSource 对空输出不会推送）
    if len(all_rows) == 0:
        return

    # 5. 输出
    if args.output_json:
        output = {
            "date": today.isoformat(),
            "range": {"begin": begin_date, "end": end_date},
            "total": len(all_rows),
            "items": all_rows,
        }
        if errors:
            output["warnings"] = errors
        print(json.dumps(output, ensure_ascii=False, default=str))
    else:
        text = _render_text(all_rows)
        if text:
            print(text)


if __name__ == "__main__":
    main()
