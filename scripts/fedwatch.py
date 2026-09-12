#!/usr/bin/env python3
"""FedWatch 利率概率(自建,免费)—— 30天联邦基金期货(ZQ)+ FRED EFFR。

方法(CME 简化版):
  - 期货隐含月均利率 = 100 - ZQ 合约价;取每次 FOMC「会议次月」合约 → 干净的会后利率。
  - 逐次会议增量 = 本次隐含 - 上次隐含(首次对当前 EFFR),按 25bp 折算 P(降/不变/升)。
  - 同时给出每次会议的累计隐含利率,呈现全年利率路径。
数据:yfinance ZQ{月码}{年}.CBT(全月度曲线)+ FRED EFFR(当前基准)。

输出中文摘要到 stdout,供 news 平台 script 源推送。
用法: python3 fedwatch.py [--json-only]
环境: DATAGW_URL (默认 http://127.0.0.1:8821; EFFR 经 Go 数据网关取)
"""
import argparse
import datetime
import json
import os
import sys
import urllib.request
import warnings

warnings.filterwarnings("ignore")
MONTH_CODE = {1: "F", 2: "G", 3: "H", 4: "J", 5: "K", 6: "M",
              7: "N", 8: "Q", 9: "U", 10: "V", 11: "X", 12: "Z"}

# ZQ 曲线磁盘缓存：Yahoo 偶发限流时复用上一份曲线(利率路径隔日几乎不变),
# 避免脚本静默无输出。每次成功拉取后回写。
CACHE_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                          ".fedwatch_zq_cache.json")


def _load_cache() -> tuple[dict, str | None]:
    try:
        with open(CACHE_FILE) as f:
            raw = json.load(f)
        curve = {(int(k.split("-")[0]), int(k.split("-")[1])): v
                 for k, v in raw.get("curve", {}).items()}
        return curve, raw.get("date")
    except Exception:  # noqa: BLE001
        return {}, None


def _save_cache(curve: dict) -> None:
    try:
        raw = {"date": datetime.date.today().isoformat(),
               "curve": {f"{y}-{m}": v for (y, m), v in curve.items()}}
        with open(CACHE_FILE, "w") as f:
            json.dump(raw, f)
    except Exception:  # noqa: BLE001
        pass

# FOMC 会议(date, 会议次月的 year, month)。脚本自动跳过已过期会议;
# 覆盖至 2027 以防跨年失效,新年度公布后在此追加即可。
FOMC = [
    ("2026-06-17", 2026, 7), ("2026-07-29", 2026, 8), ("2026-09-16", 2026, 10),
    ("2026-10-28", 2026, 11), ("2026-12-09", 2027, 1),
    ("2027-01-27", 2027, 2), ("2027-03-17", 2027, 4), ("2027-04-28", 2027, 5),
    ("2027-06-16", 2027, 7), ("2027-07-28", 2027, 8), ("2027-09-22", 2027, 10),
    ("2027-11-03", 2027, 11), ("2027-12-15", 2028, 1),
]


def effr_now() -> float | None:
    """Latest EFFR via the Go data gateway (datagw /macro), the single FRED
    fetch owner — replaces the direct keyed api.stlouisfed.org call."""
    base = os.environ.get("DATAGW_URL", "http://127.0.0.1:8821")
    url = f"{base}/macro?series=EFFR&limit=1"
    try:
        with urllib.request.urlopen(url, timeout=15) as r:
            obs = json.load(r)["data"]["observations"]
            if obs and obs[-1]["value"] is not None:
                return float(obs[-1]["value"])
    except Exception:  # noqa: BLE001
        # VPS fallback: use the existing FRED credential when the local gateway is unavailable.
        key = os.environ.get("FRED_API_KEY", "")
        if not key:
            return None
        try:
            import urllib.parse
            q = urllib.parse.urlencode({"series_id": "EFFR", "api_key": key, "file_type": "json", "limit": 1, "sort_order": "desc"})
            with urllib.request.urlopen("https://api.stlouisfed.org/fred/series/observations?" + q, timeout=15) as r:
                obs = json.load(r).get("observations", [])
            if obs and obs[0].get("value") not in (None, "."):
                return float(obs[0]["value"])
        except Exception:  # noqa: BLE001
            return None
    return None


def zq_curve(months: list[tuple[int, int]]) -> tuple[dict, str | None]:
    """一次性批量取所有需要的 ZQ 合约,返回 ({(year,month): implied_rate}, stale_date)。
    批量 yf.download 比逐只 history 请求数更少、更不易触发 Yahoo 限流。
    成功 → 回写缓存,stale_date=None;全部重试失败 → 回退缓存,stale_date=缓存日期。"""
    import contextlib
    import io
    import logging
    import time
    logging.getLogger("yfinance").setLevel(logging.CRITICAL)
    import yfinance as yf
    sym_map = {f"ZQ{MONTH_CODE[m]}{str(y)[2:]}.CBT": (y, m) for y, m in months}
    syms = list(sym_map)
    out: dict = {}
    for i in range(3):  # 偶发限流,整体重试
        try:
            # yfinance 会向 stdout 打印进度/警告 → 重定向,避免污染推送内容
            with contextlib.redirect_stdout(io.StringIO()), \
                 contextlib.redirect_stderr(io.StringIO()):
                df = yf.download(syms, period="5d", progress=False,
                                 threads=False, auto_adjust=True)
            close = df["Close"] if "Close" in df else df
            for s, ym in sym_map.items():
                try:
                    series = close[s].dropna() if s in close else None
                    if series is not None and len(series):
                        out[ym] = 100 - float(series.iloc[-1])
                except Exception:  # noqa: BLE001
                    pass
            if out:
                _save_cache(out)
                return out, None
        except Exception:  # noqa: BLE001
            pass
        time.sleep(2.0 * (i + 1))
    # 拉取失败 → 回退缓存,避免静默无输出
    cached, cdate = _load_cache()
    return cached, cdate


def probs(delta_bp: float) -> dict:
    """增量(bp)→ P(降/不变/升)。25bp 线性插值,>25bp 视为一次确定移动+下一档概率。"""
    step = delta_bp / 25.0
    if step <= -1:
        return {"cut": min(-step, 1) * 100 if -step <= 1 else 100, "hold": 0, "hike": 0}
    if step < 0:
        return {"cut": -step * 100, "hold": (1 + step) * 100, "hike": 0}
    if step == 0:
        return {"cut": 0, "hold": 100, "hike": 0}
    if step < 1:
        return {"cut": 0, "hold": (1 - step) * 100, "hike": step * 100}
    return {"cut": 0, "hold": 0, "hike": min(step, 1) * 100}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--json-only", action="store_true")
    args = ap.parse_args()

    base = effr_now()
    if base is None:
        print("WARNING: EFFR unavailable (datagw /macro down?)", file=sys.stderr)
        return 0

    today = datetime.date.today().isoformat()
    upcoming = [(d, y, m) for d, y, m in FOMC if d >= today][:6]  # 近 6 次会议
    curve, stale_date = zq_curve([(y, m) for _, y, m in upcoming])

    meetings = []
    prev = base
    for mdate, yy, mm in upcoming:
        implied = curve.get((yy, mm))
        if implied is None:
            continue
        delta_bp = (implied - prev) * 100
        meetings.append({"date": mdate, "implied": round(implied, 3),
                         "delta_bp": round(delta_bp, 1), "probs": probs(delta_bp)})
        prev = implied

    if not meetings:
        print("WARNING: no ZQ futures data (yfinance 限流且无缓存)", file=sys.stderr)
        return 0

    if args.json_only:
        print(json.dumps({"effr": base, "meetings": meetings,
                          "stale": stale_date}, ensure_ascii=False))
        return 0

    stale_note = f"  ⚠️数据取自缓存 {stale_date}(Yahoo 暂限流)" if stale_date else ""
    L = [f"🏦 FedWatch 利率路径(期货隐含 · 当前 EFFR {base:.2f}%){stale_note}", ""]
    for m in meetings:
        p = m["probs"]
        bias = max(p, key=p.get)
        tag = {"cut": "降息", "hold": "不变", "hike": "加息"}[bias]
        emoji = {"cut": "🟢", "hold": "⚪", "hike": "🔴"}[bias]
        L.append(f"{emoji} {m['date']}  隐含 {m['implied']:.2f}%  Δ{m['delta_bp']:+.0f}bp")
        L.append(f"     {tag} {p[bias]:.0f}%  "
                 f"(降{p['cut']:.0f}/平{p['hold']:.0f}/升{p['hike']:.0f})")
    total = (meetings[-1]["implied"] - base) * 100
    L += ["", f"年内累计隐含: {total:+.0f}bp → 终点 {meetings[-1]['implied']:.2f}%"]
    print("\n".join(L))
    return 0


if __name__ == "__main__":
    sys.exit(main())
