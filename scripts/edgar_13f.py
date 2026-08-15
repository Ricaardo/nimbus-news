#!/usr/bin/env python3
"""EDGAR 13F 机构持仓追踪。
检测指定 CIK 的最新 13F-HR 文件，与上次比对，仅在出现【新文件】时输出中文持仓变动摘要到 stdout。
无新文件 → 不输出（平台 script 源 stdout 为空则不推送）。

用法: python3 edgar_13f.py --cik 0002045724 --name "Situational Awareness LP"
状态存于 data/edgar_13f_state_<cik>.json
数据来源: signal-gateway (SIGNAL_GATEWAY_URL, 默认 http://127.0.0.1:8822)
"""
import argparse
import json
import os
import sys
import time
import urllib.request


def fetch_from_gateway(cik, gw_base):
    """从 signal-gateway 获取指定 CIK 的最新 13F 数据。
    返回 event dict 或抛出异常。
    """
    req_url = f"{gw_base}/fetch/13f?cik={cik.strip()}&periods=2&limit=0"
    req = urllib.request.Request(req_url, headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=20) as r:
        return json.loads(r.read())


def fmt_usd(v):
    if v >= 1e9:
        return f"${v/1e9:.2f}B"
    if v >= 1e6:
        return f"${v/1e6:.1f}M"
    if v >= 1e3:
        return f"${v/1e3:.0f}K"
    return f"${v}"


def process_one(cik, top):
    """处理单个 CIK：有新文件返回紧凑文本段，否则 None。"""
    cik10 = cik.strip().zfill(10)
    state_path = os.path.join(os.path.dirname(__file__), "..", "data", f"edgar_13f_state_{cik10}.json")

    gw_base = os.environ.get("SIGNAL_GATEWAY_URL", "http://127.0.0.1:8822")
    try:
        event = fetch_from_gateway(cik, gw_base)
    except Exception as e:
        sys.stderr.write(f"edgar_13f {cik} gateway error: {e}\n")
        return None

    facts = event.get("facts", {})
    periods = facts.get("periods", [])
    if not periods:
        return None

    latest = periods[0]  # newest-first
    accession = latest.get("accession", "")
    filing_date = latest.get("filing_date", "")
    report_date = latest.get("report_date", "")
    fund_name = event.get("name", "") or cik10

    # Convert holdings list → dict (same schema as stored state)
    cur = {}
    for h in latest.get("holdings", []):
        name = h.get("name", "")
        if not name:
            continue
        if name not in cur:
            cur[name] = {"value": 0, "shares": 0}
        cur[name]["value"] += int(h.get("value", 0) or 0)
        cur[name]["shares"] += int(h.get("shares", 0) or 0)

    if not accession or not cur:
        return None

    state = {}
    if os.path.exists(state_path):
        try:
            state = json.load(open(state_path))
        except Exception:
            state = {}
    if state.get("accession") == accession:
        return None  # 无新文件

    total = sum(h["value"] for h in cur.values())
    prev = state.get("holdings", {})

    seg = [f"● {fund_name} | {report_date or '?'} | {len(cur)}持仓 {fmt_usd(total)}"]
    top_list = sorted(cur.items(), key=lambda x: x[1]["value"], reverse=True)[:top]
    items = []
    for name, h in top_list:
        pct = (h["value"] / total * 100) if total else 0
        tag = ""
        if prev:
            if name not in prev:
                tag = "🆕"
            else:
                ds = h["shares"] - prev[name].get("shares", 0)
                tag = "➕" if ds > 0 else ("➖" if ds < 0 else "")
        items.append(f"{name} {pct:.0f}%{tag}")
    seg.append("  " + " · ".join(items))
    if prev:
        added = [n for n in cur if n not in prev]
        exited = [n for n in prev if n not in cur]
        if added:
            seg.append("  🆕新进: " + ", ".join(added[:6]) + ("…" if len(added) > 6 else ""))
        if exited:
            seg.append("  ❌清仓: " + ", ".join(exited[:6]) + ("…" if len(exited) > 6 else ""))

    os.makedirs(os.path.dirname(state_path), exist_ok=True)
    json.dump({"accession": accession, "filingDate": filing_date,
               "period": report_date, "name": fund_name, "holdings": cur},
              open(state_path, "w"), ensure_ascii=False)
    return "\n".join(seg)


def write_feed(ciks, top):
    """每次运行都把各基金当前 13F 摘要落到 nimbus feed（供 news-bridge skill 读）。"""
    feed_dir = os.path.expanduser("~/nimbus-os/nimbus/workspace/feed")
    funds = []
    for c in ciks:
        cik10 = c.strip().zfill(10)
        sp = os.path.join(os.path.dirname(__file__), "..", "data", f"edgar_13f_state_{cik10}.json")
        if not os.path.exists(sp):
            continue
        try:
            st = json.load(open(sp))
        except Exception:
            continue
        h = st.get("holdings", {})
        total = sum(x.get("value", 0) for x in h.values())
        topn = sorted(h.items(), key=lambda x: x[1].get("value", 0), reverse=True)[:top]
        funds.append({
            "name": st.get("name") or cik10,
            "period": st.get("period", ""),
            "total": fmt_usd(total),
            "top": [f"{n} {(v.get('value',0)/total*100 if total else 0):.0f}%" for n, v in topn],
        })
    if not funds:
        return
    try:
        os.makedirs(feed_dir, exist_ok=True)
        json.dump({"updated": time.strftime("%Y-%m-%d %H:%M"), "funds": funds},
                  open(os.path.join(feed_dir, "13f-latest.json"), "w"), ensure_ascii=False)
    except Exception as e:
        sys.stderr.write(f"write_feed error: {e}\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cik", required=True, help="单个或逗号分隔的多个 CIK")
    ap.add_argument("--top", type=int, default=6)
    args = ap.parse_args()

    ciks = [c for c in args.cik.split(",") if c.strip()]
    segments = []
    for c in ciks:
        try:
            seg = process_one(c, args.top)
            if seg:
                segments.append(seg)
        except Exception as e:
            sys.stderr.write(f"edgar_13f {c} error: {e}\n")

    write_feed(ciks, args.top)  # 每次都刷新 nimbus feed（即使无新申报）

    if not segments:
        return  # 无任何新文件 → 不向渠道推送（feed 已更新）
    header = f"🏛 机构 13F 更新（{len(segments)} 只新申报）"
    print(header + "\n\n" + "\n\n".join(segments))


if __name__ == "__main__":
    main()
