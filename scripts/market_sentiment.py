#!/usr/bin/env python3
"""
市场情绪/盘面总览
每日收盘后推送，提供市场整体感知

数据内容：
- 大盘指数表现（上证、深证、创业板、科创50）
- 涨跌家数（上涨/下跌/平盘）
- 涨跌停统计（涨停数、跌停数、连板数、炸板率）
- 成交额变化
- 情绪温度计（综合评分 0-100）
"""

import argparse
import json
import os
import sqlite3
import sys
from dataclasses import dataclass, asdict, field
from datetime import datetime, timedelta
from typing import Dict, List, Optional
from pathlib import Path

# 导入交易日判断工具
try:
    from utils import is_trading_day
except ImportError:
    is_trading_day = lambda d=None: True  # 降级：假设都是交易日


@dataclass
class IndexData:
    """指数数据"""
    code: str
    name: str
    close: float
    change: float
    change_pct: float
    volume: float  # 成交额(亿)


@dataclass
class MarketBreadth:
    """市场宽度"""
    up_count: int       # 上涨家数
    down_count: int     # 下跌家数
    flat_count: int     # 平盘家数
    total_count: int    # 总数
    up_ratio: float     # 上涨比例


@dataclass
class LimitStats:
    """涨跌停统计"""
    limit_up: int           # 涨停数
    limit_down: int         # 跌停数
    limit_up_broken: int    # 炸板数（曾涨停后打开）
    continuous_up: int      # 连板数（2板及以上）
    max_continuous: int     # 最高连板数
    broken_ratio: float     # 炸板率


@dataclass
class MarketSentiment:
    """市场情绪综合"""
    score: int              # 情绪分数 0-100
    level: str              # 情绪等级: 极度恐慌/恐慌/谨慎/中性/乐观/贪婪/极度贪婪
    description: str        # 情绪描述


class CacheManager:
    """缓存管理器"""

    def __init__(self, cache_dir: str = "data/cache"):
        self.cache_dir = Path(cache_dir)
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        self.db_path = self.cache_dir / "market_sentiment_cache.db"
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
            conn.execute("""
                CREATE TABLE IF NOT EXISTS market_data (
                    id INTEGER PRIMARY KEY,
                    date TEXT,
                    data_json TEXT,
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

    def get_prev_volume(self, date: str) -> Optional[float]:
        """获取前一日成交额"""
        with sqlite3.connect(self.db_path) as conn:
            cur = conn.execute(
                "SELECT data_json FROM market_data WHERE date < ? ORDER BY date DESC LIMIT 1",
                (date,)
            )
            row = cur.fetchone()
            if row:
                data = json.loads(row[0])
                return data.get('total_volume', 0)
            return None

    def save_market_data(self, date: str, data: dict):
        with sqlite3.connect(self.db_path) as conn:
            conn.execute(
                "INSERT OR REPLACE INTO market_data (date, data_json) VALUES (?, ?)",
                (date, json.dumps(data, ensure_ascii=False))
            )


class MarketSentimentFetcher:
    """市场情绪数据获取器"""

    def __init__(self, use_cache: bool = True, quiet: bool = False):
        self.quiet = quiet
        self.cache = CacheManager() if use_cache else None
        self._api_failure_count = {}  # API 失败计数

        try:
            import akshare as ak
            self.ak = ak
        except ImportError:
            print("[ERROR] AKShare 未安装，请运行: pip install akshare", file=sys.stderr)
            sys.exit(1)

    def _log(self, msg: str):
        if not self.quiet:
            print(msg, file=sys.stderr)

    def _retry_call(self, func, max_retries: int = 2, delay: float = 1.0):
        """带重试的 API 调用

        Args:
            func: 要调用的函数
            max_retries: 最大重试次数
            delay: 重试延迟（秒）

        Returns:
            调用结果，失败返回 None
        """
        import time
        for attempt in range(max_retries + 1):
            try:
                result = func()
                if result is not None:
                    return result
            except Exception as e:
                if attempt < max_retries:
                    time.sleep(delay)
                    continue
                self._log(f"[WARN] API 调用失败 (重试{max_retries}次后): {e}")
        return None

    def get_index_data(self) -> List[IndexData]:
        """获取主要指数数据（带容错）"""
        indices = []

        # 主要指数代码映射
        index_map = {
            "000001": "上证指数",
            "399001": "深证成指",
            "399006": "创业板指",
            "000688": "科创50",
        }

        df = None

        # 方法1: 东方财富接口（主要）
        df = self._retry_call(lambda: self.ak.stock_zh_index_spot_em())

        # 方法2: 新浪接口（备用）
        if df is None or df.empty:
            self._log("[INFO] 尝试备用指数接口...")
            try:
                df = self.ak.stock_zh_index_spot()
                # 新浪接口列名可能不同，需要适配
                if df is not None and '代码' not in df.columns and 'symbol' in df.columns:
                    df = df.rename(columns={'symbol': '代码', 'name': '名称',
                                            'current': '最新价', 'chg': '涨跌额', 'percent': '涨跌幅'})
            except Exception as e:
                self._log(f"[WARN] 备用指数接口失败: {e}")

        if df is not None and not df.empty:
            for code, name in index_map.items():
                try:
                    row = df[df['代码'] == code]
                    if row.empty:
                        # 尝试带前缀的代码
                        row = df[df['代码'].str.contains(code)]
                    if not row.empty:
                        r = row.iloc[0]
                        indices.append(IndexData(
                            code=code,
                            name=name,
                            close=self._safe_float(r.get('最新价', 0)),
                            change=self._safe_float(r.get('涨跌额', 0)),
                            change_pct=self._safe_float(r.get('涨跌幅', 0)),
                            volume=self._safe_float(r.get('成交额', 0)) / 100000000
                        ))
                except Exception:
                    pass

        if not indices:
            self._log("[WARN] 无法获取指数数据")

        return indices

    def get_market_breadth(self) -> MarketBreadth:
        """获取市场宽度（涨跌家数）- 带容错"""
        df = None

        # 方法1: 东方财富实时行情（主要）
        df = self._retry_call(lambda: self.ak.stock_zh_a_spot_em())

        # 方法2: 使用乐股网统计数据（备用）
        if df is None or df.empty:
            self._log("[INFO] 尝试备用市场宽度接口...")
            try:
                legu_df = self.ak.stock_market_activity_legu()
                if legu_df is not None and not legu_df.empty:
                    up_count = down_count = flat_count = 0
                    for _, row in legu_df.iterrows():
                        item = str(row.get('item', ''))
                        value = int(row.get('value', 0) or 0)
                        if '上涨' in item:
                            up_count = value
                        elif '下跌' in item:
                            down_count = value
                        elif '平盘' in item:
                            flat_count = value

                    total = up_count + down_count + flat_count
                    up_ratio = up_count / total * 100 if total > 0 else 0
                    self._log(f"[INFO] 市场宽度(备用): 上涨{up_count}, 下跌{down_count}")
                    return MarketBreadth(up_count, down_count, flat_count, total, round(up_ratio, 1))
            except Exception as e:
                self._log(f"[WARN] 备用市场宽度接口失败: {e}")

        if df is None or df.empty:
            self._log("[WARN] 无法获取市场宽度数据")
            return MarketBreadth(0, 0, 0, 0, 0.0)

        try:
            # 计算涨跌家数
            change_col = '涨跌幅'
            if change_col not in df.columns:
                change_col = 'change_pct'

            df[change_col] = df[change_col].apply(self._safe_float)

            up_count = len(df[df[change_col] > 0])
            down_count = len(df[df[change_col] < 0])
            flat_count = len(df[df[change_col] == 0])
            total = len(df)

            up_ratio = up_count / total * 100 if total > 0 else 0

            self._log(f"[INFO] 市场宽度: 上涨{up_count}, 下跌{down_count}, 平盘{flat_count}")

            return MarketBreadth(
                up_count=up_count,
                down_count=down_count,
                flat_count=flat_count,
                total_count=total,
                up_ratio=round(up_ratio, 1)
            )

        except Exception as e:
            self._log(f"[WARN] 处理市场宽度数据失败: {e}")
            return MarketBreadth(0, 0, 0, 0, 0.0)

    def get_limit_stats(self) -> LimitStats:
        """获取涨跌停统计（带容错）"""
        limit_up = 0
        limit_down = 0
        limit_up_broken = 0
        continuous_up = 0
        max_continuous = 0

        today_str = datetime.now().strftime('%Y%m%d')

        # 涨停池（带重试）
        df_zt = self._retry_call(lambda: self.ak.stock_zt_pool_em(date=today_str))
        if df_zt is not None and not df_zt.empty:
            limit_up = len(df_zt)
            # 连板统计
            if '连板数' in df_zt.columns:
                try:
                    continuous_df = df_zt[df_zt['连板数'] >= 2]
                    continuous_up = len(continuous_df)
                    max_continuous = int(df_zt['连板数'].max()) if not df_zt['连板数'].isna().all() else 0
                except Exception:
                    pass
            self._log(f"[INFO] 涨停数: {limit_up}, 连板数: {continuous_up}")
        else:
            self._log(f"[WARN] 获取涨停池失败")

        # 跌停池（带重试）
        df_dt = self._retry_call(lambda: self.ak.stock_zt_pool_dtgc_em(date=today_str))
        if df_dt is not None and not df_dt.empty:
            limit_down = len(df_dt)
            self._log(f"[INFO] 跌停数: {limit_down}")
        else:
            self._log(f"[WARN] 获取跌停池失败")

        # 炸板池（带重试）
        df_zb = self._retry_call(lambda: self.ak.stock_zt_pool_zbgc_em(date=today_str))
        if df_zb is not None and not df_zb.empty:
            limit_up_broken = len(df_zb)
            self._log(f"[INFO] 炸板数: {limit_up_broken}")
        else:
            self._log(f"[WARN] 获取炸板池失败")

        # 计算炸板率
        total_touched = limit_up + limit_up_broken
        broken_ratio = limit_up_broken / total_touched * 100 if total_touched > 0 else 0

        return LimitStats(
            limit_up=limit_up,
            limit_down=limit_down,
            limit_up_broken=limit_up_broken,
            continuous_up=continuous_up,
            max_continuous=max_continuous,
            broken_ratio=round(broken_ratio, 1)
        )

    def get_total_volume(self) -> float:
        """获取全市场成交额（亿）"""
        try:
            df = self.ak.stock_zh_a_spot_em()
            if df is not None and not df.empty:
                vol_col = '成交额'
                if vol_col not in df.columns:
                    vol_col = 'amount'
                total = df[vol_col].apply(self._safe_float).sum() / 100000000
                self._log(f"[INFO] 全市场成交额: {total:.0f}亿")
                return round(total, 0)
        except Exception as e:
            self._log(f"[WARN] 获取成交额失败: {e}")
        return 0

    def calculate_sentiment(self, breadth: MarketBreadth, limits: LimitStats,
                           volume: float, prev_volume: Optional[float]) -> MarketSentiment:
        """计算市场情绪分数"""
        score = 50  # 基础分

        # 1. 涨跌比权重 (最多 ±20分)
        if breadth.total_count > 0:
            ratio = breadth.up_count / breadth.total_count
            score += (ratio - 0.5) * 40  # 涨跌比偏离50%的程度

        # 2. 涨跌停比权重 (最多 ±15分)
        if limits.limit_up + limits.limit_down > 0:
            zt_ratio = limits.limit_up / (limits.limit_up + limits.limit_down)
            score += (zt_ratio - 0.5) * 30

        # 3. 炸板率影响 (最多 -10分)
        if limits.broken_ratio > 30:
            score -= min(10, (limits.broken_ratio - 30) / 5)

        # 4. 连板高度加分 (最多 +10分)
        if limits.max_continuous >= 5:
            score += min(10, (limits.max_continuous - 4) * 2)

        # 5. 成交量变化 (最多 ±5分)
        if prev_volume and prev_volume > 0:
            vol_change = (volume - prev_volume) / prev_volume
            score += min(5, max(-5, vol_change * 10))

        # 限制在0-100
        score = max(0, min(100, int(score)))

        # 确定等级
        if score <= 15:
            level = "极度恐慌"
            desc = "市场极度悲观，可能存在恐慌性抛售"
        elif score <= 30:
            level = "恐慌"
            desc = "市场情绪低迷，下跌占主导"
        elif score <= 40:
            level = "谨慎"
            desc = "市场偏弱，建议保持谨慎"
        elif score <= 60:
            level = "中性"
            desc = "市场情绪平稳，多空平衡"
        elif score <= 70:
            level = "乐观"
            desc = "市场情绪偏暖，赚钱效应尚可"
        elif score <= 85:
            level = "贪婪"
            desc = "市场活跃，赚钱效应良好"
        else:
            level = "极度贪婪"
            desc = "市场过热，注意高位风险"

        return MarketSentiment(score=score, level=level, description=desc)

    def _safe_float(self, val) -> float:
        if val is None:
            return 0.0
        try:
            if isinstance(val, str):
                val = val.replace('%', '').replace(',', '').replace('亿', '').strip()
                if val == '' or val == '--' or val.lower() == 'nan':
                    return 0.0
            import math
            result = float(val)
            if math.isnan(result):
                return 0.0
            return result
        except (ValueError, TypeError):
            return 0.0

    def run(self) -> Dict:
        """执行数据获取"""
        today = datetime.now()
        today_str = today.strftime('%Y-%m-%d')

        # 检查是否是交易日
        if not is_trading_day(today):
            self._log("[INFO] 今天非交易日，跳过")
            return {
                "type": "market_sentiment_report",
                "timestamp": today.isoformat(),
                "date": today_str,
                "should_push": False,
                "reason": "非交易日"
            }

        # 检查是否需要推送
        should_push = True
        if self.cache:
            last_push = self.cache.get_last_push_date("market_sentiment")
            if last_push == today_str:
                should_push = False
                self._log(f"[INFO] 今日已推送过市场情绪")

        # 获取数据
        self._log("[INFO] 获取指数数据...")
        indices = self.get_index_data()

        self._log("[INFO] 获取市场宽度...")
        breadth = self.get_market_breadth()

        self._log("[INFO] 获取涨跌停统计...")
        limits = self.get_limit_stats()

        self._log("[INFO] 获取成交额...")
        volume = self.get_total_volume()

        # 获取前一日成交额
        prev_volume = None
        if self.cache:
            prev_volume = self.cache.get_prev_volume(today_str)

        # 计算情绪分数
        sentiment = self.calculate_sentiment(breadth, limits, volume, prev_volume)

        # 保存今日数据
        if self.cache and should_push:
            self.cache.save_market_data(today_str, {'total_volume': volume})
            self.cache.set_push_date("market_sentiment", today_str)

        # 计算成交额变化
        volume_change = 0
        volume_change_pct = 0
        if prev_volume and prev_volume > 0:
            volume_change = volume - prev_volume
            volume_change_pct = volume_change / prev_volume * 100

        result = {
            "type": "market_sentiment_report",
            "timestamp": today.isoformat(),
            "date": today_str,
            "should_push": should_push,
            "indices": [asdict(i) for i in indices],
            "breadth": asdict(breadth),
            "limits": asdict(limits),
            "volume": {
                "total": volume,
                "prev": prev_volume or 0,
                "change": round(volume_change, 0),
                "change_pct": round(volume_change_pct, 1)
            },
            "sentiment": asdict(sentiment)
        }

        return result


def main():
    parser = argparse.ArgumentParser(description='市场情绪/盘面总览')
    parser.add_argument('--json-only', action='store_true', help='仅输出JSON')
    parser.add_argument('--no-cache', action='store_true', help='禁用缓存')
    parser.add_argument('--quiet', '-q', action='store_true', help='静默模式')
    parser.add_argument('--force', action='store_true', help='强制推送')

    args = parser.parse_args()

    fetcher = MarketSentimentFetcher(
        use_cache=not args.no_cache,
        quiet=args.quiet or args.json_only
    )
    result = fetcher.run()

    if args.force:
        result['should_push'] = True

    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
