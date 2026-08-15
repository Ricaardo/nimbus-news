#!/usr/bin/env python3
"""
财报日历获取脚本

数据内容:
1. 业绩预告 - 预披露的业绩增减信息
2. 财报披露时间表 - 正式财报发布日期
3. 业绩快报 - 提前披露的财务数据

运行: python scripts/earnings_calendar.py --json-only
"""

import json
import os
import sys
import sqlite3
import argparse
from datetime import datetime, timedelta
from typing import Dict, List, Optional
from dataclasses import dataclass, asdict
import warnings
import threading

warnings.filterwarnings('ignore')


# ============================================
# 缓存层 - SQLite 本地缓存
# ============================================

class CacheManager:
    """SQLite 缓存管理器"""

    _instance = None
    _lock = threading.Lock()

    def __new__(cls, db_path: str = "data/cache/earnings_calendar_cache.db"):
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
                    cls._instance._initialized = False
        return cls._instance

    def __init__(self, db_path: str = "data/cache/earnings_calendar_cache.db"):
        if self._initialized:
            return

        self.db_path = db_path
        os.makedirs(os.path.dirname(db_path), exist_ok=True)
        self._init_db()
        self._initialized = True

    def _init_db(self):
        """初始化数据库表"""
        with sqlite3.connect(self.db_path) as conn:
            conn.execute("""
                CREATE TABLE IF NOT EXISTS earnings_forecast (
                    code TEXT,
                    name TEXT,
                    report_type TEXT,
                    forecast_type TEXT,
                    forecast_content TEXT,
                    change_range TEXT,
                    announce_date TEXT,
                    updated_at TEXT,
                    PRIMARY KEY (code, report_type)
                )
            """)
            conn.execute("""
                CREATE TABLE IF NOT EXISTS disclosure_schedule (
                    code TEXT,
                    name TEXT,
                    report_type TEXT,
                    plan_date TEXT,
                    actual_date TEXT,
                    updated_at TEXT,
                    PRIMARY KEY (code, report_type)
                )
            """)
            conn.execute("""
                CREATE TABLE IF NOT EXISTS last_push (
                    push_type TEXT PRIMARY KEY,
                    push_date TEXT,
                    push_time TEXT
                )
            """)
            conn.commit()

    def get_last_push_date(self, push_type: str) -> Optional[str]:
        """获取上次推送日期"""
        with sqlite3.connect(self.db_path) as conn:
            cursor = conn.execute(
                "SELECT push_date FROM last_push WHERE push_type = ?",
                (push_type,)
            )
            row = cursor.fetchone()
            return row[0] if row else None

    def set_last_push_date(self, push_type: str, push_date: str):
        """设置推送日期"""
        with sqlite3.connect(self.db_path) as conn:
            conn.execute("""
                INSERT OR REPLACE INTO last_push (push_type, push_date, push_time)
                VALUES (?, ?, ?)
            """, (push_type, push_date, datetime.now().isoformat()))
            conn.commit()


_cache_manager: Optional[CacheManager] = None


def get_cache_manager() -> CacheManager:
    global _cache_manager
    if _cache_manager is None:
        _cache_manager = CacheManager()
    return _cache_manager


# ============================================
# 数据模型
# ============================================

@dataclass
class EarningsForecast:
    """业绩预告"""
    code: str
    name: str
    report_type: str        # 年报/季报
    forecast_type: str      # 预增/预减/续亏等
    forecast_content: str   # 预告内容
    change_range: str       # 变动幅度
    change_pct: float = 0.0 # 变动百分比（用于排序）
    announce_date: str = ""
    is_significant: bool = False  # 是否为显著变动      # 公告日期


@dataclass
class DisclosureSchedule:
    """财报披露时间表"""
    code: str
    name: str
    report_type: str        # 年报/季报
    plan_date: str          # 计划披露日期
    actual_date: str = ""   # 实际披露日期


@dataclass
class EarningsExpress:
    """业绩快报"""
    code: str
    name: str
    report_type: str
    revenue: float          # 营业收入(亿)
    revenue_yoy: float      # 营收同比
    net_profit: float       # 净利润(亿)
    profit_yoy: float       # 净利润同比
    announce_date: str


# ============================================
# 数据获取器
# ============================================

class EarningsCalendarFetcher:
    """财报日历数据获取器"""

    def __init__(self, use_cache: bool = True, quiet: bool = False):
        self.ak = None
        self.cache = get_cache_manager() if use_cache else None
        self.quiet = quiet
        self._init_libs()

    def _init_libs(self):
        """初始化依赖库"""
        try:
            import akshare as ak
            self.ak = ak
        except ImportError:
            print("[ERROR] AKShare 未安装，请运行: pip install akshare", file=sys.stderr)
            sys.exit(1)

    def _log(self, msg: str):
        if not self.quiet:
            print(msg, file=sys.stderr)

    def get_earnings_forecast(self, report_type: str = "年报",
                               min_change_pct: float = 50.0,
                               max_results: int = 30,
                               exclude_boards: List[str] = None) -> List[EarningsForecast]:
        """获取业绩预告

        Args:
            report_type: 报告类型
            min_change_pct: 最小变动幅度（绝对值），低于此值不推送
            max_results: 最大返回数量
            exclude_boards: 排除的板块 ['kcb', 'cyb', 'bse', 'st']
        """
        if not self.ak:
            return []

        if exclude_boards is None:
            exclude_boards = ['kcb', 'cyb', 'bse', 'st']  # 默认排除科创/创业/北交/ST

        try:
            # 获取业绩预告数据
            df = self.ak.stock_yjyg_em(date=self._get_report_date(report_type))
            if df is None or df.empty:
                self._log(f"[INFO] 未获取到{report_type}业绩预告数据")
                return []

            results = []
            seen_codes = set()  # 去重：同一公司只保留一条

            for _, row in df.iterrows():
                code = str(row.get('股票代码', row.get('代码', ''))).zfill(6)
                name = str(row.get('股票简称', row.get('名称', '')))
                forecast_type = str(row.get('预测指标', row.get('业绩变动类型', '')))
                forecast_content = str(row.get('业绩变动', row.get('预告内容', '')))
                change_range = str(row.get('预测内容', row.get('业绩变动幅度', '')))
                announce_date = str(row.get('公告日期', ''))[:10]

                # 板块过滤
                if self._should_exclude(code, name, exclude_boards):
                    continue

                # 去重：同一公司只保留净利润相关的预告
                if code in seen_codes:
                    continue

                # 只保留"归属于上市公司股东的净利润"类型
                if '净利润' not in forecast_type and '扣除非经常性' not in forecast_type:
                    continue

                # 优先保留"归属于上市公司股东的净利润"
                if '扣除非经常性' in forecast_type and code in seen_codes:
                    continue

                # 解析变动百分比
                change_pct = self._parse_change_pct(change_range)

                # 判断是否显著变动
                is_significant = self._is_significant_change(forecast_content, change_pct, min_change_pct)

                if code and name and is_significant:
                    seen_codes.add(code)
                    results.append(EarningsForecast(
                        code=code,
                        name=name,
                        report_type=report_type,
                        forecast_type=forecast_type,
                        forecast_content=forecast_content,
                        change_range=change_range,
                        change_pct=change_pct,
                        announce_date=announce_date,
                        is_significant=is_significant
                    ))

            # 按变动幅度绝对值排序，取前 max_results 条
            results.sort(key=lambda x: abs(x.change_pct), reverse=True)
            results = results[:max_results]

            self._log(f"[INFO] 获取到 {len(results)} 条显著{report_type}业绩预告 (筛选后)")
            return results

        except Exception as e:
            self._log(f"[WARN] 获取业绩预告失败: {e}")
            return []

    def get_disclosure_schedule(self, target_date: str = None) -> List[DisclosureSchedule]:
        """获取财报披露时间表"""
        if not self.ak:
            return []

        if target_date is None:
            target_date = datetime.now().strftime('%Y%m%d')

        try:
            # 获取财报披露时间表
            df = self.ak.stock_report_disclosure(date=target_date[:6])
            if df is None or df.empty:
                self._log("[INFO] 未获取到财报披露时间表数据")
                return []

            results = []
            for _, row in df.iterrows():
                code = str(row.get('股票代码', row.get('代码', ''))).zfill(6)
                name = str(row.get('股票简称', row.get('名称', '')))
                plan_date = str(row.get('首次预约时间', row.get('计划披露日期', '')))[:10]
                actual_date = str(row.get('实际披露时间', row.get('实际披露日期', '')))[:10] if row.get('实际披露时间') else ""

                # 确定报告类型
                report_type = self._determine_report_type(target_date)

                if code and name:
                    results.append(DisclosureSchedule(
                        code=code,
                        name=name,
                        report_type=report_type,
                        plan_date=plan_date,
                        actual_date=actual_date
                    ))

            self._log(f"[INFO] 获取到 {len(results)} 条财报披露计划")
            return results

        except Exception as e:
            self._log(f"[WARN] 获取财报披露时间表失败: {e}")
            return []

    def get_earnings_express(self) -> List[EarningsExpress]:
        """获取业绩快报"""
        if not self.ak:
            return []

        try:
            df = self.ak.stock_yjkb_em(date=self._get_current_report_date())
            if df is None or df.empty:
                self._log("[INFO] 未获取到业绩快报数据")
                return []

            results = []
            for _, row in df.iterrows():
                code = str(row.get('股票代码', row.get('代码', ''))).zfill(6)
                name = str(row.get('股票简称', row.get('名称', '')))

                revenue = self._safe_float(row.get('营业收入-营业收入', row.get('营业收入', 0))) / 100000000
                revenue_yoy = self._safe_float(row.get('营业收入-同比增长', row.get('营收同比', 0)))
                net_profit = self._safe_float(row.get('净利润-净利润', row.get('净利润', 0))) / 100000000
                profit_yoy = self._safe_float(row.get('净利润-同比增长', row.get('净利润同比', 0)))
                announce_date = str(row.get('公告日期', ''))[:10]

                if code and name:
                    results.append(EarningsExpress(
                        code=code,
                        name=name,
                        report_type=self._determine_report_type(datetime.now().strftime('%Y%m%d')),
                        revenue=revenue,
                        revenue_yoy=revenue_yoy,
                        net_profit=net_profit,
                        profit_yoy=profit_yoy,
                        announce_date=announce_date
                    ))

            self._log(f"[INFO] 获取到 {len(results)} 条业绩快报")
            return results

        except Exception as e:
            self._log(f"[WARN] 获取业绩快报失败: {e}")
            return []

    def _get_report_date(self, report_type: str) -> str:
        """获取报告期日期"""
        now = datetime.now()
        year = now.year
        month = now.month

        if report_type == "年报":
            # 年报一般在次年1-4月披露
            if month <= 4:
                return f"{year-1}1231"
            else:
                return f"{year}1231"
        elif report_type == "三季报":
            if month <= 10:
                return f"{year}0930"
            else:
                return f"{year}0930"
        elif report_type == "中报":
            if month <= 8:
                return f"{year}0630"
            else:
                return f"{year}0630"
        else:  # 一季报
            if month <= 4:
                return f"{year}0331"
            else:
                return f"{year}0331"

    def _get_current_report_date(self) -> str:
        """获取当前报告期"""
        now = datetime.now()
        year = now.year
        month = now.month

        if month <= 4:
            return f"{year-1}1231"  # 年报
        elif month <= 7:
            return f"{year}0331"    # 一季报
        elif month <= 10:
            return f"{year}0630"    # 中报
        else:
            return f"{year}0930"    # 三季报

    def _determine_report_type(self, date_str: str) -> str:
        """根据日期确定报告类型"""
        if not date_str or len(date_str) < 6:
            return "年报"

        month = int(date_str[4:6]) if len(date_str) >= 6 else 1

        if month <= 4:
            return "年报"
        elif month <= 7:
            return "一季报"
        elif month <= 10:
            return "中报"
        else:
            return "三季报"

    def _safe_float(self, val) -> float:
        """安全转换为浮点数"""
        if val is None:
            return 0.0
        try:
            if isinstance(val, str):
                val = val.replace('%', '').replace(',', '').strip()
                if val == '' or val == '--' or val == 'nan':
                    return 0.0
            return float(val)
        except (ValueError, TypeError):
            return 0.0

    def _parse_change_pct(self, change_range: str) -> float:
        """解析变动百分比"""
        if not change_range or change_range == 'nan':
            return 0.0
        try:
            # change_range 可能是 "111.0" 或 "-36.87" 等
            return float(change_range)
        except (ValueError, TypeError):
            return 0.0

    def _should_exclude(self, code: str, name: str, exclude_boards: List[str]) -> bool:
        """判断是否应该排除该股票

        Args:
            code: 股票代码
            name: 股票名称
            exclude_boards: 排除的板块列表
                - 'kcb': 科创板 (688开头)
                - 'cyb': 创业板 (300开头)
                - 'bse': 北交所 (8开头, 4开头)
                - 'st': ST股票 (名称含ST/*ST)
                - 'b': B股 (200/900开头)
        """
        if not exclude_boards:
            return False

        # 科创板: 688开头
        if 'kcb' in exclude_boards and code.startswith('688'):
            return True

        # 创业板: 300开头
        if 'cyb' in exclude_boards and code.startswith('300'):
            return True

        # 北交所: 8开头(830/831/832/833/834/835/836/837/838/839), 4开头(430/420)
        if 'bse' in exclude_boards:
            if code.startswith('8') or code.startswith('43') or code.startswith('42'):
                return True

        # ST股票: 名称含ST或*ST
        if 'st' in exclude_boards:
            name_upper = name.upper()
            if 'ST' in name_upper or '*ST' in name_upper:
                return True

        # B股: 200开头(深B) 或 900开头(沪B)
        if 'b' in exclude_boards:
            if code.startswith('200') or code.startswith('900'):
                return True

        return False

    def _is_significant_change(self, forecast_content: str, change_pct: float,
                               min_change_pct: float) -> bool:
        """判断是否为显著变动"""
        # 1. 扭亏/首亏 都是显著变动
        if '扭亏' in forecast_content or '首亏' in forecast_content:
            return True

        # 2. 变动幅度超过阈值
        if abs(change_pct) >= min_change_pct:
            return True

        # 3. 预增/预减/续盈/续亏 关键词
        significant_keywords = ['预增', '预减', '大幅', '翻倍', '暴增', '暴跌']
        for kw in significant_keywords:
            if kw in forecast_content:
                return True

        return False

    def run(self, lookahead_days: int = 7, mode: str = "daily",
            min_change_pct: float = 50.0, max_forecasts: int = 30,
            exclude_boards: List[str] = None) -> Dict:
        """执行数据获取

        Args:
            lookahead_days: 提前展示的天数
            mode: daily (每日财报) 或 weekly (每周财报)
            min_change_pct: 业绩变动最小阈值(%)，低于此值不推送
            max_forecasts: 最大业绩预告数量
            exclude_boards: 排除的板块 ['kcb', 'cyb', 'bse', 'st', 'b']
        """
        if exclude_boards is None:
            exclude_boards = ['kcb', 'cyb', 'bse', 'st', 'b']  # 默认排除

        today = datetime.now()
        today_str = today.strftime('%Y-%m-%d')

        # 检查是否需要推送
        should_push = True
        if self.cache:
            last_push = self.cache.get_last_push_date(f"earnings_{mode}")
            if last_push == today_str:
                should_push = False
                self._log(f"[INFO] 今日已推送过 {mode} 财报日历")

        # 获取数据（带筛选）
        forecasts = self.get_earnings_forecast(
            min_change_pct=min_change_pct,
            max_results=max_forecasts,
            exclude_boards=exclude_boards
        )
        schedules = self.get_disclosure_schedule()
        express_list = self.get_earnings_express()

        # 筛选目标日期范围内的数据
        end_date = today + timedelta(days=lookahead_days)

        # 筛选今日披露的财报
        today_disclosures = [s for s in schedules if s.plan_date == today_str]

        # 筛选今日公告的业绩预告
        today_forecasts = [f for f in forecasts if f.announce_date == today_str]

        # 筛选未来一周的财报
        upcoming_disclosures = [
            s for s in schedules
            if s.plan_date and today_str < s.plan_date <= end_date.strftime('%Y-%m-%d')
        ]

        # 统计数据
        total_upcoming = len([s for s in schedules if s.plan_date and s.plan_date >= today_str])

        result = {
            "type": "earnings_calendar_report",
            "timestamp": datetime.now().isoformat(),
            "mode": mode,
            "date": today_str,
            "lookahead_days": lookahead_days,
            "should_push": should_push,
            "summary": {
                "today_disclosures": len(today_disclosures),
                "today_forecasts": len(today_forecasts),
                "upcoming_disclosures": len(upcoming_disclosures),
                "total_upcoming": total_upcoming,
            },
            "today_disclosures": [asdict(s) for s in today_disclosures[:20]],
            "today_forecasts": [asdict(f) for f in today_forecasts[:20]],
            "upcoming_disclosures": [asdict(s) for s in upcoming_disclosures[:30]],
            "earnings_express": [asdict(e) for e in express_list[:10]],
        }

        # 更新推送记录
        if should_push and self.cache:
            self.cache.set_last_push_date(f"earnings_{mode}", today_str)

        return result


def main():
    parser = argparse.ArgumentParser(description='财报日历获取')
    parser.add_argument('--lookahead', type=int, default=7, help='提前展示天数')
    parser.add_argument('--mode', type=str, default='daily', choices=['daily', 'weekly'],
                        help='模式: daily (每日) / weekly (每周)')
    parser.add_argument('--min-change', type=float, default=50.0,
                        help='业绩变动最小阈值(%%)，默认50%%')
    parser.add_argument('--max-forecasts', type=int, default=30,
                        help='最大业绩预告数量，默认30条')
    parser.add_argument('--exclude-boards', type=str, default='kcb,cyb,bse,st,b',
                        help='排除的板块，逗号分隔 (kcb=科创,cyb=创业,bse=北交,st=ST,b=B股)')
    parser.add_argument('--json-only', action='store_true', help='仅输出JSON')
    parser.add_argument('--no-cache', action='store_true', help='禁用缓存')
    parser.add_argument('--quiet', '-q', action='store_true', help='静默模式')
    parser.add_argument('--force', action='store_true', help='强制推送')

    args = parser.parse_args()

    exclude_boards = [b.strip() for b in args.exclude_boards.split(',') if b.strip()]

    fetcher = EarningsCalendarFetcher(
        use_cache=not args.no_cache,
        quiet=args.quiet or args.json_only
    )
    result = fetcher.run(
        lookahead_days=args.lookahead,
        mode=args.mode,
        min_change_pct=args.min_change,
        max_forecasts=args.max_forecasts,
        exclude_boards=exclude_boards
    )

    if args.force:
        result['should_push'] = True

    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
