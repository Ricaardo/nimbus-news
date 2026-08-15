#!/usr/bin/env python3
"""
intraday_breakout.py — 盘中异动扫描

10:30 / 14:00 扫描:
- 突破 60 日新高
- 量比 > 2 (当前成交量 / 5 日均量)

输出 JSON:
{
  "type": "intraday_breakout",
  "timestamp": "...",
  "market": {zt_count, dt_count, up_count, down_count},
  "breakouts": [{code, name, price, change_pct, volume_ratio, sector,
                 prev_60d_high, reason}]
}
"""

import argparse
import json
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Dict, List

sys.path.append(str(Path(__file__).parent))


def is_restricted(code: str, name: str) -> bool:
    if "ST" in name.upper() or name.startswith("N"):
        return True
    if code.startswith(("83", "87", "88")):  # 北交所
        return True
    return False


def fetch_market_stats() -> Dict:
    """涨跌停 + 涨跌家数快照"""
    try:
        import akshare as ak
    except ImportError:
        return {}
    today = datetime.now().strftime("%Y%m%d")
    stats = {"zt_count": 0, "dt_count": 0, "up_count": 0, "down_count": 0}
    try:
        zt = ak.stock_zt_pool_em(date=today)
        stats["zt_count"] = 0 if zt is None else len(zt)
    except Exception:
        pass
    try:
        dt = ak.stock_zt_pool_dtgc_em(date=today)
        stats["dt_count"] = 0 if dt is None else len(dt)
    except Exception:
        pass
    try:
        spot = ak.stock_zh_a_spot_em()
        if spot is not None and not spot.empty and "涨跌幅" in spot.columns:
            stats["up_count"] = int((spot["涨跌幅"] > 0).sum())
            stats["down_count"] = int((spot["涨跌幅"] < 0).sum())
    except Exception:
        pass
    return stats


def scan_breakouts(top_n: int, min_volume_ratio: float) -> List[Dict]:
    """扫描全市场, 挑出符合突破+放量条件的股票"""
    try:
        import akshare as ak
    except ImportError:
        return []

    out = []
    today = datetime.now().strftime("%Y%m%d")

    # 1) 获取候选池: 全市场快照 → 涨停池 (降级)
    candidates = []
    try:
        spot = ak.stock_zh_a_spot_em()
        if spot is not None and not spot.empty:
            spot = spot[spot["涨跌幅"] > 3].copy()
            if "成交额" in spot.columns:
                spot = spot.sort_values("成交额", ascending=False).head(2000)
            for _, r in spot.iterrows():
                candidates.append(r)
    except Exception:
        pass

    if not candidates:
        try:
            zt = ak.stock_zt_pool_em(date=today)
            if zt is not None and not zt.empty:
                for _, r in zt.iterrows():
                    candidates.append(r)
        except Exception:
            pass

    if not candidates:
        return out

    # 2) 批量检查每只是否突破 60 日新高 + 量比 > 2
    for r in candidates:
        code = str(r.get("代码") or "").strip()
        name = str(r.get("名称") or "").strip()
        if not code or not name or is_restricted(code, name):
            continue

        price = float(r.get("最新价", 0) or 0)
        change_pct = float(r.get("涨跌幅", 0) or 0)
        volume = float(r.get("成交量", 0) or 0)
        if price <= 0 or volume <= 0:
            continue

        try:
            end = today
            start = (datetime.now() - timedelta(days=90)).strftime("%Y%m%d")
            df = ak.stock_zh_a_hist(
                symbol=code, period="daily",
                start_date=start, end_date=end, adjust="qfq"
            )
            if df is None or df.empty:
                if code.startswith("6"):
                    prefix = "sh"
                elif code.startswith(("8", "9")):
                    prefix = "bj"
                else:
                    prefix = "sz"
                df = ak.stock_zh_a_daily(symbol=prefix + code, adjust="")
                if df is not None and not df.empty and "date" in df.columns:
                    df.rename(columns={
                        "date": "日期", "open": "开盘", "high": "最高",
                        "low": "最低", "close": "收盘", "volume": "成交量",
                    }, inplace=True)
            if df is None or df.empty or len(df) < 10:
                continue
            hist = df.iloc[:-1].tail(60)
            if hist.empty:
                continue
            prev_high = float(hist["最高"].max())
            if price <= prev_high:
                continue
            vol_ma5 = float(hist["成交量"].tail(5).mean())
            if vol_ma5 <= 0:
                continue
            vol_ratio = volume / vol_ma5
            if vol_ratio < min_volume_ratio:
                continue

            sector = str(r.get("所属行业") or r.get("行业") or "")
            out.append({
                "code": code,
                "name": name,
                "price": round(price, 2),
                "change_pct": round(change_pct, 2),
                "volume_ratio": round(vol_ratio, 2),
                "prev_60d_high": round(prev_high, 2),
                "sector": sector,
                "reason": f"突破 60 日新高 (前高 {prev_high:.2f}), 量比 {vol_ratio:.1f}",
            })

            if len(out) >= top_n:
                break
        except Exception:
            continue

    out.sort(key=lambda x: x["volume_ratio"], reverse=True)
    return out[:top_n]

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--top", type=int, default=15, help="最多输出候选数")
    ap.add_argument("--min-volume-ratio", type=float, default=2.0, help="最低量比")
    ap.add_argument("--json-only", action="store_true")
    args = ap.parse_args()

    breakouts = scan_breakouts(args.top, args.min_volume_ratio)
    market = fetch_market_stats()

    report = {
        "type": "intraday_breakout",
        "timestamp": datetime.now().isoformat(),
        "market": market,
        "breakouts": breakouts,
    }
    print(json.dumps(report, ensure_ascii=False, indent=None if args.json_only else 2))


if __name__ == "__main__":
    main()
