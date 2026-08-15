#!/usr/bin/env python3
"""
资金流向获取脚本

数据内容:
1. 北向资金流向 - 沪股通/深股通
2. 两融余额 - 融资融券数据
3. 主力资金 - 大单资金流向

运行: python scripts/capital_flow.py --json-only
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

    def __new__(cls, db_path: str = "data/cache/capital_flow_cache.db"):
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
                    cls._instance._initialized = False
        return cls._instance

    def __init__(self, db_path: str = "data/cache/capital_flow_cache.db"):
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
                CREATE TABLE IF NOT EXISTS northbound_flow (
                    date TEXT PRIMARY KEY,
                    sh_connect REAL,
                    sz_connect REAL,
                    total REAL,
                    updated_at TEXT
                )
            """)
            conn.execute("""
                CREATE TABLE IF NOT EXISTS margin_data (
                    date TEXT PRIMARY KEY,
                    margin_balance REAL,
                    short_balance REAL,
                    total_balance REAL,
                    updated_at TEXT
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

    def save_northbound_flow(self, date: str, sh: float, sz: float, total: float):
        """保存北向资金数据"""
        with sqlite3.connect(self.db_path) as conn:
            conn.execute("""
                INSERT OR REPLACE INTO northbound_flow
                (date, sh_connect, sz_connect, total, updated_at)
                VALUES (?, ?, ?, ?, ?)
            """, (date, sh, sz, total, datetime.now().isoformat()))
            conn.commit()

    def save_margin_data(self, date: str, margin: float, short: float, total: float):
        """保存两融数据"""
        with sqlite3.connect(self.db_path) as conn:
            conn.execute("""
                INSERT OR REPLACE INTO margin_data
                (date, margin_balance, short_balance, total_balance, updated_at)
                VALUES (?, ?, ?, ?, ?)
            """, (date, margin, short, total, datetime.now().isoformat()))
            conn.commit()

    def get_recent_northbound(self, days: int = 5) -> List[Dict]:
        """获取近期北向资金数据"""
        with sqlite3.connect(self.db_path) as conn:
            cursor = conn.execute("""
                SELECT date, sh_connect, sz_connect, total
                FROM northbound_flow
                ORDER BY date DESC LIMIT ?
            """, (days,))
            rows = cursor.fetchall()
            return [
                {'date': r[0], 'sh_connect': r[1], 'sz_connect': r[2], 'total': r[3]}
                for r in rows
            ]


_cache_manager: Optional[CacheManager] = None


def get_cache_manager() -> CacheManager:
    global _cache_manager
    if _cache_manager is None:
        _cache_manager = CacheManager()
    return _cache_manager


# ============================================
# 熔断机制
# ============================================

class CircuitBreaker:
    """熔断器 - 带半开状态和指数退避

    状态：
    - CLOSED: 正常状态，允许请求
    - OPEN: 熔断状态，拒绝请求
    - HALF_OPEN: 半开状态，允许一个测试请求
    """

    STATE_CLOSED = "closed"
    STATE_OPEN = "open"
    STATE_HALF_OPEN = "half_open"

    def __init__(self, failure_threshold: int = 3, base_recovery_timeout: int = 60,
                 max_recovery_timeout: int = 600):
        self.failure_threshold = failure_threshold
        self.base_recovery_timeout = base_recovery_timeout
        self.max_recovery_timeout = max_recovery_timeout
        self._failures: Dict[str, int] = {}
        self._consecutive_opens: Dict[str, int] = {}  # 连续熔断次数（用于退避）
        self._last_failure_time: Dict[str, datetime] = {}
        self._state: Dict[str, str] = {}  # 状态
        self._lock = threading.Lock()

    def _get_recovery_timeout(self, source: str) -> int:
        """计算恢复超时（指数退避）"""
        opens = self._consecutive_opens.get(source, 0)
        timeout = self.base_recovery_timeout * (2 ** opens)
        return min(timeout, self.max_recovery_timeout)

    def is_open(self, source: str) -> bool:
        """检查熔断器是否打开

        Returns:
            True: 熔断器打开，应拒绝请求
            False: 熔断器关闭或半开，允许请求
        """
        with self._lock:
            state = self._state.get(source, self.STATE_CLOSED)

            if state == self.STATE_CLOSED:
                return False

            if state == self.STATE_OPEN:
                # 检查是否超过恢复时间
                if source in self._last_failure_time:
                    elapsed = (datetime.now() - self._last_failure_time[source]).total_seconds()
                    if elapsed >= self._get_recovery_timeout(source):
                        # 转换为半开状态
                        self._state[source] = self.STATE_HALF_OPEN
                        return False  # 允许一个测试请求
                return True

            if state == self.STATE_HALF_OPEN:
                return False  # 允许测试请求

            return False

    def record_success(self, source: str):
        """记录成功"""
        with self._lock:
            state = self._state.get(source, self.STATE_CLOSED)

            if state == self.STATE_HALF_OPEN:
                # 半开状态成功，重置为关闭
                self._state[source] = self.STATE_CLOSED
                self._failures[source] = 0
                self._consecutive_opens[source] = 0
            else:
                self._failures[source] = 0

    def record_failure(self, source: str):
        """记录失败"""
        with self._lock:
            self._failures[source] = self._failures.get(source, 0) + 1
            self._last_failure_time[source] = datetime.now()

            state = self._state.get(source, self.STATE_CLOSED)

            if state == self.STATE_HALF_OPEN:
                # 半开状态失败，立即熔断
                self._state[source] = self.STATE_OPEN
                self._consecutive_opens[source] = self._consecutive_opens.get(source, 0) + 1

            elif self._failures[source] >= self.failure_threshold:
                # 超过阈值，熔断
                self._state[source] = self.STATE_OPEN
                self._consecutive_opens[source] = self._consecutive_opens.get(source, 0) + 1

    def get_state(self, source: str) -> str:
        """获取熔断器状态"""
        with self._lock:
            return self._state.get(source, self.STATE_CLOSED)


_circuit_breaker: Optional[CircuitBreaker] = None


def get_circuit_breaker() -> CircuitBreaker:
    global _circuit_breaker
    if _circuit_breaker is None:
        _circuit_breaker = CircuitBreaker()
    return _circuit_breaker


# ============================================
# 数据模型
# ============================================

@dataclass
class NorthboundFlow:
    """北向资金流向"""
    date: str
    sh_connect: float       # 沪股通净流入(亿)
    sz_connect: float       # 深股通净流入(亿)
    total: float            # 总净流入(亿)
    week_total: float = 0   # 本周累计
    is_significant: bool = False  # 是否显著变动


@dataclass
class MarginData:
    """两融数据"""
    date: str
    margin_balance: float   # 融资余额(亿)
    margin_change: float    # 融资变化
    short_balance: float    # 融券余额(亿)
    short_change: float     # 融券变化
    total_balance: float    # 两融余额(亿)
    total_change_pct: float # 变化百分比


@dataclass
class MainCapitalFlow:
    """主力资金流向"""
    code: str
    name: str
    net_inflow: float       # 主力净流入(亿)
    change_pct: float       # 涨跌幅


# ============================================
# 数据获取器
# ============================================

class CapitalFlowFetcher:
    """资金流向数据获取器"""

    def __init__(self, use_cache: bool = True, quiet: bool = False):
        self.ak = None
        self.cache = get_cache_manager() if use_cache else None
        self.circuit_breaker = get_circuit_breaker()
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

    def get_northbound_flow(self) -> Optional[NorthboundFlow]:
        """获取北向资金流向 - 多数据源"""
        if not self.ak:
            return None
        if self.circuit_breaker.is_open('northbound'):
            self._log("[WARN] 北向资金数据源熔断中")
            return None

        # 方法1: 使用 stock_hsgt_fund_flow_summary_em 筛选北向数据 (AKShare)
        result = self._get_northbound_method1()
        if result:
            return result

        # 方法2: 使用东方财富 datacenter API (直接HTTP请求)
        result = self._get_northbound_method2()
        if result:
            return result

        # 方法2备用: 使用 stock_hsgt_hist_em 历史数据 (AKShare)
        result = self._get_northbound_method2_akshare()
        if result:
            return result

        # 方法3: 使用原有解析方式
        result = self._get_northbound_method3()
        if result:
            return result

        # 方法4: 尝试从缓存获取最近数据
        result = self._get_northbound_from_cache()
        if result:
            return result

        self._log("[WARN] 所有北向资金数据源均失败，可能是假期或数据源问题")
        self.circuit_breaker.record_failure('northbound')
        return None

    def _get_northbound_from_cache(self) -> Optional[NorthboundFlow]:
        """方法4: 从缓存获取最近有效数据"""
        if not self.cache:
            return None

        try:
            self._log("[INFO] 尝试方法4: 从缓存获取数据")
            recent = self.cache.get_recent_northbound(10)
            if not recent:
                self._log("[INFO] 方法4: 缓存为空")
                return None

            # 找到最近有非零数据的记录
            for record in recent:
                total = self._safe_float(record.get('total', 0))
                if total != 0:
                    sh = self._safe_float(record.get('sh_connect', 0))
                    sz = self._safe_float(record.get('sz_connect', 0))
                    date = record.get('date', '')
                    self._log(f"[INFO] 方法4成功: 使用缓存数据 {date} {total:.2f}亿")
                    return NorthboundFlow(
                        date=date + " (缓存)",
                        sh_connect=sh,
                        sz_connect=sz,
                        total=total,
                        week_total=total,
                        is_significant=abs(total) > 50
                    )

            self._log("[INFO] 方法4: 缓存中无有效数据")
            return None
        except Exception as e:
            self._log(f"[WARN] 方法4失败: {e}")
            return None

    def _get_northbound_method1(self) -> Optional[NorthboundFlow]:
        """方法1: 使用 stock_hsgt_fund_flow_summary_em 筛选北向数据"""
        try:
            self._log("[INFO] 尝试方法1: stock_hsgt_fund_flow_summary_em (北向筛选)")
            df = self.ak.stock_hsgt_fund_flow_summary_em()
            if df is None or df.empty:
                self._log("[INFO] 方法1: 数据为空")
                return None

            # 筛选北向资金数据 (沪股通和深股通)
            # 数据结构: 交易日, 类型, 板块, 资金方向, 成交净买额 等
            north_df = df[df['资金方向'] == '北向']
            if north_df.empty:
                self._log("[INFO] 方法1: 无北向数据")
                return None

            # 获取日期
            date = str(north_df.iloc[0].get('交易日', datetime.now().strftime('%Y-%m-%d')))[:10]

            # 获取沪股通和深股通数据
            sh_row = north_df[north_df['板块'] == '沪股通']
            sz_row = north_df[north_df['板块'] == '深股通']

            sh_connect = 0.0
            sz_connect = 0.0

            if not sh_row.empty:
                # 成交净买额单位是亿元
                sh_connect = self._safe_float(sh_row.iloc[0].get('成交净买额', 0))

            if not sz_row.empty:
                sz_connect = self._safe_float(sz_row.iloc[0].get('成交净买额', 0))

            total = sh_connect + sz_connect

            # 如果数据全为0，返回 None 让其他方法尝试
            if total == 0 and sh_connect == 0 and sz_connect == 0:
                self._log("[INFO] 方法1: 数据全为0")
                return None

            # 计算本周累计
            week_total = total
            if self.cache:
                recent = self.cache.get_recent_northbound(5)
                week_total = sum(r['total'] for r in recent) + total

            is_significant = abs(total) > 50

            if self.cache:
                self.cache.save_northbound_flow(date, sh_connect, sz_connect, total)

            self.circuit_breaker.record_success('northbound')
            self._log(f"[INFO] 方法1成功: 北向资金 {total:.2f}亿 (沪:{sh_connect:.2f} 深:{sz_connect:.2f})")

            return NorthboundFlow(
                date=date,
                sh_connect=sh_connect,
                sz_connect=sz_connect,
                total=total,
                week_total=week_total,
                is_significant=is_significant
            )
        except Exception as e:
            self._log(f"[WARN] 方法1失败: {e}")
            return None

    def _get_northbound_method2(self) -> Optional[NorthboundFlow]:
        """方法2: 使用东方财富 datacenter API"""
        try:
            import requests
            self._log("[INFO] 尝试方法2: 东方财富 datacenter API")

            url = 'https://datacenter-web.eastmoney.com/api/data/v1/get'
            params = {
                'reportName': 'RPT_MUTUAL_DEAL_HISTORY',
                'columns': 'ALL',
                'pageSize': 20,
                'sortColumns': 'TRADE_DATE',
                'sortTypes': -1,
                'source': 'WEB',
                'client': 'WEB',
            }
            headers = {
                'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36',
                'Referer': 'https://data.eastmoney.com/'
            }

            resp = requests.get(url, params=params, headers=headers, timeout=15)
            data = resp.json()

            if not data.get('success') or not data.get('result', {}).get('data'):
                self._log("[INFO] 方法2: 无数据")
                return None

            records = data['result']['data']

            # 找到最近有北向数据的日期
            # MUTUAL_TYPE: 001=沪股通(北向), 003=深股通(北向), 005=北向合计
            sh_data = None
            sz_data = None

            for record in records:
                if record.get('MUTUAL_TYPE') == '001' and record.get('NET_DEAL_AMT') is not None:
                    if sh_data is None:
                        sh_data = record
                elif record.get('MUTUAL_TYPE') == '003' and record.get('NET_DEAL_AMT') is not None:
                    if sz_data is None:
                        sz_data = record

                if sh_data and sz_data:
                    break

            if not sh_data and not sz_data:
                self._log("[INFO] 方法2: 未找到有效的北向数据")
                return None

            # 使用找到的数据
            date = datetime.now().strftime('%Y-%m-%d')
            sh_connect = 0.0
            sz_connect = 0.0

            if sh_data:
                date = str(sh_data.get('TRADE_DATE', ''))[:10]
                # NET_DEAL_AMT 单位是百万元，转换为亿元
                sh_connect = self._safe_float(sh_data.get('NET_DEAL_AMT', 0)) / 100

            if sz_data:
                if not sh_data:
                    date = str(sz_data.get('TRADE_DATE', ''))[:10]
                sz_connect = self._safe_float(sz_data.get('NET_DEAL_AMT', 0)) / 100

            total = sh_connect + sz_connect

            if total == 0:
                self._log("[INFO] 方法2: 数据为0")
                return None

            week_total = total
            if self.cache:
                recent = self.cache.get_recent_northbound(5)
                week_total = sum(r['total'] for r in recent) + total

            is_significant = abs(total) > 50

            if self.cache:
                self.cache.save_northbound_flow(date, sh_connect, sz_connect, total)

            self.circuit_breaker.record_success('northbound')
            self._log(f"[INFO] 方法2成功: 北向资金 {total:.2f}亿 ({date})")

            return NorthboundFlow(
                date=date,
                sh_connect=sh_connect,
                sz_connect=sz_connect,
                total=total,
                week_total=week_total,
                is_significant=is_significant
            )
        except Exception as e:
            self._log(f"[WARN] 方法2失败: {e}")
            return None

    def _get_northbound_method2_akshare(self) -> Optional[NorthboundFlow]:
        """方法2备用: 使用 stock_hsgt_hist_em 历史数据"""
        try:
            import pandas as pd
            self._log("[INFO] 尝试方法2备用: stock_hsgt_hist_em")
            df = self.ak.stock_hsgt_hist_em(symbol="沪股通")

            if df is None or df.empty:
                self._log("[INFO] 方法2备用: 沪股通数据为空")
                return None

            # 找到最近有效数据（当日成交净买额不为 NaN 且不为 0）
            sh_latest = None
            for i in range(len(df) - 1, max(len(df) - 200, -1), -1):
                row = df.iloc[i]
                net_buy_val = row.get('当日成交净买额')
                if pd.notna(net_buy_val):
                    net_buy = self._safe_float(net_buy_val)
                    if net_buy != 0:
                        sh_latest = row
                        break

            if sh_latest is None:
                self._log("[INFO] 方法2备用: 沪股通无有效数据")
                return None

            date_val = sh_latest.get('日期')
            if hasattr(date_val, 'strftime'):
                date = date_val.strftime('%Y-%m-%d')
            else:
                date = str(date_val)[:10]

            sh_connect = self._safe_float(sh_latest.get('当日成交净买额', 0))
            if abs(sh_connect) > 10000:
                sh_connect = sh_connect / 100000000

            # 尝试获取深股通数据
            sz_connect = 0.0
            try:
                df_sz = self.ak.stock_hsgt_hist_em(symbol="深股通")
                if df_sz is not None and not df_sz.empty:
                    for i in range(len(df_sz) - 1, max(len(df_sz) - 200, -1), -1):
                        row = df_sz.iloc[i]
                        net_buy_val = row.get('当日成交净买额')
                        if pd.notna(net_buy_val):
                            net_buy = self._safe_float(net_buy_val)
                            if net_buy != 0:
                                sz_connect = net_buy
                                if abs(sz_connect) > 10000:
                                    sz_connect = sz_connect / 100000000
                                break
            except Exception:
                pass

            total = sh_connect + sz_connect

            if total == 0:
                return None

            week_total = total
            if self.cache:
                recent = self.cache.get_recent_northbound(5)
                week_total = sum(self._safe_float(r.get('total', 0)) for r in recent) + total

            is_significant = abs(total) > 50

            if self.cache:
                self.cache.save_northbound_flow(date, sh_connect, sz_connect, total)

            self.circuit_breaker.record_success('northbound')
            self._log(f"[INFO] 方法2备用成功: 北向资金 {total:.2f}亿 ({date})")

            return NorthboundFlow(
                date=date,
                sh_connect=sh_connect,
                sz_connect=sz_connect,
                total=total,
                week_total=week_total,
                is_significant=is_significant
            )
        except Exception as e:
            self._log(f"[WARN] 方法2备用失败: {e}")
            return None

    def _get_northbound_method3(self) -> Optional[NorthboundFlow]:
        """方法3: 使用原有 stock_hsgt_fund_flow_summary_em"""
        try:
            self._log("[INFO] 尝试方法3: stock_hsgt_fund_flow_summary_em")
            df = self.ak.stock_hsgt_fund_flow_summary_em()
            if df is None or df.empty:
                self._log("[INFO] 方法3: 数据为空")
                return None

            latest = df.iloc[-1] if len(df) > 0 else None
            if latest is None:
                return None

            date = str(latest.get('日期', datetime.now().strftime('%Y-%m-%d')))[:10]

            sh_connect = self._safe_float(latest.get('沪股通-净流入', latest.get('沪股通净流入', 0))) / 100000000
            sz_connect = self._safe_float(latest.get('深股通-净流入', latest.get('深股通净流入', 0))) / 100000000
            total = sh_connect + sz_connect

            # 如果数据全为0，返回None让其他方法尝试
            if total == 0 and sh_connect == 0 and sz_connect == 0:
                self._log("[INFO] 方法3: 数据全为0")
                return None

            week_total = total
            if self.cache:
                recent = self.cache.get_recent_northbound(5)
                week_total = sum(r['total'] for r in recent) + total

            is_significant = abs(total) > 50

            if self.cache:
                self.cache.save_northbound_flow(date, sh_connect, sz_connect, total)

            self.circuit_breaker.record_success('northbound')
            self._log(f"[INFO] 方法3成功: 北向资金 {total:.2f}亿")

            return NorthboundFlow(
                date=date,
                sh_connect=sh_connect,
                sz_connect=sz_connect,
                total=total,
                week_total=week_total,
                is_significant=is_significant
            )
        except Exception as e:
            self._log(f"[WARN] 方法3失败: {e}")
            return None

    def get_margin_data(self) -> Optional[MarginData]:
        """获取两融数据"""
        if not self.ak:
            return None
        if self.circuit_breaker.is_open('margin'):
            return None

        try:
            # 获取上证融资融券数据
            today = datetime.now()
            start_date = (today - timedelta(days=30)).strftime('%Y%m%d')
            end_date = today.strftime('%Y%m%d')

            df_sse = self.ak.stock_margin_sse(start_date=start_date, end_date=end_date)
            if df_sse is None or df_sse.empty:
                self._log("[INFO] 未获取到上证两融数据")
                return None

            # 获取深证融资融券数据
            df_szse = self.ak.stock_margin_szse()

            latest_sse = df_sse.iloc[0]  # 最新数据在第一行
            prev_sse = df_sse.iloc[1] if len(df_sse) > 1 else None

            date_str = str(latest_sse.get('信用交易日期', today.strftime('%Y%m%d')))
            if len(date_str) == 8:
                date = f"{date_str[:4]}-{date_str[4:6]}-{date_str[6:8]}"
            else:
                date = date_str[:10]

            # 上证融资余额（单位：元）
            sse_margin = self._safe_float(latest_sse.get('融资余额', 0)) / 100000000
            # 深证融资余额（单位：亿元）
            szse_margin = self._safe_float(df_szse.iloc[0].get('融资余额', 0)) if df_szse is not None and not df_szse.empty else 0

            margin_balance = sse_margin + szse_margin

            # 上证融券余额（单位：元）
            sse_short = self._safe_float(latest_sse.get('融券余量金额', 0)) / 100000000
            # 深证融券余额（单位：亿元）
            szse_short = self._safe_float(df_szse.iloc[0].get('融券余额', 0)) if df_szse is not None and not df_szse.empty else 0
            short_balance = sse_short + szse_short

            total_balance = margin_balance + short_balance

            margin_change = 0
            short_change = 0
            total_change_pct = 0

            if prev_sse is not None:
                prev_sse_margin = self._safe_float(prev_sse.get('融资余额', 0)) / 100000000
                prev_margin = prev_sse_margin + szse_margin  # 简化处理
                prev_sse_short = self._safe_float(prev_sse.get('融券余量金额', 0)) / 100000000
                prev_short = prev_sse_short + szse_short
                prev_total = prev_margin + prev_short

                margin_change = margin_balance - prev_margin
                short_change = short_balance - prev_short
                if prev_total > 0:
                    total_change_pct = (total_balance - prev_total) / prev_total * 100

            # 保存到缓存
            if self.cache:
                self.cache.save_margin_data(date, margin_balance, short_balance, total_balance)

            self.circuit_breaker.record_success('margin')

            return MarginData(
                date=date,
                margin_balance=margin_balance,
                margin_change=margin_change,
                short_balance=short_balance,
                short_change=short_change,
                total_balance=total_balance,
                total_change_pct=total_change_pct
            )

        except Exception as e:
            self._log(f"[WARN] 获取两融数据失败: {e}")
            self.circuit_breaker.record_failure('margin')
            return None

    def get_main_capital_flow(self, top_n: int = 10) -> List[MainCapitalFlow]:
        """获取主力资金流向"""
        if not self.ak:
            return []
        if self.circuit_breaker.is_open('main_capital'):
            return []

        try:
            # 获取主力资金流向数据
            df = self.ak.stock_individual_fund_flow_rank(indicator="今日")
            if df is None or df.empty:
                self._log("[INFO] 未获取到主力资金数据")
                return []

            results = []

            # 净流入前N
            inflow_df = df.nlargest(top_n, '主力净流入-净额')
            for _, row in inflow_df.iterrows():
                code = str(row.get('代码', '')).zfill(6)
                name = str(row.get('名称', ''))
                net_inflow = self._safe_float(row.get('主力净流入-净额', 0)) / 100000000
                change_pct = self._safe_float(row.get('涨跌幅', 0))

                results.append(MainCapitalFlow(
                    code=code,
                    name=name,
                    net_inflow=net_inflow,
                    change_pct=change_pct
                ))

            # 净流出前N
            outflow_df = df.nsmallest(top_n, '主力净流入-净额')
            for _, row in outflow_df.iterrows():
                code = str(row.get('代码', '')).zfill(6)
                name = str(row.get('名称', ''))
                net_inflow = self._safe_float(row.get('主力净流入-净额', 0)) / 100000000
                change_pct = self._safe_float(row.get('涨跌幅', 0))

                results.append(MainCapitalFlow(
                    code=code,
                    name=name,
                    net_inflow=net_inflow,
                    change_pct=change_pct
                ))

            self.circuit_breaker.record_success('main_capital')
            self._log(f"[INFO] 获取到 {len(results)} 条主力资金数据")
            return results

        except Exception as e:
            self._log(f"[WARN] 获取主力资金流向失败: {e}")
            self.circuit_breaker.record_failure('main_capital')
            return []

    def run(self, include_northbound: bool = False, include_margin: bool = True,
            include_main_capital: bool = True) -> Dict:
        """执行数据获取"""
        today = datetime.now()
        today_str = today.strftime('%Y-%m-%d')

        # 检查是否需要推送
        should_push = True
        if self.cache:
            last_push = self.cache.get_last_push_date('capital_flow')
            if last_push == today_str:
                should_push = False
                self._log("[INFO] 今日已推送过资金流向")

        result = {
            "type": "capital_flow_report",
            "timestamp": datetime.now().isoformat(),
            "date": today_str,
            "should_push": should_push,
            "northbound": None,
            "margin": None,
            "main_capital_inflow": [],
            "main_capital_outflow": [],
        }

        if include_northbound:
            self._log("[INFO] 获取北向资金数据...")
            northbound = self.get_northbound_flow()
            if northbound:
                result["northbound"] = asdict(northbound)

        if include_margin:
            self._log("[INFO] 获取两融数据...")
            margin = self.get_margin_data()
            if margin:
                result["margin"] = asdict(margin)

        if include_main_capital:
            self._log("[INFO] 获取主力资金数据...")
            main_capital = self.get_main_capital_flow(5)
            if main_capital:
                # 分离流入和流出
                inflow = [asdict(m) for m in main_capital if m.net_inflow > 0]
                outflow = [asdict(m) for m in main_capital if m.net_inflow < 0]
                result["main_capital_inflow"] = sorted(inflow, key=lambda x: x['net_inflow'], reverse=True)[:5]
                result["main_capital_outflow"] = sorted(outflow, key=lambda x: x['net_inflow'])[:5]

        # 更新推送记录
        if should_push and self.cache:
            self.cache.set_last_push_date('capital_flow', today_str)

        return result


def main():
    parser = argparse.ArgumentParser(description='资金流向获取')
    parser.add_argument('--no-northbound', action='store_true', help='不获取北向资金')
    parser.add_argument('--no-margin', action='store_true', help='不获取两融数据')
    parser.add_argument('--no-main-capital', action='store_true', help='不获取主力资金')
    parser.add_argument('--json-only', action='store_true', help='仅输出JSON')
    parser.add_argument('--no-cache', action='store_true', help='禁用缓存')
    parser.add_argument('--quiet', '-q', action='store_true', help='静默模式')
    parser.add_argument('--force', action='store_true', help='强制推送')

    args = parser.parse_args()

    fetcher = CapitalFlowFetcher(
        use_cache=not args.no_cache,
        quiet=args.quiet or args.json_only
    )
    result = fetcher.run(
        include_northbound=not args.no_northbound,
        include_margin=not args.no_margin,
        include_main_capital=not args.no_main_capital
    )

    if args.force:
        result['should_push'] = True

    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
