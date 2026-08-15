#!/usr/bin/env python3
"""
closing_scan.py — 尾盘打板扫描

14:45 调用, 拉今日涨停池 + 昨日数据温度, 对每只候选股:
  1. 提取 6 维特征向量 (连板数/封单比/板块排名/炸板率/龙虎榜/美股联动)
  2. 在 limit_up_daily 历史表里查同特征的样本 → 分桶预测
  3. KNN 最近 20 个样本 → KNN 预测
  4. 置信度判断 + 分布统计
  5. 输出 JSON 给 Go 层渲染模板

用法:
    python3 scripts/closing_scan.py --json-only

依赖:
    data/cache/closing_scan.db (需先跑 closing_scan_backfill.py --days 60)
    akshare, pandas
"""

import argparse
import json
import math
import sqlite3
import sys
import time
from datetime import datetime
from pathlib import Path
from typing import Dict, List, Optional, Tuple

sys.path.append(str(Path(__file__).parent))
from utils import get_trading_calendar  # noqa: E402

DB_PATH = Path("data/cache/closing_scan.db")

# 特征离散化配置 (与 backfill 一致)
STREAK_BUCKETS = ["1板", "2板", "3板", "4板+"]
SEAL_BUCKETS = ["<2%", "2-5%", ">5%"]
RANK_BUCKETS = ["榜首", "Top3", "其他"]
MARKET_BUCKETS = ["炸板<20%", "20-40%", ">40%"]
LHB_BUCKETS = ["有机构买入", "无"]
US_BUCKETS = ["<-2%", "持平", "同概念美股>+2%"]

MIN_BUCKET_SAMPLES = 15  # 分桶最小样本阈值
KNN_K = 20               # KNN 近邻数
MIN_KNN_SAMPLES = 5      # KNN 最小样本阈值


def bucketize(row: Dict) -> Dict[str, str]:
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

    return {
        "streak": streak_b,
        "seal_ratio": sr_b,
        "sector_rank": rank_b,
        "market_temp": mbr_b,
        "lhb_inst": lhb_b,
        "us_linkage": us_b,
    }


def feature_to_vec(f: Dict[str, str]) -> List[int]:
    """把字符串特征映射成整数向量, 用于欧氏距离"""
    return [
        STREAK_BUCKETS.index(f["streak"]),
        SEAL_BUCKETS.index(f["seal_ratio"]),
        RANK_BUCKETS.index(f["sector_rank"]),
        MARKET_BUCKETS.index(f["market_temp"]),
        LHB_BUCKETS.index(f["lhb_inst"]),
        US_BUCKETS.index(f["us_linkage"]),
    ]


def load_history(db_path: Path) -> List[Dict]:
    """加载历史涨停 + 次日表现 join"""
    if not db_path.exists():
        return []
    with sqlite3.connect(db_path) as conn:
        conn.row_factory = sqlite3.Row
        cur = conn.execute("""
            SELECT l.date, l.code, l.feature_vec,
                   p.open_pct, p.high_pct, p.close_pct
            FROM limit_up_daily l
            JOIN next_day_perf p ON l.date=p.date AND l.code=p.code
        """)
        rows = []
        for r in cur:
            try:
                fv = json.loads(r["feature_vec"])
                rows.append({
                    "date": r["date"],
                    "code": r["code"],
                    "feat": fv,
                    "vec": feature_to_vec(fv),
                    "open_pct": r["open_pct"],
                    "high_pct": r["high_pct"],
                    "close_pct": r["close_pct"],
                })
            except (json.JSONDecodeError, ValueError, KeyError):
                continue
        return rows


def predict_bucket(cand_feat: Dict[str, str], history: List[Dict]) -> Optional[Dict]:
    """完全匹配同桶的历史样本"""
    same = [h for h in history if h["feat"] == cand_feat]
    if len(same) < MIN_BUCKET_SAMPLES:
        return None
    opens = [h["open_pct"] for h in same]
    highs = [h["high_pct"] for h in same]
    return {
        "open_median": median(opens),
        "open_std": stddev(opens),
        "high_median": median(highs),
        "high_std": stddev(highs),
        "win_rate": sum(1 for o in opens if o > 0) / len(opens),
        "n_samples": len(same),
        "distribution": distribution(opens),
    }


def predict_knn(cand_vec: List[int], history: List[Dict]) -> Optional[Dict]:
    """KNN K=20"""
    if len(history) < MIN_KNN_SAMPLES:
        return None
    pairs = [(euclidean(cand_vec, h["vec"]), h) for h in history]
    pairs.sort(key=lambda x: x[0])
    top_k = [p[1] for p in pairs[:KNN_K]]
    opens = [h["open_pct"] for h in top_k]
    highs = [h["high_pct"] for h in top_k]
    return {
        "open_median": median(opens),
        "high_median": median(highs),
        "win_rate": sum(1 for o in opens if o > 0) / len(opens),
        "n_samples": len(top_k),
    }


def euclidean(a: List[int], b: List[int]) -> float:
    return math.sqrt(sum((ai - bi) ** 2 for ai, bi in zip(a, b)))


def median(xs: List[float]) -> float:
    if not xs:
        return 0
    s = sorted(xs)
    n = len(s)
    return s[n // 2] if n % 2 == 1 else (s[n // 2 - 1] + s[n // 2]) / 2


def stddev(xs: List[float]) -> float:
    if len(xs) < 2:
        return 0
    m = sum(xs) / len(xs)
    var = sum((x - m) ** 2 for x in xs) / (len(xs) - 1)
    return math.sqrt(var)


def distribution(opens: List[float]) -> Dict[str, float]:
    """分布: 下跌 / 小涨(0-3%) / 中涨(3-7%) / 大涨(>7%)"""
    if not opens:
        return {}
    n = len(opens)
    buckets = {"下跌": 0, "小涨(0-3%)": 0, "中涨(3-7%)": 0, "大涨(>7%)": 0}
    for o in opens:
        if o < 0:
            buckets["下跌"] += 1
        elif o < 3:
            buckets["小涨(0-3%)"] += 1
        elif o < 7:
            buckets["中涨(3-7%)"] += 1
        else:
            buckets["大涨(>7%)"] += 1
    return {k: round(v / n, 3) for k, v in buckets.items()}


def judge_confidence(bucket: Optional[Dict], knn: Optional[Dict]) -> str:
    if bucket is None and knn is None:
        return "无数据"
    if bucket is None:
        return "低（样本不足，仅 KNN）"
    if knn is None:
        return "中"
    diff = abs(bucket["open_median"] - knn["open_median"])
    if diff < 2:
        return "高"
    return "中"


def fetch_today_context() -> Dict:
    """抓今日市场温度 + 昨日首板溢价"""
    try:
        import akshare as ak
    except ImportError:
        return {"zt_count": 0, "dt_count": 0, "today_break_ratio": 0,
                "prev_day_first_board_avg_open": 0, "main_theme": []}

    today = datetime.now().strftime("%Y%m%d")
    ctx = {"zt_count": 0, "dt_count": 0, "today_break_ratio": 0,
           "prev_day_first_board_avg_open": 0, "main_theme": []}

    try:
        zt = ak.stock_zt_pool_em(date=today)
        ctx["zt_count"] = 0 if zt is None else len(zt)
    except Exception:
        pass
    try:
        dt = ak.stock_zt_pool_dtgc_em(date=today)
        ctx["dt_count"] = 0 if dt is None else len(dt)
    except Exception:
        pass
    try:
        zb = ak.stock_zt_pool_zbgc_em(date=today)
        zb_n = 0 if zb is None else len(zb)
        denom = ctx["zt_count"] + zb_n
        if denom:
            ctx["today_break_ratio"] = round(zb_n / denom, 3)
    except Exception:
        pass

    # 前日首板今日平均开盘溢价
    cal = get_trading_calendar()
    try:
        from datetime import timedelta
        prev = datetime.now() - timedelta(days=1)
        while not cal.is_trading_day(prev):
            prev -= timedelta(days=1)
        prev_ymd = prev.strftime("%Y%m%d")
        prev_zt = ak.stock_zt_pool_em(date=prev_ymd)
        if prev_zt is not None and not prev_zt.empty:
            first_boards = prev_zt[prev_zt.get("连板数", 1) == 1]
            if first_boards is not None and not first_boards.empty:
                premiums = []
                for _, r in first_boards.head(30).iterrows():
                    code = str(r.get("代码") or "").strip()
                    if not code:
                        continue
                    try:
                        df = ak.stock_zh_a_hist(
                            symbol=code, period="daily",
                            start_date=prev_ymd, end_date=today, adjust="qfq")
                        if df is None or df.empty or len(df) < 2:
                            continue
                        prev_close = float(df.iloc[0]["收盘"])
                        today_open = float(df.iloc[-1]["开盘"])
                        if prev_close > 0:
                            premiums.append((today_open - prev_close) / prev_close * 100)
                    except Exception:
                        pass
                    time.sleep(0.1)
                if premiums:
                    ctx["prev_day_first_board_avg_open"] = round(sum(premiums) / len(premiums), 2)
    except Exception as e:
        print(f"[WARN] prev first-board: {e}", file=sys.stderr)

    # 主线板块 (涨停家数最多的概念)
    try:
        if zt is not None and not zt.empty and "所属行业" in zt.columns:
            counts = zt["所属行业"].value_counts().head(2)
            ctx["main_theme"] = counts.index.tolist()
    except Exception:
        pass

    # 涨停天梯统计 (1 板 / 2 板 / ≥3 板)
    ladder = {"first": 0, "second": 0, "third_plus": 0}
    try:
        if zt is not None and not zt.empty and "连板数" in zt.columns:
            for _, r in zt.iterrows():
                streak = int(r.get("连板数", 1) or 1)
                if streak >= 3:
                    ladder["third_plus"] += 1
                elif streak == 2:
                    ladder["second"] += 1
                else:
                    ladder["first"] += 1
    except Exception:
        pass
    ctx["ladder"] = ladder
    # 断板风险: 2 板及以上 / 1 板 比例; 比例越低说明高位难以延续
    total = ladder["first"] + ladder["second"] + ladder["third_plus"]
    if total > 0:
        ctx["breakage_risk"] = round(
            (ladder["second"] + ladder["third_plus"]) / total, 3)
    else:
        ctx["breakage_risk"] = 0

    return ctx


def fetch_today_candidates() -> List[Dict]:
    """抓今日涨停池 + 特征"""
    try:
        import akshare as ak
    except ImportError:
        return []

    today = datetime.now().strftime("%Y%m%d")

    try:
        df = ak.stock_zt_pool_em(date=today)
    except Exception as e:
        print(f"[WARN] zt_pool: {e}", file=sys.stderr)
        return []

    if df is None or df.empty:
        return []

    # 今日炸板池, 用于排除
    broken_codes = set()
    try:
        zb = ak.stock_zt_pool_zbgc_em(date=today)
        if zb is not None and not zb.empty:
            for _, r in zb.iterrows():
                c = str(r.get("代码") or "").strip()
                if c:
                    broken_codes.add(c)
    except Exception:
        pass

    mbr = 0.0
    try:
        zb_n = 0 if zb is None else len(zb)
        denom = len(df) + zb_n
        if denom:
            mbr = round(zb_n / denom, 3)
    except Exception:
        pass

    # 板块涨幅榜, 用于 sector_rank
    sector_ranks = {}
    try:
        boards = ak.stock_board_concept_name_em()
        if boards is not None and not boards.empty:
            boards = boards.sort_values("涨跌幅", ascending=False).reset_index(drop=True)
            for i, r in boards.head(20).iterrows():
                name = str(r.get("板块名称") or "").strip()
                if name:
                    sector_ranks[name] = i + 1
    except Exception:
        pass

    out = []
    for _, r in df.iterrows():
        code = str(r.get("代码") or "").strip()
        name = str(r.get("名称") or "").strip()
        if not code or not name:
            continue
        if code in broken_codes:
            continue

        streak = int(r.get("连板数", 1) or 1)
        seal_amount = float(r.get("封板资金", 0) or 0)
        float_mv = float(r.get("流通市值", 0) or 0)
        seal_ratio = seal_amount / float_mv if float_mv > 0 else 0
        sector = str(r.get("所属行业", "") or "").strip()
        sector_rank = sector_ranks.get(sector, 0)

        # 硬过滤: ST / 次新
        if "ST" in name.upper() or name.startswith("N"):
            continue
        # 北交所
        if code.startswith(("83", "87", "88")):
            continue

        out.append({
            "code": code,
            "name": name,
            "streak": streak,
            "seal_amount": seal_amount,
            "seal_ratio": seal_ratio,
            "sector_name": sector,
            "sector_rank": sector_rank,
            "market_break_ratio": mbr,
            "has_lhb_institutional": 0,  # 下面 enrich
            "us_linkage_pct": 0,         # Go 层负责补充美股联动
        })

    # 富化: 批量查今日龙虎榜机构买入
    try:
        import akshare as ak
        lhb_df = ak.stock_lhb_detail_em(
            start_date=datetime.now().strftime("%Y%m%d"),
            end_date=datetime.now().strftime("%Y%m%d"),
        )
        if lhb_df is not None and not lhb_df.empty:
            # 判断是否有机构买入: 在 LHB 列表里且有买方/卖方机构席位
            inst_codes = set()
            for _, lr in lhb_df.iterrows():
                c = str(lr.get("代码") or "").strip()
                reason = str(lr.get("上榜原因") or "")
                # 机构席位通常包含 "机构" 字样
                if c and "机构" in reason:
                    inst_codes.add(c)
            for cand in out:
                if cand["code"] in inst_codes:
                    cand["has_lhb_institutional"] = 1
    except Exception as e:
        print(f"[WARN] lhb enrich: {e}", file=sys.stderr)

    return out


def compute_score(cand: Dict, pred: Optional[Dict]) -> int:
    """0-100 评分"""
    score = 0
    # 涨停质量
    if cand["seal_ratio"] >= 0.05:
        score += 25
    elif cand["seal_ratio"] >= 0.02:
        score += 15
    else:
        score += 5

    # 市场温度 (炸板率低加分)
    mbr = cand.get("market_break_ratio", 0)
    if mbr < 0.2:
        score += 18
    elif mbr < 0.4:
        score += 10

    # 板块联动
    rank = cand.get("sector_rank", 0)
    if rank == 1:
        score += 15
    elif 2 <= rank <= 3:
        score += 10
    elif 4 <= rank <= 10:
        score += 5

    # 连板梯队
    streak = cand["streak"]
    if streak == 2:
        score += 10
    elif streak == 3:
        score += 12
    elif streak == 4:
        score += 8
    elif streak >= 5:
        score += 3  # 5 板以上加速风险
    else:
        score += 6  # 首板

    # 预测加成
    if pred and pred.get("bucket"):
        win = pred["bucket"].get("win_rate", 0)
        score += int(win * 15)
    elif pred and pred.get("knn"):
        win = pred["knn"].get("win_rate", 0)
        score += int(win * 10)

    return min(100, score)


def build_reasons(cand: Dict) -> List[str]:
    reasons = []
    if cand.get("sector_rank") == 1:
        reasons.append(f"{cand['sector_name']} 板块龙一")
    elif 2 <= cand.get("sector_rank", 99) <= 3:
        reasons.append(f"{cand['sector_name']} 板块 Top3")
    if cand["seal_ratio"] >= 0.05:
        reasons.append(f"封单 {cand['seal_amount'] / 1e8:.1f}亿 ({cand['seal_ratio']*100:.1f}% 市值)")
    elif cand["seal_ratio"] >= 0.02:
        reasons.append(f"封单 {cand['seal_amount'] / 1e8:.1f}亿")
    if cand["streak"] >= 2:
        reasons.append(f"{cand['streak']} 连板")
    return reasons


def build_warnings(cand: Dict, ladder: Dict[str, int]) -> List[str]:
    """基于当前候选股 + 全市场天梯结构, 生成风险提示"""
    warnings: List[str] = []

    streak = cand.get("streak", 1)

    # 5+ 板加速段, 接力风险高
    if streak >= 5:
        warnings.append(f"{streak} 板加速段, 接力风险高")

    # 孤票: 高位但全市场同梯队只有它一只
    if streak >= 3:
        third_plus = ladder.get("third_plus", 0)
        if third_plus <= 2:
            warnings.append(f"{streak} 板孤票 (全市场 ≥3 板仅 {third_plus} 只), 无梯队支撑")

    # 断板环境: 1 板占比 > 85% 说明市场无接力意愿
    first = ladder.get("first", 0)
    total = first + ladder.get("second", 0) + ladder.get("third_plus", 0)
    if total >= 10 and first / total > 0.85 and streak >= 2:
        warnings.append(f"断板环境 (1 板占比 {first / total * 100:.0f}%), 连板承压")

    # 封单弱: seal_ratio < 1% 且连板 > 1, 明日开盘可能抛压
    if streak >= 2 and cand.get("seal_ratio", 0) < 0.01:
        warnings.append("封单薄弱 (< 1% 市值), 明日抛压风险")

    return warnings


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", type=str, default=str(DB_PATH))
    ap.add_argument("--json-only", action="store_true")
    ap.add_argument("--top", type=int, default=10, help="输出 Top N 候选")
    args = ap.parse_args()

    history = load_history(Path(args.db))
    if not args.json_only:
        print(f"[INFO] history loaded: {len(history)} samples", file=sys.stderr)

    ctx = fetch_today_context()
    candidates = fetch_today_candidates()

    ladder = ctx.get("ladder", {})

    scored = []
    for c in candidates:
        feat = bucketize(c)
        vec = feature_to_vec(feat)
        bucket = predict_bucket(feat, history)
        knn = predict_knn(vec, history)
        prediction = {
            "bucket": bucket,
            "knn": knn,
            "confidence": judge_confidence(bucket, knn),
        }
        score = compute_score(c, prediction)
        warnings = build_warnings(c, ladder)
        scored.append({
            **c,
            "score": score,
            "prediction": prediction,
            "reasons": build_reasons(c),
            "warnings": warnings,
        })

    scored.sort(key=lambda x: x["score"], reverse=True)

    high_risk = [s for s in scored if s["streak"] >= 5 or "孤票" in "".join(s["warnings"])]

    report = {
        "type": "closing_scan_report",
        "timestamp": datetime.now().isoformat(),
        "market_temp": ctx,
        "history_samples": len(history),
        "candidates": scored[:args.top],
        "excluded": [],
        "high_risk": high_risk[:5],
    }

    print(json.dumps(report, ensure_ascii=False, indent=2 if not args.json_only else None))


if __name__ == "__main__":
    main()
