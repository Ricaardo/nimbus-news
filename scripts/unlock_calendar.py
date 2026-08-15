#!/usr/bin/env python3
"""
限售解禁日历
推送内容：
- 今日解禁：今天解禁的股票
- 本周解禁：本周解禁的重要股票
- 大额解禁预警：解禁市值大或占比高的个股
"""

import argparse
import json
import sqlite3
import sys
from dataclasses import dataclass, asdict
from datetime import datetime, timedelta
from typing import Dict, List, Optional
from pathlib import Path


@dataclass
class UnlockStock:
    """解禁股票信息"""
    code: str
    name: str
    unlock_date: str        # 解禁时间
    unlock_type: str        # 限售股类型
    unlock_amount: float    # 解禁数量(万股)
    unlock_value: float     # 解禁市值(亿元)
    ratio_total: float      # 占总市值比例(%)
    ratio_float: float      # 占流通市值比例(%)
    pre_close: float        # 解禁前收盘价
    pre_20d_change: float   # 解禁前20日涨跌幅(%)


class CacheManager:
    """缓存管理器"""

    def __init__(self, cache_dir: str = "data/cache"):
        self.cache_dir = Path(cache_dir)
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        self.db_path = self.cache_dir / "unlock_calendar_cache.db"
        self._init_db()

    def _init_db(self):
        with sqlite3.connect(self.db_path) as conn:
            conn.execute("""
                CREATE TABLE IF NOT EXISTS push_history (
                    id INTEGER PRIMARY KEY,
                    push_type TEXT,
                    push_date TEXT,
                    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                )
            """)

    def get_last_push_date(self, push_type: str) -> Optional[str]:
        with sqlite3.connect(self.db_path) as conn:
            cur = conn.execute(
                "SELECT push_date FROM push_history WHERE push_type=? ORDER BY id DESC LIMIT 1",
                (push_type,)
            )
            row = cur.fetchone()
            return row[0] if row else None

    def set_push_date(self, push_type: str, date: str):
        with sqlite3.connect(self.db_path) as conn:
            conn.execute(
                "INSERT INTO push_history (push_type, push_date) VALUES (?, ?)",
                (push_type, date)
            )


class UnlockCalendarFetcher:
    """解禁日历数据获取器"""

    def __init__(self, use_cache: bool = True, quiet: bool = False,
                 lookahead_days: int = 7, min_value: float = 1.0,
                 min_ratio: float = 5.0, exclude_boards: List[str] = None):
        """
        Args:
            min_value: 最小解禁市值(亿元)，用于过滤小额解禁
            min_ratio: 最小占比(%)，用于筛选大额解禁预警
        """
        self.quiet = quiet
        self.cache = CacheManager() if use_cache else None
        self.lookahead_days = lookahead_days
        self.min_value = min_value
        self.min_ratio = min_ratio
        self.exclude_boards = exclude_boards or []

        try:
            import akshare as ak
            self.ak = ak
        except ImportError:
            print("[ERROR] AKShare 未安装", file=sys.stderr)
            sys.exit(1)

    def _log(self, msg: str):
        if not self.quiet:
            print(msg, file=sys.stderr)

    def _safe_float(self, val) -> float:
        if val is None:
            return 0.0
        try:
            import math
            if isinstance(val, str):
                val = val.replace('%', '').replace(',', '').strip()
                if val == '' or val == '--' or val.lower() == 'nan':
                    return 0.0
            result = float(val)
            return 0.0 if math.isnan(result) else result
        except (ValueError, TypeError):
            return 0.0

    def _safe_date(self, val) -> str:
        if val is None:
            return ''
        try:
            import pandas as pd
            if pd.isna(val):
                return ''
            if hasattr(val, 'strftime'):
                return val.strftime('%Y-%m-%d')
            return str(val)[:10]
        except:
            return ''

    def _should_exclude(self, code: str, name: str) -> bool:
        code = str(code).zfill(6)
        for board in self.exclude_boards:
            board = board.lower()
            if board == 'kcb' and code.startswith('688'):
                return True
            if board == 'cyb' and code.startswith('300'):
                return True
            if board == 'bse' and (code.startswith('8') or code.startswith('43') or code.startswith('42')):
                return True
            if board == 'st' and 'ST' in name.upper():
                return True
        return False

    def get_unlock_list(self, start_date: str, end_date: str) -> List[UnlockStock]:
        """获取解禁列表"""
        stocks = []
        try:
            df = self.ak.stock_restricted_release_detail_em(
                start_date=start_date.replace('-', ''),
                end_date=end_date.replace('-', '')
            )
            if df is None or df.empty:
                return stocks

            self._log(f"[INFO] 获取到 {len(df)} 条解禁记录")

            for _, row in df.iterrows():
                code = str(row.get('股票代码', '')).zfill(6)
                name = str(row.get('股票简称', ''))

                if self._should_exclude(code, name):
                    continue

                unlock_value = self._safe_float(row.get('实际解禁市值', 0)) / 100000000  # 转为亿
                ratio_float = self._safe_float(row.get('占解禁前流通市值比例', 0))

                # 过滤小额解禁
                if unlock_value < self.min_value:
                    continue

                stocks.append(UnlockStock(
                    code=code,
                    name=name,
                    unlock_date=self._safe_date(row.get('解禁时间')),
                    unlock_type=str(row.get('限售股类型', '')),
                    unlock_amount=self._safe_float(row.get('实际解禁数量', 0)) / 10000,  # 转为万股
                    unlock_value=round(unlock_value, 2),
                    ratio_total=0,  # API没有直接提供
                    ratio_float=round(ratio_float * 100, 2),  # 转为百分比
                    pre_close=self._safe_float(row.get('解禁前一交易日收盘价', 0)),
                    pre_20d_change=self._safe_float(row.get('解禁前20日涨跌幅', 0))
                ))

        except Exception as e:
            self._log(f"[WARN] 获取解禁列表失败: {e}")

        return stocks

    def run(self) -> Dict:
        """执行数据获取"""
        today = datetime.now()
        today_str = today.strftime('%Y-%m-%d')

        # 计算本周范围
        week_start = today - timedelta(days=today.weekday())
        week_end = week_start + timedelta(days=6)

        # 未来7天
        future_date = (today + timedelta(days=self.lookahead_days)).strftime('%Y-%m-%d')

        # 检查是否需要推送
        should_push = True
        if self.cache:
            last_push = self.cache.get_last_push_date("unlock_calendar")
            if last_push == today_str:
                should_push = False
                self._log(f"[INFO] 今日已推送过解禁日历")

        # 获取今日到未来7天的数据
        all_stocks = self.get_unlock_list(today_str, future_date)

        # 分类
        today_unlock = []     # 今日解禁
        week_unlock = []      # 本周解禁
        big_unlock = []       # 大额解禁预警

        for stock in all_stocks:
            if stock.unlock_date == today_str:
                today_unlock.append(stock)
            if week_start.strftime('%Y-%m-%d') <= stock.unlock_date <= week_end.strftime('%Y-%m-%d'):
                week_unlock.append(stock)
            # 大额解禁：市值>10亿或占比>5%
            if stock.unlock_value >= 10 or stock.ratio_float >= self.min_ratio:
                big_unlock.append(stock)

        # 按解禁市值排序
        today_unlock.sort(key=lambda x: x.unlock_value, reverse=True)
        week_unlock.sort(key=lambda x: x.unlock_value, reverse=True)
        big_unlock.sort(key=lambda x: x.unlock_value, reverse=True)

        # 计算今日/本周总解禁市值
        today_total_value = sum(s.unlock_value for s in today_unlock)
        week_total_value = sum(s.unlock_value for s in week_unlock)

        # 保存推送记录
        has_content = today_unlock or big_unlock
        if self.cache and should_push and has_content:
            self.cache.set_push_date("unlock_calendar", today_str)

        result = {
            "type": "unlock_calendar_report",
            "timestamp": today.isoformat(),
            "date": today_str,
            "should_push": should_push and has_content,
            "summary": {
                "today_count": len(today_unlock),
                "today_value": round(today_total_value, 2),
                "week_count": len(week_unlock),
                "week_value": round(week_total_value, 2),
                "big_unlock_count": len(big_unlock)
            },
            "today_unlock": [asdict(s) for s in today_unlock[:15]],
            "week_unlock": [asdict(s) for s in week_unlock[:20]],
            "big_unlock": [asdict(s) for s in big_unlock[:10]]
        }

        return result


def main():
    parser = argparse.ArgumentParser(description='限售解禁日历')
    parser.add_argument('--json-only', action='store_true', help='仅输出JSON')
    parser.add_argument('--no-cache', action='store_true', help='禁用缓存')
    parser.add_argument('--quiet', '-q', action='store_true', help='静默模式')
    parser.add_argument('--force', action='store_true', help='强制推送')
    parser.add_argument('--lookahead', type=int, default=7, help='提前展示天数')
    parser.add_argument('--min-value', type=float, default=1.0, help='最小解禁市值(亿)')
    parser.add_argument('--min-ratio', type=float, default=5.0, help='大额解禁占比阈值(%)')
    parser.add_argument('--exclude-boards', type=str, default='', help='排除板块')

    args = parser.parse_args()
    exclude_boards = [b.strip() for b in args.exclude_boards.split(',') if b.strip()]

    fetcher = UnlockCalendarFetcher(
        use_cache=not args.no_cache,
        quiet=args.quiet or args.json_only,
        lookahead_days=args.lookahead,
        min_value=args.min_value,
        min_ratio=args.min_ratio,
        exclude_boards=exclude_boards
    )
    result = fetcher.run()

    if args.force:
        result['should_push'] = True

    print(json.dumps(result, ensure_ascii=False, indent=2, default=str))


if __name__ == "__main__":
    main()
