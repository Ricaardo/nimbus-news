#!/usr/bin/env python3
"""
filter_checker.py — morning-scan 候选股硬过滤批量检查

输入 stdin JSON: {"codes": ["300750", "002415", ...]}
输出 stdout JSON: {"results": [{"code", "skip", "reasons": []}, ...]}

检查维度 (命中任一 skip=true):
- 今日停牌 (近 3 日 akshare hist 为空 / 无最新数据)
- 3 日累涨 ≥ 7%
- 明日解禁 (unlock_calendar lookahead 1 天)
- 明日财报披露 (earnings_calendar lookahead 1 天)

性能: 典型 20 只候选 ~5-10s
"""

import argparse
import json
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime, timedelta
from pathlib import Path
from typing import Dict, List, Set

sys.path.append(str(Path(__file__).parent))


def load_unlock_tomorrow() -> Set[str]:
    """明日解禁股 code 集合"""
    try:
        import akshare as ak
    except ImportError:
        return set()
    try:
        tomorrow = (datetime.now() + timedelta(days=1)).strftime("%Y-%m-%d")
        df = ak.stock_restricted_release_detail_em()
        if df is None or df.empty:
            return set()
        codes = set()
        for _, row in df.iterrows():
            release_date = str(row.get("解禁日期") or row.get("release_date") or "")
            if release_date.startswith(tomorrow):
                code = str(row.get("代码") or row.get("code") or "").strip()
                if code:
                    codes.add(code)
        return codes
    except Exception as e:
        print(f"[WARN] unlock fetch: {e}", file=sys.stderr)
        return set()


def load_earnings_tomorrow() -> Set[str]:
    """明日财报披露股 code 集合"""
    try:
        import akshare as ak
    except ImportError:
        return set()
    try:
        tomorrow = (datetime.now() + timedelta(days=1)).strftime("%Y-%m-%d")
        df = ak.stock_report_disclosure(market="沪深京", period="本期")
        if df is None or df.empty:
            return set()
        codes = set()
        for _, row in df.iterrows():
            plan_date = str(row.get("预计披露时间") or row.get("plan_date") or "")
            if plan_date.startswith(tomorrow):
                code = str(row.get("代码") or row.get("code") or "").strip()
                if code:
                    codes.add(code)
        return codes
    except Exception as e:
        print(f"[WARN] earnings fetch: {e}", file=sys.stderr)
        return set()


def check_stock(code: str, unlock_set: Set[str], earnings_set: Set[str]) -> Dict:
    """单只股票判断, 返回 {code, skip, reasons}"""
    reasons = []

    # 明日解禁
    if code in unlock_set:
        reasons.append("明日解禁")
    # 明日财报
    if code in earnings_set:
        reasons.append("明日财报")

    # 3 日累涨 / 停牌 — 查 K 线
    try:
        import akshare as ak
        end = datetime.now().strftime("%Y%m%d")
        start = (datetime.now() - timedelta(days=10)).strftime("%Y%m%d")
        df = ak.stock_zh_a_hist(
            symbol=code, period="daily",
            start_date=start, end_date=end, adjust="qfq"
        )
        if df is None or df.empty:
            reasons.append("停牌或无数据")
        else:
            # 最新 3 个交易日累计涨幅
            recent = df.tail(3)
            if len(recent) >= 3:
                first_close = float(recent.iloc[0]["收盘"])
                last_close = float(recent.iloc[-1]["收盘"])
                if first_close > 0:
                    pct = (last_close - first_close) / first_close * 100
                    if pct >= 7:
                        reasons.append(f"3日累涨{pct:.1f}%")
            # 停牌检测: 最新数据日期距今 > 3 天
            latest_date = str(recent.iloc[-1]["日期"])[:10]
            today_str = datetime.now().strftime("%Y-%m-%d")
            try:
                latest_dt = datetime.strptime(latest_date, "%Y-%m-%d")
                if (datetime.now() - latest_dt).days > 3:
                    reasons.append("疑似停牌")
            except ValueError:
                pass
    except Exception as e:
        # 单只失败不阻断, 标记未知
        pass

    return {
        "code": code,
        "skip": len(reasons) > 0,
        "reasons": reasons,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--codes", type=str, default="",
                    help="逗号分隔的 code 列表, 不传则从 stdin 读 JSON")
    args = ap.parse_args()

    codes: List[str] = []
    if args.codes:
        codes = [c.strip() for c in args.codes.split(",") if c.strip()]
    else:
        try:
            payload = json.loads(sys.stdin.read())
            codes = payload.get("codes", [])
        except Exception as e:
            print(f"[ERROR] stdin parse: {e}", file=sys.stderr)
            sys.exit(1)

    if not codes:
        print(json.dumps({"results": []}))
        return

    # 批量加载解禁/财报集合 (避免每只查一次)
    unlock_set = load_unlock_tomorrow()
    earnings_set = load_earnings_tomorrow()

    # 并发查 K 线 (ThreadPool, max_workers=8)
    results = [None] * len(codes)
    with ThreadPoolExecutor(max_workers=8) as executor:
        futures = {
            executor.submit(check_stock, code, unlock_set, earnings_set): i
            for i, code in enumerate(codes)
        }
        for fut in as_completed(futures):
            i = futures[fut]
            try:
                results[i] = fut.result()
            except Exception as e:
                print(f"[WARN] check {codes[i]}: {e}", file=sys.stderr)
                results[i] = {"code": codes[i], "skip": False, "reasons": []}

    # 过滤掉 None (理论上不会有)
    results = [r for r in results if r is not None]

    print(json.dumps({"results": results}, ensure_ascii=False))


if __name__ == "__main__":
    main()
