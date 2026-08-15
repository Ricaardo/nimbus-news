#!/usr/bin/env python3
"""超级投资者 13F 持仓追踪(Dataroma 式)—— SEC EDGAR 官方免费源,无 key。

对一组大师 CIK:
  - 取最近两份 13F-HR,解析 information table(按 cusip 聚合 value/shares);
  - 季度环比分类每只持仓:🆕新进 / ➕加仓 / ➖减仓 / ❌清仓;
  - Grand Portfolio:跨大师聚合,统计「被几位同时持有」+ 合计市值 → 市场共识。

输出中文摘要到 stdout,供 news 平台 script 源推送。
仅在有「新文件」或显著共识时输出(可 --always 强制)。

用法:
  python3 superinvestors.py --cik 0001067983,0001336528,... --top 12
  python3 superinvestors.py --cik <list> --json-only
"""
import argparse
import json
import os
import re
import sys
import time
import urllib.request
from collections import defaultdict

UA = "news-platform superinvestor-tracker contact@example.com"  # EDGAR 要求带 UA
SUB_URL = "https://data.sec.gov/submissions/CIK{cik:010d}.json"
ARCH = "https://www.sec.gov/Archives/edgar/data/{cik}/{accnd}/"
# datagw /sec-xbrl 是 data.sec.gov 的共享取数口(缓存/统一 UA)，不可用时直连兜底
# (2026-07 R2 T-4)。Archives 文件抓取不在其覆盖范围，保持直连。
DATAGW_URL = os.environ.get("DATAGW_URL", "http://127.0.0.1:8821")


def _submissions(cik: int) -> dict:
    """CIK submissions JSON：datagw-first，直连 SEC 兜底。"""
    try:
        with urllib.request.urlopen(
            f"{DATAGW_URL}/sec-xbrl?path=/submissions/CIK{cik:010d}.json", timeout=60
        ) as r:
            data = json.loads(r.read()).get("data")
            if data:
                return data
    except Exception:  # noqa: BLE001 — gateway down/unreachable
        pass
    return json.loads(_get(SUB_URL.format(cik=cik)))


def _get(url: str, retries: int = 3) -> bytes:
    for i in range(retries):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": UA,
                                                       "Accept-Encoding": "gzip, deflate"})
            with urllib.request.urlopen(req, timeout=25) as r:
                data = r.read()
                if r.headers.get("Content-Encoding") == "gzip":
                    import gzip
                    data = gzip.decompress(data)
                return data
        except Exception:  # noqa: BLE001
            if i == retries - 1:
                raise
            time.sleep(1.0 + i)
    return b""


def latest_13f(cik: int, n: int = 2) -> tuple[str, list[dict]]:
    """返回 (filer_name, [{accession, date}...])(最多 n 份,最新在前)。"""
    d = _submissions(cik)
    name = d.get("name", str(cik))
    r = d["filings"]["recent"]
    out = []
    for i, form in enumerate(r["form"]):
        if form == "13F-HR":
            out.append({"accession": r["accessionNumber"][i], "date": r["filingDate"][i]})
        if len(out) >= n:
            break
    return name, out


def parse_infotable(cik: int, accession: str) -> dict:
    """解析某份 13F 的 information table,按 cusip 聚合 → {cusip: {name, value, shares}}。"""
    accnd = accession.replace("-", "")
    base = ARCH.format(cik=cik, accnd=accnd)
    listing = json.loads(_get(base + "index.json"))
    xml_name = None
    for item in listing["directory"]["item"]:
        nm = item["name"]
        if nm.endswith(".xml") and nm != "primary_doc.xml":
            xml_name = nm
            break
    if not xml_name:
        return {}
    raw = _get(base + xml_name).decode("utf-8", "replace")
    # 剥掉标签命名空间前缀(兼容 <n1:infoTable> 与 <infoTable> 两种风格),
    # 再用正则按块抽取 —— 无视 xsi:schemaLocation 等属性,避免 XML 命名空间坑。
    raw = re.sub(r"<(/?)\w+:", r"<\1", raw)
    holdings: dict = defaultdict(lambda: {"name": "", "value": 0.0, "shares": 0.0})

    def _field(block: str, tag: str) -> str:
        m = re.search(rf"<{tag}>(.*?)</{tag}>", block, re.S)
        return m.group(1).strip() if m else ""

    for block in re.findall(r"<infoTable>(.*?)</infoTable>", raw, re.S):
        cusip = _field(block, "cusip").upper()
        if not cusip:
            continue
        h = holdings[cusip]
        h["name"] = h["name"] or _field(block, "nameOfIssuer")
        try:
            h["value"] += float(_field(block, "value") or 0)
            h["shares"] += float(_field(block, "sshPrnamt") or 0)
        except ValueError:
            pass
    return dict(holdings)


def classify(cur: dict, prev: dict) -> list[dict]:
    """季度环比分类。返回带 action 的持仓列表(按 value 降序)。"""
    rows = []
    for cusip, h in cur.items():
        p = prev.get(cusip)
        if p is None:
            action = "🆕新进"
        else:
            ds = h["shares"] - p["shares"]
            if ds > p["shares"] * 0.02:
                action = "➕加仓"
            elif ds < -p["shares"] * 0.02:
                action = "➖减仓"
            else:
                action = "持有"
        rows.append({"cusip": cusip, "name": h["name"], "value": h["value"],
                     "shares": h["shares"], "action": action})
    for cusip, p in prev.items():  # 清仓
        if cusip not in cur:
            rows.append({"cusip": cusip, "name": p["name"], "value": 0,
                         "shares": 0, "action": "❌清仓"})
    rows.sort(key=lambda x: x["value"], reverse=True)
    return rows


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cik", required=True, help="逗号分隔的大师 CIK")
    ap.add_argument("--top", type=int, default=12, help="Grand Portfolio 展示数")
    ap.add_argument("--per-guru", type=int, default=4, help="每位大师展示的变动数")
    ap.add_argument("--always", action="store_true", help="即便无新文件也输出")
    ap.add_argument("--json-only", action="store_true")
    args = ap.parse_args()

    ciks = [int(c) for c in args.cik.split(",") if c.strip()]
    grand_val: dict = defaultdict(float)
    grand_holders: dict = defaultdict(set)   # 按发行人名 → {guru_idx}(去重,合并股份类别)
    guru_blocks = []

    # 新文件检测:仅在某位大师出现【新 13F】时才推送(避免每日重复)
    state_path = os.path.join(os.path.dirname(__file__), "..", "data",
                              "superinvestors_state.json")
    try:
        with open(state_path) as f:
            state = json.load(f)
    except Exception:  # noqa: BLE001
        state = {}
    has_new = False

    for gi, cik in enumerate(ciks):
        try:
            name, filings = latest_13f(cik, 2)
            if not filings:
                continue
            if state.get(str(cik)) != filings[0]["accession"]:
                has_new = True
                state[str(cik)] = filings[0]["accession"]
            time.sleep(0.3)
            cur = parse_infotable(cik, filings[0]["accession"])
            prev = parse_infotable(cik, filings[1]["accession"]) if len(filings) > 1 else {}
            time.sleep(0.3)
        except Exception as e:  # noqa: BLE001
            print(f"WARNING: CIK {cik} failed: {e}", file=sys.stderr)
            continue
        if not cur:
            continue
        rows = classify(cur, prev)
        for h in cur.values():
            key = h["name"].upper().strip()
            grand_val[key] += h["value"]
            grand_holders[key].add(gi)
        moves = [r for r in rows if r["action"] in ("🆕新进", "➕加仓", "❌清仓")][:args.per_guru]
        guru_blocks.append({"name": name, "date": filings[0]["date"],
                            "top": rows[:3], "moves": moves})

    if not guru_blocks:
        return 0

    # 持久化最新 accession;无新文件且非 --always → 不输出(平台空 stdout 不推送)
    try:
        os.makedirs(os.path.dirname(state_path), exist_ok=True)
        with open(state_path, "w") as f:
            json.dump(state, f)
    except Exception:  # noqa: BLE001
        pass
    if not has_new and not args.always:
        return 0

    grand = sorted(grand_holders.keys(),
                   key=lambda k: (len(grand_holders[k]), grand_val[k]), reverse=True)

    if args.json_only:
        out = {"grand_portfolio": [{"name": k, "holders": len(grand_holders[k]),
                                    "total_value": grand_val[k]} for k in grand[:args.top]],
               "gurus": guru_blocks}
        print(json.dumps(out, ensure_ascii=False, default=str))
        return 0

    L = ["🏛️ 超级投资者持仓追踪(13F · SEC EDGAR)", ""]
    L.append(f"📊 Grand Portfolio · 共识持仓(共 {len(guru_blocks)} 位大师):")
    for k in grand[:args.top]:
        n = len(grand_holders[k])
        if n >= 2:  # 仅展示≥2 位共同持有
            L.append(f"  {n}位 | {k[:26]:<26} ${grand_val[k]/1e9:.1f}B")
    L.append("")
    for b in guru_blocks:
        L.append(f"— {b['name'][:34]} ({b['date']})")
        if b["moves"]:
            for m in b["moves"]:
                v = f"${m['value']/1e9:.2f}B" if m["value"] else "—"
                L.append(f"    {m['action']} {m['name'][:24]} {v}")
        else:
            top = ", ".join(t["name"][:14] for t in b["top"])
            L.append(f"    重仓: {top}")
    print("\n".join(L))
    return 0


if __name__ == "__main__":
    sys.exit(main())
