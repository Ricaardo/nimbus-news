#!/usr/bin/env python3
"""
block_trade.py — 大宗交易异动监控

16:30-17:00 拉取当日大宗交易数据:
- 机构席位参与 (买方 / 卖方)
- 异常折价率 > 3% (折价买入 = 机构抄底)
- 异常溢价率 > 3% (溢价接盘 = 机构承接)

输出 JSON:
{
  "type": "block_trade",
  "timestamp": "...",
  "should_push": bool,
  "summary": {total_count, total_amount_billion, institution_count},
  "institution_trades": [{code, name, buyer, seller, price, volume, amount, discount_pct, is_premium}],
  "significant": [...]  # 折溢价 > 3% 或单笔 > 1 亿
}
"""

import argparse
import json
import sys
from datetime import datetime
from pathlib import Path
from typing import Dict, List

sys.path.append(str(Path(__file__).parent))


def fetch_block_trades() -> List[Dict]:
    import akshare as ak
    import time
    today = datetime.now().strftime("%Y%m%d")
    for attempt in range(3):
        try:
            df = ak.stock_dzjy_mrmx(symbol="A股", start_date=today, end_date=today)
            if df is None or df.empty:
                return []
            out = []
            for _, r in df.iterrows():
                code = str(r.get("证券代码") or "").strip()
                name = str(r.get("证券简称") or "").strip()
                if not code or not name:
                    continue
                try:
                    price = float(r.get("成交价", 0) or 0)
                    volume = float(r.get("成交量", 0) or 0)
                    amount = float(r.get("成交额", 0) or 0)
                    close_price = float(r.get("收盘价", 0) or r.get("最新价", 0) or 0)
                    buyer = str(r.get("买方营业部", "") or "")
                    seller = str(r.get("卖方营业部", "") or "")
                except (ValueError, TypeError):
                    continue

                discount_pct = 0.0
                if close_price > 0:
                    discount_pct = (price - close_price) / close_price * 100

                is_institution = ("机构" in buyer) or ("机构" in seller)

                out.append({
                    "code": code,
                    "name": name,
                    "buyer": buyer,
                    "seller": seller,
                    "price": round(price, 2),
                    "volume": volume,
                    "amount": round(amount, 2),
                    "amount_billion": round(amount / 1e8, 3),
                    "close_price": round(close_price, 2),
                    "discount_pct": round(discount_pct, 2),
                    "is_institution": is_institution,
                    "is_premium": discount_pct > 0,
                })
            return out
        except Exception as e:
            if attempt < 2:
                time.sleep(2)
                continue
            print(f"[WARN] block trade: {e}", file=sys.stderr)
            return []


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--json-only", action="store_true")
    ap.add_argument("--threshold", type=float, default=3.0,
                    help="异常折溢价率阈值 (%)")
    ap.add_argument("--min-amount", type=float, default=1.0,
                    help="大额交易阈值 (亿)")
    args = ap.parse_args()

    trades = fetch_block_trades()
    if not trades:
        print(json.dumps({
            "type": "block_trade",
            "should_push": False,
            "summary": {"total_count": 0},
        }, ensure_ascii=False))
        return

    # 机构参与
    institution_trades = [t for t in trades if t["is_institution"]]

    # 异常: 折溢价大 或 单笔 > threshold
    significant = []
    for t in trades:
        if abs(t["discount_pct"]) >= args.threshold:
            significant.append(t)
        elif t["amount_billion"] >= args.min_amount:
            if t not in significant:
                significant.append(t)

    # 按交易额排序
    significant.sort(key=lambda x: x["amount_billion"], reverse=True)
    institution_trades.sort(key=lambda x: x["amount_billion"], reverse=True)

    total_amount = sum(t["amount_billion"] for t in trades)

    should_push = len(institution_trades) > 0 or len(significant) > 0

    report = {
        "type": "block_trade",
        "timestamp": datetime.now().isoformat(),
        "should_push": should_push,
        "summary": {
            "total_count": len(trades),
            "total_amount_billion": round(total_amount, 2),
            "institution_count": len(institution_trades),
            "significant_count": len(significant),
        },
        "institution_trades": institution_trades[:10],
        "significant": significant[:10],
    }

    print(json.dumps(report, ensure_ascii=False, indent=None if args.json_only else 2))


if __name__ == "__main__":
    main()
