#!/usr/bin/env python3
"""
limit_up_ladder.py — 涨停梯队实时监控

盘中 11:00 / 13:30 扫描涨停池:
- 连板数 ≥ 5 的加速段股票 (高度票)
- 2 板梯队 vs 1 板梯队比例 (情绪)
- 炸板池大小 (警报)

只在以下条件触发推送 (避免空报):
- 出现 5+ 板股
- 2 板梯队 ≥ 5 只
- 炸板率 ≥ 40%

输出 JSON:
{
  "type": "limit_up_ladder",
  "timestamp": "...",
  "should_push": true,
  "trigger_reasons": [...],
  "ladder": {first, second, third_plus_detail: [{streak, count, stocks: [code, name]}]},
  "broken_count": N,
  "broken_ratio": 0.xx,
  "high_stocks": [{code, name, streak, price, change_pct, seal_amount, sector}]
}
"""

import argparse
import json
import sys
from datetime import datetime
from pathlib import Path
from typing import Dict, List

sys.path.append(str(Path(__file__).parent))


def fetch_limit_up_pool() -> List[Dict]:
    try:
        import akshare as ak
        df = ak.stock_zt_pool_em(date=datetime.now().strftime("%Y%m%d"))
        if df is None or df.empty:
            return []
        out = []
        for _, r in df.iterrows():
            code = str(r.get("代码") or "").strip()
            name = str(r.get("名称") or "").strip()
            if not code or not name:
                continue
            # 排除 ST / 北交所
            if "ST" in name.upper() or name.startswith("N"):
                continue
            if code.startswith(("83", "87", "88")):
                continue
            out.append({
                "code": code,
                "name": name,
                "streak": int(r.get("连板数", 1) or 1),
                "price": float(r.get("最新价", 0) or 0),
                "change_pct": float(r.get("涨跌幅", 0) or 0),
                "seal_amount": float(r.get("封板资金", 0) or 0),
                "sector": str(r.get("所属行业", "") or ""),
            })
        return out
    except Exception as e:
        print(f"[WARN] zt_pool: {e}", file=sys.stderr)
        return []


def fetch_broken_count() -> int:
    try:
        import akshare as ak
        df = ak.stock_zt_pool_zbgc_em(date=datetime.now().strftime("%Y%m%d"))
        return 0 if df is None else len(df)
    except Exception:
        return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--json-only", action="store_true")
    args = ap.parse_args()

    pool = fetch_limit_up_pool()
    broken = fetch_broken_count()

    # 梯队统计
    first_count = 0
    second_count = 0
    third_plus_map: Dict[int, List[Dict]] = {}
    high_stocks: List[Dict] = []

    for s in pool:
        streak = s["streak"]
        if streak == 1:
            first_count += 1
        elif streak == 2:
            second_count += 1
        else:
            third_plus_map.setdefault(streak, []).append(s)
            if streak >= 5:
                high_stocks.append(s)

    third_plus_detail = []
    for streak in sorted(third_plus_map.keys(), reverse=True):
        stocks = third_plus_map[streak]
        third_plus_detail.append({
            "streak": streak,
            "count": len(stocks),
            "stocks": [{"code": s["code"], "name": s["name"]} for s in stocks[:10]],
        })

    total = first_count + second_count + sum(len(v) for v in third_plus_map.values())
    broken_ratio = (broken / (total + broken)) if (total + broken) > 0 else 0

    # 触发条件
    trigger_reasons = []
    if len(high_stocks) > 0:
        trigger_reasons.append(f"出现 {len(high_stocks)} 只 5+ 板高度股")
    if second_count >= 5:
        trigger_reasons.append(f"2 板梯队 {second_count} 只, 接力意愿强")
    if broken_ratio >= 0.4:
        trigger_reasons.append(f"炸板率 {broken_ratio*100:.0f}% 警报")

    should_push = len(trigger_reasons) > 0

    report = {
        "type": "limit_up_ladder",
        "timestamp": datetime.now().isoformat(),
        "should_push": should_push,
        "trigger_reasons": trigger_reasons,
        "ladder": {
            "first": first_count,
            "second": second_count,
            "third_plus_detail": third_plus_detail,
        },
        "broken_count": broken,
        "broken_ratio": round(broken_ratio, 3),
        "high_stocks": high_stocks,
    }

    print(json.dumps(report, ensure_ascii=False, indent=None if args.json_only else 2))


if __name__ == "__main__":
    main()
