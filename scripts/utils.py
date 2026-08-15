#!/usr/bin/env python3
"""
A股通用工具函数
提供交易日判断、日期格式化等功能
"""

import sqlite3
from datetime import datetime, timedelta
from pathlib import Path
from typing import Optional, Set
import sys


class TradingCalendar:
    """交易日历管理器（带缓存）"""

    _instance = None
    _cache_dir = Path("data/cache")

    def __new__(cls):
        if cls._instance is None:
            cls._instance = super().__new__(cls)
            cls._instance._initialized = False
        return cls._instance

    def __init__(self):
        if self._initialized:
            return
        self._initialized = True
        self._trading_days: Set[str] = set()
        self._last_update: Optional[datetime] = None
        self._cache_dir.mkdir(parents=True, exist_ok=True)
        self._db_path = self._cache_dir / "trading_calendar.db"
        self._init_db()
        self._load_from_cache()

    def _init_db(self):
        """初始化数据库"""
        with sqlite3.connect(self._db_path) as conn:
            conn.execute("""
                CREATE TABLE IF NOT EXISTS trading_days (
                    date TEXT PRIMARY KEY,
                    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                )
            """)
            conn.execute("""
                CREATE TABLE IF NOT EXISTS meta (
                    key TEXT PRIMARY KEY,
                    value TEXT
                )
            """)

    def _load_from_cache(self):
        """从缓存加载交易日历"""
        try:
            with sqlite3.connect(self._db_path) as conn:
                # 检查最后更新时间
                cur = conn.execute("SELECT value FROM meta WHERE key='last_update'")
                row = cur.fetchone()
                if row:
                    self._last_update = datetime.fromisoformat(row[0])

                # 加载交易日
                cur = conn.execute("SELECT date FROM trading_days")
                self._trading_days = {row[0] for row in cur.fetchall()}

        except Exception:
            pass

    def _save_to_cache(self, dates: Set[str]):
        """保存交易日历到缓存"""
        try:
            with sqlite3.connect(self._db_path) as conn:
                # 清空旧数据
                conn.execute("DELETE FROM trading_days")

                # 插入新数据
                conn.executemany(
                    "INSERT OR REPLACE INTO trading_days (date) VALUES (?)",
                    [(d,) for d in dates]
                )

                # 更新时间
                now = datetime.now().isoformat()
                conn.execute(
                    "INSERT OR REPLACE INTO meta (key, value) VALUES ('last_update', ?)",
                    (now,)
                )

        except Exception:
            pass

    def _should_refresh(self) -> bool:
        """判断是否需要刷新缓存"""
        if not self._trading_days:
            return True
        if self._last_update is None:
            return True
        # 每天刷新一次
        return (datetime.now() - self._last_update).days >= 1

    def _fetch_from_api(self) -> Set[str]:
        """从API获取交易日历"""
        dates = set()
        try:
            import akshare as ak
            df = ak.tool_trade_date_hist_sina()
            if df is not None and not df.empty:
                for _, row in df.iterrows():
                    trade_date = row.get('trade_date')
                    if trade_date:
                        if hasattr(trade_date, 'strftime'):
                            dates.add(trade_date.strftime('%Y-%m-%d'))
                        else:
                            dates.add(str(trade_date)[:10])
        except Exception as e:
            print(f"[WARN] 获取交易日历失败: {e}", file=sys.stderr)
        return dates

    def refresh(self, force: bool = False):
        """刷新交易日历"""
        if not force and not self._should_refresh():
            return

        dates = self._fetch_from_api()
        if dates:
            self._trading_days = dates
            self._last_update = datetime.now()
            self._save_to_cache(dates)

    def is_trading_day(self, date: Optional[datetime] = None) -> bool:
        """判断是否是交易日

        Args:
            date: 日期，默认为今天

        Returns:
            是否是交易日
        """
        if date is None:
            date = datetime.now()

        date_str = date.strftime('%Y-%m-%d')

        # 首先检查周末（快速过滤）
        if date.weekday() >= 5:  # 周六日
            return False

        # 刷新缓存
        self.refresh()

        # 如果缓存为空，按周末规则判断
        if not self._trading_days:
            return date.weekday() < 5

        return date_str in self._trading_days

    def get_last_trading_day(self, date: Optional[datetime] = None) -> str:
        """获取最近的交易日

        Args:
            date: 参考日期，默认为今天

        Returns:
            最近交易日的日期字符串 (YYYY-MM-DD)
        """
        if date is None:
            date = datetime.now()

        # 刷新缓存
        self.refresh()

        # 最多往前找30天
        for i in range(30):
            check_date = date - timedelta(days=i)
            if self.is_trading_day(check_date):
                return check_date.strftime('%Y-%m-%d')

        # 如果找不到，返回当前日期
        return date.strftime('%Y-%m-%d')

    def get_next_trading_day(self, date: Optional[datetime] = None) -> str:
        """获取下一个交易日

        Args:
            date: 参考日期，默认为今天

        Returns:
            下一个交易日的日期字符串 (YYYY-MM-DD)
        """
        if date is None:
            date = datetime.now()

        # 刷新缓存
        self.refresh()

        # 最多往后找30天
        for i in range(1, 30):
            check_date = date + timedelta(days=i)
            if self.is_trading_day(check_date):
                return check_date.strftime('%Y-%m-%d')

        # 如果找不到，返回明天
        return (date + timedelta(days=1)).strftime('%Y-%m-%d')


# 全局实例
_calendar: Optional[TradingCalendar] = None


def get_trading_calendar() -> TradingCalendar:
    """获取交易日历实例"""
    global _calendar
    if _calendar is None:
        _calendar = TradingCalendar()
    return _calendar


def is_trading_day(date: Optional[datetime] = None) -> bool:
    """判断是否是交易日

    Args:
        date: 日期，默认为今天

    Returns:
        是否是交易日
    """
    return get_trading_calendar().is_trading_day(date)


def get_last_trading_day(date: Optional[datetime] = None) -> str:
    """获取最近的交易日

    Args:
        date: 参考日期，默认为今天

    Returns:
        最近交易日的日期字符串 (YYYY-MM-DD)
    """
    return get_trading_calendar().get_last_trading_day(date)


def get_next_trading_day(date: Optional[datetime] = None) -> str:
    """获取下一个交易日

    Args:
        date: 参考日期，默认为今天

    Returns:
        下一个交易日的日期字符串 (YYYY-MM-DD)
    """
    return get_trading_calendar().get_next_trading_day(date)


# 日期格式化工具
def format_date(date: Optional[datetime] = None, fmt: str = '%Y-%m-%d') -> str:
    """格式化日期

    Args:
        date: 日期，默认为今天
        fmt: 格式字符串

    Returns:
        格式化后的日期字符串
    """
    if date is None:
        date = datetime.now()
    return date.strftime(fmt)


def parse_date(date_str: str) -> Optional[datetime]:
    """解析日期字符串

    支持的格式：
    - YYYY-MM-DD
    - YYYYMMDD
    - YYYY/MM/DD

    Args:
        date_str: 日期字符串

    Returns:
        datetime 对象，解析失败返回 None
    """
    formats = ['%Y-%m-%d', '%Y%m%d', '%Y/%m/%d']
    for fmt in formats:
        try:
            return datetime.strptime(date_str, fmt)
        except ValueError:
            continue
    return None


if __name__ == "__main__":
    # 测试
    print(f"今天是否是交易日: {is_trading_day()}")
    print(f"最近的交易日: {get_last_trading_day()}")
    print(f"下一个交易日: {get_next_trading_day()}")
