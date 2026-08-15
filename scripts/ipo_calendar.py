#!/usr/bin/env python3
"""
新股日历
推送内容：
- 今日申购：今天可以申购的新股
- 今日上市：今天上市的新股
- 近期申购：未来7天可申购的新股
- 中签公布：今天公布中签结果的新股
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
class IPOStock:
    """新股信息"""
    code: str
    name: str
    apply_date: str         # 申购日期
    list_date: str          # 上市日期
    price: float            # 发行价
    pe_ratio: float         # 发行市盈率
    win_rate: float         # 中签率(%)
    issue_amount: float     # 发行数量(万股)
    apply_limit: float      # 申购上限(万股)
    win_announce_date: str  # 中签公告日
    pay_date: str           # 中签缴款日


class CacheManager:
    """缓存管理器"""

    def __init__(self, cache_dir: str = "data/cache"):
        self.cache_dir = Path(cache_dir)
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        self.db_path = self.cache_dir / "ipo_calendar_cache.db"
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


class IPOCalendarFetcher:
    """新股日历数据获取器"""

    def __init__(self, use_cache: bool = True, quiet: bool = False,
                 lookahead_days: int = 7, exclude_boards: List[str] = None):
        self.quiet = quiet
        self.cache = CacheManager() if use_cache else None
        self.lookahead_days = lookahead_days
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

    def _should_exclude(self, code: str) -> bool:
        code = str(code).zfill(6)
        for board in self.exclude_boards:
            board = board.lower()
            if board == 'kcb' and code.startswith('688'):
                return True
            if board == 'cyb' and code.startswith('300'):
                return True
            if board == 'bse' and (code.startswith('8') or code.startswith('43') or code.startswith('42')):
                return True
        return False

    def get_ipo_list(self) -> List[IPOStock]:
        """获取新股列表"""
        stocks = []
        try:
            df = self.ak.stock_new_ipo_cninfo()
            if df is None or df.empty:
                return stocks

            self._log(f"[INFO] 获取到 {len(df)} 条新股记录")

            for _, row in df.iterrows():
                code = str(row.get('证劵代码', '')).zfill(6)
                if self._should_exclude(code):
                    continue

                stocks.append(IPOStock(
                    code=code,
                    name=str(row.get('证券简称', '')),
                    apply_date=self._safe_date(row.get('申购日期')),
                    list_date=self._safe_date(row.get('上市日期')),
                    price=self._safe_float(row.get('发行价')),
                    pe_ratio=self._safe_float(row.get('发行市盈率')),
                    win_rate=self._safe_float(row.get('上网发行中签率')),
                    issue_amount=self._safe_float(row.get('总发行数量')),
                    apply_limit=self._safe_float(row.get('网上申购上限')),
                    win_announce_date=self._safe_date(row.get('中签公告日')),
                    pay_date=self._safe_date(row.get('中签缴款日'))
                ))

        except Exception as e:
            self._log(f"[WARN] 获取新股列表失败: {e}")

        return stocks

    def run(self) -> Dict:
        """执行数据获取"""
        today = datetime.now()
        today_str = today.strftime('%Y-%m-%d')
        future_date = (today + timedelta(days=self.lookahead_days)).strftime('%Y-%m-%d')

        # 检查是否需要推送
        should_push = True
        if self.cache:
            last_push = self.cache.get_last_push_date("ipo_calendar")
            if last_push == today_str:
                should_push = False
                self._log(f"[INFO] 今日已推送过新股日历")

        # 获取数据
        all_stocks = self.get_ipo_list()

        # 分类
        today_apply = []      # 今日申购
        today_list = []       # 今日上市
        today_win = []        # 今日中签公布
        upcoming_apply = []   # 近期申购

        for stock in all_stocks:
            if stock.apply_date == today_str:
                today_apply.append(stock)
            elif stock.apply_date > today_str and stock.apply_date <= future_date:
                upcoming_apply.append(stock)

            if stock.list_date == today_str:
                today_list.append(stock)

            if stock.win_announce_date == today_str:
                today_win.append(stock)

        # 按日期排序
        upcoming_apply.sort(key=lambda x: x.apply_date)

        # 保存推送记录
        has_content = bool(today_apply or today_list or today_win or upcoming_apply)
        if self.cache and should_push and has_content:
            self.cache.set_push_date("ipo_calendar", today_str)

        result = {
            "type": "ipo_calendar_report",
            "timestamp": today.isoformat(),
            "date": today_str,
            "should_push": should_push and has_content,
            "summary": {
                "today_apply": len(today_apply),
                "today_list": len(today_list),
                "today_win": len(today_win),
                "upcoming_apply": len(upcoming_apply)
            },
            "today_apply": [asdict(s) for s in today_apply],
            "today_list": [asdict(s) for s in today_list],
            "today_win": [asdict(s) for s in today_win],
            "upcoming_apply": [asdict(s) for s in upcoming_apply[:10]]
        }

        return result


def main():
    parser = argparse.ArgumentParser(description='新股日历')
    parser.add_argument('--json-only', action='store_true', help='仅输出JSON')
    parser.add_argument('--no-cache', action='store_true', help='禁用缓存')
    parser.add_argument('--quiet', '-q', action='store_true', help='静默模式')
    parser.add_argument('--force', action='store_true', help='强制推送')
    parser.add_argument('--lookahead', type=int, default=7, help='提前展示天数')
    parser.add_argument('--exclude-boards', type=str, default='', help='排除板块')

    args = parser.parse_args()
    exclude_boards = [b.strip() for b in args.exclude_boards.split(',') if b.strip()]

    fetcher = IPOCalendarFetcher(
        use_cache=not args.no_cache,
        quiet=args.quiet or args.json_only,
        lookahead_days=args.lookahead,
        exclude_boards=exclude_boards
    )
    result = fetcher.run()

    if args.force:
        result['should_push'] = True

    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
