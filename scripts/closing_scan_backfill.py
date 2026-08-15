#!/usr/bin/env python3
"""
closing_scan_backfill.py

回灌过去 N 个交易日的涨停股特征 + 次日表现到 SQLite, 供 closing_scan.py 做 KNN / 分桶预测。

用法:
    # 首次初始化: 回灌 60 个交易日
    python3 scripts/closing_scan_backfill.py --days 60

    # 每日增量 (建议 16:00 cron): 只处理最近 2 个交易日
    python3 scripts/closing_scan_backfill.py --days 2

数据库:
    data/cache/closing_scan.db
        - limit_up_daily   T 日涨停股特征
        - next_day_perf    T+1 表现 (T 日视角)
        - meta             最后更新时间等

输出:
    JSON 摘要 {status, processed_days, inserted_limit_ups, inserted_perfs}
"""

import argparse
import json
import sqlite3
import sys
import time
from datetime import datetime, timedelta
from pathlib import Path
from typing import Dict, List, Optional

sys.path.append(str(Path(__file__).parent))
from utils import get_trading_calendar  # noqa: E402

DB_PATH = Path("data/cache/closing_scan.db")


def init_db(db_path: Path):
    db_path.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(db_path) as conn:
        conn.executescript("""
        CREATE TABLE IF NOT EXISTS limit_up_daily (
            date TEXT,
            code TEXT,
            name TEXT,
            streak INTEGER,
            seal_amount REAL,
            seal_ratio REAL,
            sector_name TEXT,
            sector_rank INTEGER,
            market_break_ratio REAL,
            has_lhb_institutional INTEGER DEFAULT 0,
            us_linkage_pct REAL DEFAULT 0,
            feature_vec TEXT,
            PRIMARY KEY (date, code)
        );
        CREATE INDEX IF NOT EXISTS idx_limit_up_date ON limit_up_daily(date);

        CREATE TABLE IF NOT EXISTS next_day_perf (
            date TEXT,
            code TEXT,
            open_pct REAL,
            high_pct REAL,
            close_pct REAL,
            PRIMARY KEY (date, code)
        );
        CREATE INDEX IF NOT EXISTS idx_perf_date ON next_day_perf(date);

        CREATE TABLE IF NOT EXISTS meta (
            key TEXT PRIMARY KEY,
            value TEXT
        );
        """)


def list_trading_days(end: datetime, n: int) -> List[str]:
    """从 end 往前取 n 个交易日, 返回按升序排列的日期字符串列表"""
    cal = get_trading_calendar()
    days = []
    d = end
    tries = 0
    while len(days) < n and tries < n * 3:
        if cal.is_trading_day(d):
            days.append(d.strftime("%Y-%m-%d"))
        d -= timedelta(days=1)
        tries += 1
    return sorted(days)


def fetch_limit_up_for_date(date_str: str) -> List[Dict]:
    """拉取指定交易日的涨停池, 带特征字段"""
    try:
        import akshare as ak
    except ImportError:
        print("[ERROR] 缺少 akshare: pip install akshare", file=sys.stderr)
        return []

    ymd = date_str.replace("-", "")
    try:
        df = ak.stock_zt_pool_em(date=ymd)
    except Exception as e:
        print(f"[WARN] stock_zt_pool_em {date_str}: {e}", file=sys.stderr)
        return []

    if df is None or df.empty:
        return []

    rows = []
    for _, r in df.iterrows():
        code = str(r.get("代码") or r.get("code") or "").strip()
        name = str(r.get("名称") or r.get("name") or "").strip()
        if not code or not name:
            continue
        # 连板数
        streak = int(r.get("连板数", 1) or 1)
        # 封单金额 (元)
        seal_amount = float(r.get("封板资金", 0) or 0)
        # 流通市值
        float_mv = float(r.get("流通市值", 0) or 0)
        seal_ratio = seal_amount / float_mv if float_mv > 0 else 0
        # 所属行业/概念 (akshare 不一定返回, 留待后续补)
        sector = str(r.get("所属行业", "") or "").strip()

        rows.append({
            "date": date_str,
            "code": code,
            "name": name,
            "streak": streak,
            "seal_amount": seal_amount,
            "seal_ratio": seal_ratio,
            "sector_name": sector,
            "sector_rank": 0,  # 历史 sector_rank 不好回灌, 设 0
            "market_break_ratio": 0,
            "has_lhb_institutional": 0,
            "us_linkage_pct": 0,
        })
    return rows


def compute_market_break_ratio(date_str: str) -> float:
    """计算当日炸板率 = 炸板数 / (炸板数 + 涨停数)"""
    try:
        import akshare as ak
    except ImportError:
        return 0.0

    ymd = date_str.replace("-", "")
    try:
        zt = ak.stock_zt_pool_em(date=ymd)
        zb = ak.stock_zt_pool_zbgc_em(date=ymd)
    except Exception:
        return 0.0

    zt_n = 0 if zt is None else len(zt)
    zb_n = 0 if zb is None else len(zb)
    denom = zt_n + zb_n
    if denom == 0:
        return 0.0
    return round(zb_n / denom, 3)


def fetch_next_day_perf(base_date: str, next_date: str, codes: List[str]) -> List[Dict]:
    """对 base_date 的涨停股, 拉 next_date 的开盘价 / 盘中最高价, 计算相对 base_date 收盘的涨幅"""
    if not codes:
        return []
    try:
        import akshare as ak
    except ImportError:
        return []

    out = []
    base_ymd = base_date.replace("-", "")
    next_ymd = next_date.replace("-", "")

    for code in codes:
        try:
            if code.startswith("6"):
                prefix = "sh"
            elif code.startswith(("8", "9")):
                prefix = "bj"
            else:
                prefix = "sz"
            df = ak.stock_zh_a_daily(symbol=prefix + code, adjust="")
            if df is None or df.empty or len(df) < 2:
                continue

            # 找到 base_date 和 next_date 对应行
            base_row = df[df["date"].astype(str).str.startswith(base_date)]
            next_row = df[df["date"].astype(str).str.startswith(next_date)]
            if base_row.empty or next_row.empty:
                continue

            base_close = float(base_row.iloc[0]["close"])
            if base_close <= 0:
                continue

            nr = next_row.iloc[0]
            next_open = float(nr["open"])
            next_high = float(nr["high"])
            next_close = float(nr["close"])

            open_pct = (next_open - base_close) / base_close * 100
            high_pct = (next_high - base_close) / base_close * 100
            close_pct = (next_close - base_close) / base_close * 100

            out.append({
                "date": base_date,
                "code": code,
                "open_pct": round(open_pct, 3),
                "high_pct": round(high_pct, 3),
                "close_pct": round(close_pct, 3),
            })
        except Exception as e:
            print(f"[WARN] hist {code} {base_date}: {e}", file=sys.stderr)
        time.sleep(0.15)

    return out


def feature_vec(row: Dict) -> str:
    """将 row 离散化为 6 维 bucket, 存成 JSON 字符串, 用于后续分桶/KNN 距离计算"""
    streak = row["streak"]
    if streak >= 4:
        streak_b = "4板+"
    elif streak == 3:
        streak_b = "3板"
    elif streak == 2:
        streak_b = "2板"
    else:
        streak_b = "1板"

    sr = row["seal_ratio"]
    if sr >= 0.05:
        sr_b = ">5%"
    elif sr >= 0.02:
        sr_b = "2-5%"
    else:
        sr_b = "<2%"

    rank = row.get("sector_rank", 0)
    if rank == 1:
        rank_b = "榜首"
    elif 2 <= rank <= 3:
        rank_b = "Top3"
    else:
        rank_b = "其他"

    mbr = row.get("market_break_ratio", 0)
    if mbr >= 0.4:
        mbr_b = ">40%"
    elif mbr >= 0.2:
        mbr_b = "20-40%"
    else:
        mbr_b = "炸板<20%"

    lhb_b = "有机构买入" if row.get("has_lhb_institutional") else "无"

    us = row.get("us_linkage_pct", 0)
    if us >= 2:
        us_b = "同概念美股>+2%"
    elif us <= -2:
        us_b = "<-2%"
    else:
        us_b = "持平"

    return json.dumps({
        "streak": streak_b,
        "seal_ratio": sr_b,
        "sector_rank": rank_b,
        "market_temp": mbr_b,
        "lhb_inst": lhb_b,
        "us_linkage": us_b,
    }, ensure_ascii=False)


def upsert_limit_up(conn: sqlite3.Connection, rows: List[Dict]):
    sql = """INSERT OR REPLACE INTO limit_up_daily
             (date, code, name, streak, seal_amount, seal_ratio, sector_name,
              sector_rank, market_break_ratio, has_lhb_institutional, us_linkage_pct, feature_vec)
             VALUES (?,?,?,?,?,?,?,?,?,?,?,?)"""
    conn.executemany(sql, [
        (r["date"], r["code"], r["name"], r["streak"], r["seal_amount"],
         r["seal_ratio"], r["sector_name"], r["sector_rank"],
         r["market_break_ratio"], r["has_lhb_institutional"],
         r["us_linkage_pct"], feature_vec(r))
        for r in rows
    ])


def upsert_perf(conn: sqlite3.Connection, rows: List[Dict]):
    sql = """INSERT OR REPLACE INTO next_day_perf
             (date, code, open_pct, high_pct, close_pct)
             VALUES (?,?,?,?,?)"""
    conn.executemany(sql, [
        (r["date"], r["code"], r["open_pct"], r["high_pct"], r["close_pct"])
        for r in rows
    ])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--days", type=int, default=60, help="回溯交易日数")
    ap.add_argument("--db", type=str, default=str(DB_PATH), help="SQLite 路径")
    ap.add_argument("--json-only", action="store_true", help="仅输出 JSON 摘要")
    args = ap.parse_args()

    db_path = Path(args.db)
    init_db(db_path)

    days = list_trading_days(datetime.now(), args.days + 1)  # 多取 1 天方便 next_day
    if len(days) < 2:
        print(json.dumps({"status": "error", "msg": "trading days < 2"}))
        return

    processed = 0
    inserted_lu = 0
    inserted_perf = 0

    with sqlite3.connect(db_path) as conn:
        for i, date in enumerate(days):
            if not args.json_only:
                print(f"[{i+1}/{len(days)}] processing {date}", file=sys.stderr)

            # T 日涨停池
            rows = fetch_limit_up_for_date(date)
            if rows:
                mbr = compute_market_break_ratio(date)
                for r in rows:
                    r["market_break_ratio"] = mbr
                upsert_limit_up(conn, rows)
                inserted_lu += len(rows)

            # 次日表现 (需要 T+1 是交易日, 用 days[i+1])
            if i + 1 < len(days) and rows:
                next_date = days[i + 1]
                codes = [r["code"] for r in rows]
                perfs = fetch_next_day_perf(date, next_date, codes)
                if perfs:
                    upsert_perf(conn, perfs)
                    inserted_perf += len(perfs)

            conn.commit()
            processed += 1

        conn.execute(
            "INSERT OR REPLACE INTO meta(key, value) VALUES('last_backfill', ?)",
            (datetime.now().isoformat(),)
        )
        conn.commit()

    summary = {
        "status": "ok",
        "processed_days": processed,
        "inserted_limit_ups": inserted_lu,
        "inserted_perfs": inserted_perf,
        "db": str(db_path),
    }
    print(json.dumps(summary, ensure_ascii=False))


if __name__ == "__main__":
    main()
