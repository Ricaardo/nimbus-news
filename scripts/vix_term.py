#!/usr/bin/env python3
"""VIX 恐慌指数 + 期限结构（经 data-access facade → market-hub）。

数据: facade `quote` 的 ^VIX9D/^VIX/^VIX3M/^VIX6M（market-hub Yahoo）。
术语: VIX3M>VIX = contango(正常/平静);VIX>VIX3M = backwardation(恐慌/risk-off)。
输出中文摘要到 stdout,供 news 平台 script 源推送。

用法: python3 vix_term.py [--json-only]
"""
import argparse
import json
import os
import sys

# (输出键, 中文标签, facade 规范符号)
TENORS = [("_VIX9D", "9日", "^VIX9D"), ("_VIX", "VIX", "^VIX"),
          ("_VIX3M", "3月", "^VIX3M"), ("_VIX6M", "6月", "^VIX6M")]

_data = None


def _facade():
    """Lazily import the data-access facade SDK (the single read path)."""
    global _data
    if _data is None:
        pkg = os.environ.get("DATA_ACCESS_PKG", os.path.expanduser("~/nimbus-os/services/data-access"))
        if pkg not in sys.path:
            sys.path.insert(0, pkg)
        from data_access import legacy as data  # noqa: PLC0415
        _data = data
    return _data


def fetch_all() -> dict:
    """{输出键: {current_price, price_change, price_change_percent}} via facade quote。"""
    try:
        by_fac = {q.get("symbol"): q for q in (_facade().quote(*[t[2] for t in TENORS]) or [])}
    except Exception as e:
        print(f"WARN: facade quote failed: {e}", file=sys.stderr)
        return {}
    out = {}
    for key, _, fac in TENORS:
        q = by_fac.get(fac) or {}
        out[key] = {"current_price": q.get("last"),
                    "price_change": q.get("change") or 0,
                    "price_change_percent": q.get("change_pct") or 0}
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--json-only", action="store_true")
    args = ap.parse_args()

    vals = fetch_all()
    prices = {k: v.get("current_price") for k, v in vals.items()}

    vix, vix3m = prices.get("_VIX"), prices.get("_VIX3M")
    if vix is None or vix3m is None or vix3m <= 0:
        return 0  # 数据缺失 → 空输出,不推送

    ratio = vix / vix3m
    if ratio < 0.90:
        regime, emoji, read = "深度 Contango", "🟢", "市场平静,波动率曲线陡峭向上,risk-on"
    elif ratio < 0.98:
        regime, emoji, read = "Contango", "🟢", "正常结构,无近端恐慌"
    elif ratio <= 1.02:
        regime, emoji, read = "走平", "🟡", "结构转平,留意风险事件临近"
    else:
        regime, emoji, read = "Backwardation", "🔴", "近端恐慌 > 远端,risk-off / 避险，常见于急跌"

    if args.json_only:
        print(json.dumps({"vix": vix, "vix3m": vix3m, "ratio": round(ratio, 3),
                          "regime": regime, "prices": prices}, ensure_ascii=False))
        return 0

    vix_d = vals.get("_VIX", {})
    chg = vix_d.get("price_change", 0)
    chg_pct = vix_d.get("price_change_percent", 0)
    arrow = "▲" if chg > 0 else "▼"

    lines = [f"{emoji} VIX 恐慌指数 · 期限结构【{regime}】",
             f"VIX {vix} {arrow}{abs(chg):.2f} ({chg_pct:+.1f}%)", ""]
    for _sym, label, fac in TENORS:
        p = prices.get(_sym)
        if p is not None:
            lines.append(f"  {label:>4}: {p:>6.2f}")
    lines += ["", f"VIX/3M = {ratio:.3f} → {read}"]
    print("\n".join(lines))
    return 0


if __name__ == "__main__":
    sys.exit(main())
