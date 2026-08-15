#!/usr/bin/env python3
"""
机构调研数据
追踪知名机构调研动向

数据内容：
- 近期机构调研汇总
- 调研机构数量排行
- 知名机构调研标的
"""

import argparse
import json
import os
import sqlite3
import sys
from dataclasses import dataclass, asdict
from datetime import datetime, timedelta
from typing import Dict, List, Optional, Set
from pathlib import Path

# 导入交易日判断工具
try:
    from utils import is_trading_day, get_last_trading_day
except ImportError:
    is_trading_day = lambda d=None: True
    get_last_trading_day = lambda d=None: datetime.now().strftime('%Y-%m-%d')


# 知名机构列表
FAMOUS_INSTITUTIONS = {
    "高瓴": "高瓴资本",
    "景林": "景林资产",
    "淡水泉": "淡水泉",
    "睿远": "睿远基金",
    "兴全": "兴全基金",
    "易方达": "易方达",
    "广发": "广发基金",
    "汇添富": "汇添富",
    "富国": "富国基金",
    "嘉实": "嘉实基金",
    "南方": "南方基金",
    "华夏": "华夏基金",
    "博时": "博时基金",
    "工银瑞信": "工银瑞信",
    "招商": "招商基金",
    "中欧": "中欧基金",
    "交银施罗德": "交银施罗德",
    "摩根士丹利": "摩根士丹利",
    "高盛": "高盛",
    "瑞银": "瑞银",
    "摩根大通": "摩根大通",
    "贝莱德": "贝莱德",
    "桥水": "桥水基金",
    "QFII": "QFII",
}


@dataclass
class InstitutionalVisit:
    """机构调研数据"""
    code: str
    name: str
    price: float
    change_pct: float
    institution_count: int    # 接待机构数量
    visit_method: str         # 接待方式
    visitors: str             # 接待人员
    visit_date: str           # 接待日期
    announce_date: str        # 公告日期
    famous_institutions: List[str]  # 知名机构列表


class CacheManager:
    """缓存管理器"""

    def __init__(self, db_path: str = "data/cache/institutional_research_cache.db"):
        self.db_path = Path(db_path)
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
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
            conn.execute("CREATE INDEX IF NOT EXISTS idx_push_date ON push_history(push_type, push_date)")

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


class InstitutionalResearchFetcher:
    """机构调研数据获取器"""

    def __init__(self, use_cache: bool = True, quiet: bool = False,
                 min_institution_count: int = 10):
        self.quiet = quiet
        self.cache = CacheManager() if use_cache else None
        self.min_institution_count = min_institution_count

        try:
            import akshare as ak
            self.ak = ak
        except ImportError:
            print("[ERROR] AKShare 未安装，请运行: pip install akshare", file=sys.stderr)
            sys.exit(1)

    def _log(self, msg: str):
        if not self.quiet:
            print(msg, file=sys.stderr)

    def _safe_float(self, val, default: float = 0.0) -> float:
        try:
            if val is None or str(val).strip() in ('', '-', 'nan', 'None'):
                return default
            return float(val)
        except (ValueError, TypeError):
            return default

    def _should_exclude(self, code: str, name: str) -> bool:
        """排除科创板、创业板、北交所、ST"""
        if code.startswith(('688', '689')):  # 科创板
            return True
        if code.startswith(('300', '301')):  # 创业板
            return True
        if code.startswith(('83', '87', '43')):  # 北交所
            return True
        if 'ST' in name or '*ST' in name:
            return True
        return False

    def _find_famous_institutions(self, visitors: str) -> List[str]:
        """从接待人员/机构信息中识别知名机构"""
        famous = []
        for key, name in FAMOUS_INSTITUTIONS.items():
            if key in visitors:
                famous.append(name)
        return famous

    def get_recent_visits(self, days: int = 7) -> List[InstitutionalVisit]:
        """获取近期机构调研"""
        visits = []

        try:
            # 使用年初作为起始日期获取数据
            start_date = (datetime.now() - timedelta(days=days)).strftime('%Y%m%d')

            self._log(f"[INFO] 获取机构调研数据...")
            df = self.ak.stock_jgdy_tj_em(date=start_date[:4] + "0101")

            if df is None or df.empty:
                return visits

            self._log(f"[INFO] 获取到 {len(df)} 条调研记录")

            # 过滤最近 N 天
            cutoff_date = (datetime.now() - timedelta(days=days)).strftime('%Y-%m-%d')

            for _, row in df.iterrows():
                code = str(row.get('代码', '')).zfill(6)
                name = str(row.get('名称', ''))

                if self._should_exclude(code, name):
                    continue

                visit_date = str(row.get('接待日期', ''))[:10]
                if visit_date < cutoff_date:
                    continue

                institution_count = int(row.get('接待机构数量', 0) or 0)

                # 获取详细信息中的机构列表
                visitors = str(row.get('接待人员', ''))
                famous = self._find_famous_institutions(visitors)

                visit = InstitutionalVisit(
                    code=code,
                    name=name,
                    price=self._safe_float(row.get('最新价', 0)),
                    change_pct=self._safe_float(row.get('涨跌幅', 0)),
                    institution_count=institution_count,
                    visit_method=str(row.get('接待方式', '')),
                    visitors=visitors[:100] if len(visitors) > 100 else visitors,
                    visit_date=visit_date,
                    announce_date=str(row.get('公告日期', ''))[:10],
                    famous_institutions=famous
                )
                visits.append(visit)

        except Exception as e:
            self._log(f"[WARN] 获取机构调研失败: {e}")

        # 按机构数量排序
        visits.sort(key=lambda x: x.institution_count, reverse=True)
        return visits

    def run(self) -> Dict:
        """执行数据获取"""
        now = datetime.now()
        today_str = now.strftime('%Y-%m-%d')

        # 检查是否是交易日
        if not is_trading_day(now):
            self._log("[INFO] 今天非交易日，跳过")
            return {
                "type": "institutional_research",
                "timestamp": now.isoformat(),
                "date": today_str,
                "should_push": False,
                "reason": "非交易日"
            }

        # 检查是否需要推送
        should_push = True
        if self.cache:
            last_push = self.cache.get_last_push_date("institutional_research")
            if last_push == today_str:
                should_push = False
                self._log(f"[INFO] 今日已推送过机构调研")

        # 获取数据
        visits = self.get_recent_visits(days=7)

        # 分类
        hot_visits = [v for v in visits if v.institution_count >= self.min_institution_count]
        famous_visits = [v for v in visits if v.famous_institutions]

        # 保存推送记录
        if self.cache and should_push and (hot_visits or famous_visits):
            self.cache.set_push_date("institutional_research", today_str)

        result = {
            "type": "institutional_research",
            "timestamp": now.isoformat(),
            "date": today_str,
            "should_push": should_push,
            "summary": {
                "total_visits": len(visits),
                "hot_visits": len(hot_visits),
                "famous_visits": len(famous_visits)
            },
            "hot_visits": [asdict(v) for v in hot_visits[:15]],
            "famous_visits": [asdict(v) for v in famous_visits[:10]]
        }

        self._log(f"[INFO] 调研统计: 总{len(visits)}, 热门{len(hot_visits)}, 知名机构{len(famous_visits)}")

        return result


def main():
    parser = argparse.ArgumentParser(description='机构调研数据')
    parser.add_argument('--json-only', action='store_true', help='仅输出JSON')
    parser.add_argument('--no-cache', action='store_true', help='禁用缓存')
    parser.add_argument('--quiet', '-q', action='store_true', help='静默模式')
    parser.add_argument('--days', type=int, default=7, help='查询天数')
    parser.add_argument('--min-count', type=int, default=10, help='最小机构数')

    args = parser.parse_args()

    fetcher = InstitutionalResearchFetcher(
        use_cache=not args.no_cache,
        quiet=args.quiet or args.json_only,
        min_institution_count=args.min_count
    )
    result = fetcher.run()

    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
