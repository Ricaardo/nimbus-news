#!/usr/bin/env python3
"""
A股热门板块推送系统
结合 AKShare + 同花顺问财(pywencai) 获取热门板块、龙头股及技术指标分析

数据源优先级:
1. 同花顺问财 (pywencai) - 人气排名、热门板块、龙头股（需要cookie）
2. AKShare (东方财富) - 板块行情、资金流向、K线数据
3. AData - 备选数据源

功能:
1. 获取近3天热门板块/概念（基于人气热度、资金流向）
2. 获取板块龙一龙二（基于人气排名）
3. 分析龙头股K线和技术指标（MACD/KDJ/RSI/布林带）
4. 生成买入建议报告
5. 推送到指定渠道

运行: python scripts/hot_sector_push.py
定时: 配合 cron 或 schedule 库实现每日定时推送

同花顺问财配置:
- 需要安装 Node.js v16+
- 自动获取 Cookie: python scripts/wencai_cookie.py --interactive
- 或手动设置: export WENCAI_COOKIE="your_cookie_here"
"""

import json
import os
import sys
import time
import sqlite3
import hashlib
import requests
from datetime import datetime, timedelta
from typing import Dict, List, Optional, Tuple, Any
from dataclasses import dataclass, field, asdict
from functools import wraps
from pathlib import Path
import warnings
import threading

warnings.filterwarnings("ignore")

# ============================================
# 缓存层 - SQLite 本地缓存
# ============================================


class CacheManager:
    """SQLite 缓存管理器，减少 API 调用"""

    _instance = None
    _lock = threading.Lock()

    def __new__(cls, db_path: str = "data/cache/market_cache.db"):
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
                    cls._instance._initialized = False
        return cls._instance

    def __init__(self, db_path: str = "data/cache/market_cache.db"):
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
                CREATE TABLE IF NOT EXISTS kline_cache (
                    stock_code TEXT,
                    trade_date TEXT,
                    open REAL,
                    high REAL,
                    low REAL,
                    close REAL,
                    volume REAL,
                    amount REAL,
                    turnover REAL,
                    updated_at TEXT,
                    PRIMARY KEY (stock_code, trade_date)
                )
            """)
            conn.execute("""
                CREATE TABLE IF NOT EXISTS sector_cache (
                    cache_key TEXT PRIMARY KEY,
                    data TEXT,
                    created_at TEXT,
                    ttl_seconds INTEGER
                )
            """)
            conn.execute("""
                CREATE TABLE IF NOT EXISTS hot_rank_cache (
                    stock_code TEXT PRIMARY KEY,
                    stock_name TEXT,
                    price REAL,
                    change_pct REAL,
                    hot_rank INTEGER,
                    updated_at TEXT
                )
            """)
            conn.execute(
                "CREATE INDEX IF NOT EXISTS idx_kline_code ON kline_cache(stock_code)"
            )
            conn.execute(
                "CREATE INDEX IF NOT EXISTS idx_kline_date ON kline_cache(trade_date)"
            )
            conn.commit()

    def get_cached_kline(self, stock_code: str, days: int = 60) -> Optional[List[Dict]]:
        """获取缓存的K线数据"""
        today = datetime.now().strftime("%Y-%m-%d")
        start_date = (datetime.now() - timedelta(days=days + 10)).strftime("%Y-%m-%d")

        with sqlite3.connect(self.db_path) as conn:
            cursor = conn.execute(
                """
                SELECT trade_date, open, high, low, close, volume, amount, turnover
                FROM kline_cache
                WHERE stock_code = ? AND trade_date >= ? AND trade_date <= ?
                ORDER BY trade_date ASC
            """,
                (stock_code, start_date, today),
            )
            rows = cursor.fetchall()

            if len(rows) >= days * 0.8:  # 如果缓存数据足够（80%以上）
                return [
                    {
                        "date": row[0],
                        "open": row[1],
                        "high": row[2],
                        "low": row[3],
                        "close": row[4],
                        "volume": row[5],
                        "amount": row[6],
                        "turnover": row[7],
                    }
                    for row in rows
                ]
        return None

    def save_kline(self, stock_code: str, kline_data: List[Dict]):
        """保存K线数据到缓存"""
        if not kline_data:
            return

        now = datetime.now().isoformat()
        with sqlite3.connect(self.db_path) as conn:
            for k in kline_data:
                conn.execute(
                    """
                    INSERT OR REPLACE INTO kline_cache
                    (stock_code, trade_date, open, high, low, close, volume, amount, turnover, updated_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                    (
                        stock_code,
                        k["date"],
                        k["open"],
                        k["high"],
                        k["low"],
                        k["close"],
                        k["volume"],
                        k.get("amount", 0),
                        k.get("turnover", 0),
                        now,
                    ),
                )
            conn.commit()

    def get_latest_kline_date(self, stock_code: str) -> Optional[str]:
        """获取最新缓存的K线日期"""
        with sqlite3.connect(self.db_path) as conn:
            cursor = conn.execute(
                """
                SELECT MAX(trade_date) FROM kline_cache WHERE stock_code = ?
            """,
                (stock_code,),
            )
            row = cursor.fetchone()
            return row[0] if row and row[0] else None

    def get_sector_cache(self, cache_key: str) -> Optional[Any]:
        """获取板块缓存"""
        with sqlite3.connect(self.db_path) as conn:
            cursor = conn.execute(
                """
                SELECT data, created_at, ttl_seconds FROM sector_cache WHERE cache_key = ?
            """,
                (cache_key,),
            )
            row = cursor.fetchone()

            if row:
                created_at = datetime.fromisoformat(row[1])
                ttl = row[2]
                if (datetime.now() - created_at).total_seconds() < ttl:
                    return json.loads(row[0])
        return None

    def set_sector_cache(self, cache_key: str, data: Any, ttl_seconds: int = 300):
        """设置板块缓存"""
        with sqlite3.connect(self.db_path) as conn:
            conn.execute(
                """
                INSERT OR REPLACE INTO sector_cache (cache_key, data, created_at, ttl_seconds)
                VALUES (?, ?, ?, ?)
            """,
                (
                    cache_key,
                    json.dumps(data, ensure_ascii=False),
                    datetime.now().isoformat(),
                    ttl_seconds,
                ),
            )
            conn.commit()

    def get_hot_rank_cache(self) -> Dict[str, int]:
        """获取人气排名缓存"""
        today = datetime.now().strftime("%Y-%m-%d")
        with sqlite3.connect(self.db_path) as conn:
            cursor = conn.execute(
                """
                SELECT stock_code, hot_rank FROM hot_rank_cache
                WHERE updated_at LIKE ?
            """,
                (f"{today}%",),
            )
            return {row[0]: row[1] for row in cursor.fetchall()}

    def save_hot_rank_cache(self, hot_data: List[Dict]):
        """保存人气排名到缓存"""
        now = datetime.now().isoformat()
        with sqlite3.connect(self.db_path) as conn:
            for s in hot_data:
                conn.execute(
                    """
                    INSERT OR REPLACE INTO hot_rank_cache
                    (stock_code, stock_name, price, change_pct, hot_rank, updated_at)
                    VALUES (?, ?, ?, ?, ?, ?)
                """,
                    (
                        s["code"],
                        s.get("name", ""),
                        s.get("price", 0),
                        s.get("change_pct", 0),
                        s.get("rank", 9999),
                        now,
                    ),
                )
            conn.commit()

    def cleanup_old_data(self, days: int = 90):
        """清理过期数据"""
        cutoff = (datetime.now() - timedelta(days=days)).strftime("%Y-%m-%d")
        with sqlite3.connect(self.db_path) as conn:
            conn.execute("DELETE FROM kline_cache WHERE trade_date < ?", (cutoff,))
            conn.execute("DELETE FROM sector_cache WHERE created_at < ?", (cutoff,))
            conn.commit()


# ============================================
# 熔断机制
# ============================================


class CircuitBreaker:
    """熔断器 - 当某个数据源连续失败时自动熔断"""

    def __init__(self, failure_threshold: int = 3, recovery_timeout: int = 300):
        self.failure_threshold = failure_threshold  # 失败阈值
        self.recovery_timeout = recovery_timeout  # 恢复超时(秒)
        self._failures: Dict[str, int] = {}
        self._last_failure_time: Dict[str, datetime] = {}
        self._lock = threading.Lock()

    def is_open(self, source: str) -> bool:
        """检查熔断器是否打开(即数据源是否不可用)"""
        with self._lock:
            if source not in self._failures:
                return False

            if self._failures[source] >= self.failure_threshold:
                # 检查是否到了恢复时间
                if source in self._last_failure_time:
                    elapsed = (
                        datetime.now() - self._last_failure_time[source]
                    ).total_seconds()
                    if elapsed >= self.recovery_timeout:
                        # 半开状态，允许一次尝试
                        self._failures[source] = self.failure_threshold - 1
                        return False
                return True
            return False

    def record_success(self, source: str):
        """记录成功"""
        with self._lock:
            self._failures[source] = 0

    def record_failure(self, source: str):
        """记录失败"""
        with self._lock:
            self._failures[source] = self._failures.get(source, 0) + 1
            self._last_failure_time[source] = datetime.now()

            if self._failures[source] >= self.failure_threshold:
                print(
                    f"[CIRCUIT] 数据源 {source} 已熔断，{self.recovery_timeout}秒后尝试恢复"
                )

    def get_status(self) -> Dict[str, str]:
        """获取所有数据源状态"""
        status = {}
        for source, failures in self._failures.items():
            if failures >= self.failure_threshold:
                status[source] = "OPEN (熔断中)"
            elif failures > 0:
                status[source] = f"HALF-OPEN (失败{failures}次)"
            else:
                status[source] = "CLOSED (正常)"
        return status


# 全局实例
_cache_manager: Optional[CacheManager] = None
_circuit_breaker: Optional[CircuitBreaker] = None


def get_cache_manager() -> CacheManager:
    global _cache_manager
    if _cache_manager is None:
        _cache_manager = CacheManager()
    return _cache_manager


def get_circuit_breaker() -> CircuitBreaker:
    global _circuit_breaker
    if _circuit_breaker is None:
        _circuit_breaker = CircuitBreaker()
    return _circuit_breaker


def retry_on_error(max_retries: int = 3, delay: float = 2.0):
    """重试装饰器，用于处理网络不稳定"""

    def decorator(func):
        @wraps(func)
        def wrapper(*args, **kwargs):
            last_error = None
            for attempt in range(max_retries):
                try:
                    return func(*args, **kwargs)
                except Exception as e:
                    last_error = e
                    if attempt < max_retries - 1:
                        time.sleep(delay * (attempt + 1))
            # 所有重试都失败后返回空或抛出异常
            return None if "get_" in func.__name__ else None

        return wrapper

    return decorator


# 同花顺问财 Cookie (优先级: 环境变量 > 缓存文件)
def _load_wencai_cookie() -> str:
    """加载问财 Cookie"""
    # 1. 环境变量优先
    cookie = os.environ.get("WENCAI_COOKIE", "")
    if cookie:
        return cookie

    # 2. 尝试从缓存文件读取 (由 wencai_cookie.py 生成)
    cookie_file = Path(__file__).parent.parent / "data" / ".wencai" / "cookie.txt"
    if cookie_file.exists():
        cookie = cookie_file.read_text().strip()
        if cookie:
            return cookie

    return ""


WENCAI_COOKIE = _load_wencai_cookie()

# 需要过滤掉的板块（仅过滤 ST 相关和一些无效板块）
EXCLUDED_SECTOR_PATTERNS = [
    "ST板块",
    "*ST板块",  # ST 板块本身
    "融资融券",
    "沪股通",
    "深股通",
    "港股通",  # 通道类
]


def is_valid_sector(name: str) -> bool:
    """检查是否为有效的概念板块"""
    if not name:
        return False
    for pattern in EXCLUDED_SECTOR_PATTERNS:
        if pattern in name:
            return False
    return True


def is_restricted_board(code: str) -> bool:
    """检查是否为门槛板块股票（科创板、创业板、北交所）

    门槛要求:
    - 科创板 (688xxx): 50万资产 + 2年经验
    - 创业板 (300xxx, 301xxx): 10万资产 + 2年经验
    - 北交所 (8xxxxx, 4xxxxx): 50万资产 + 2年经验
    """
    if not code:
        return False
    code = str(code).strip()
    # 科创板: 688 开头
    if code.startswith("688"):
        return True
    # 创业板: 300, 301 开头
    if code.startswith("300") or code.startswith("301"):
        return True
    # 北交所: 8 或 4 开头（通常是 83xxxx, 43xxxx, 87xxxx 等）
    if code.startswith("8") or code.startswith("4"):
        return True
    return False


def is_valid_stock(name: str, code: str = "") -> bool:
    """检查是否为有效股票（过滤ST、门槛板块等）"""
    if not name:
        return False
    # 过滤 ST、*ST、PT 股票
    if name.startswith("ST") or name.startswith("*ST") or name.startswith("PT"):
        return False
    # 过滤门槛板块（科创板、创业板、北交所）
    if code and is_restricted_board(code):
        return False
    return True


def calculate_leader_score(stock: Dict, hot_rank: int = 9999) -> float:
    """计算龙头股评分"""
    # 人气排名得分
    rank_score = max(0, 100 - hot_rank)

    # 涨跌幅得分（排除异常值）
    change_pct = float(stock.get("change_pct", 0) or 0)
    if 0 < change_pct <= 9.8:  # 正常涨幅
        change_score = change_pct * 10
    elif change_pct > 9.8:  # 涨停，但可能异常
        change_score = 50
    else:
        change_score = 0

    # 成交额得分
    amount = float(stock.get("amount", 0) or 0)
    amount_score = min(100, amount / 1000000000 * 10)  # 10亿为满分

    # 换手率得分
    turnover = float(stock.get("turnover_rate", 0) or 0)
    if 2 <= turnover <= 15:  # 适中换手率
        turnover_score = 80
    elif 15 < turnover <= 25:
        turnover_score = 60
    else:
        turnover_score = 30

    # 综合评分
    total_score = (
        rank_score * 0.4
        + change_score * 0.3
        + amount_score * 0.2
        + turnover_score * 0.1
    )

    return total_score


def select_top_leader(
    sector_stocks: List[Dict], hot_ranks: Dict[str, int] = None
) -> Dict:
    """选择板块龙头股（龙一）"""
    if not sector_stocks:
        return None

    hot_ranks = hot_ranks or {}

    # 计算综合评分
    scored_stocks = []
    for stock in sector_stocks:
        hot_rank = hot_ranks.get(stock.get("code", ""), 9999)
        score = calculate_leader_score(stock, hot_rank)
        scored_stocks.append((stock, score))

    # 按评分排序，返回最高分股票
    scored_stocks.sort(key=lambda x: x[1], reverse=True)
    return scored_stocks[0][0] if scored_stocks else None


# ============================================
# 数据模型
# ============================================


@dataclass
class StockInfo:
    """股票信息"""

    code: str
    name: str
    price: float = 0.0
    change_pct: float = 0.0
    hot_rank: int = 0  # 人气排名
    turnover_rate: float = 0.0  # 换手率
    volume: float = 0.0  # 成交量
    main_net_inflow: float = 0.0  # 主力净流入


@dataclass
class TechIndicators:
    """技术指标"""

    # MACD
    macd_dif: float = 0.0
    macd_dea: float = 0.0
    macd_hist: float = 0.0
    macd_signal: str = ""  # 金叉/死叉/多头/空头

    # KDJ
    kdj_k: float = 0.0
    kdj_d: float = 0.0
    kdj_j: float = 0.0
    kdj_signal: str = ""  # 超买/超卖/金叉/死叉

    # RSI
    rsi_6: float = 0.0
    rsi_12: float = 0.0
    rsi_24: float = 0.0
    rsi_signal: str = ""  # 超买/超卖/正常

    # 布林带
    boll_upper: float = 0.0
    boll_mid: float = 0.0
    boll_lower: float = 0.0
    boll_signal: str = ""  # 上轨压力/中轨支撑/下轨支撑

    # MA均线
    ma5: float = 0.0
    ma10: float = 0.0
    ma20: float = 0.0
    ma60: float = 0.0
    ma_signal: str = ""  # 多头排列/空头排列/震荡


@dataclass
class BuyRecommendation:
    """买入建议"""

    score: int = 0  # 0-100 综合评分
    signal: str = ""  # 强烈推荐/推荐/观望/回避
    reasons: List[str] = field(default_factory=list)
    risks: List[str] = field(default_factory=list)
    strategy: str = ""  # 策略类型: 强势追涨/回调低吸/突破买入/观望
    buy_price: float = 0.0  # 建议买入价
    stop_loss: float = 0.0  # 止损价
    target_price: float = 0.0  # 目标价
    buyable: bool = False  # 是否可买入


@dataclass
class LeaderStock:
    """龙头股详情"""

    stock: StockInfo
    indicators: TechIndicators
    recommendation: BuyRecommendation
    kline_summary: str = ""  # K线形态总结
    leader_score: float = 0.0  # 龙头评分


@dataclass
class HotSector:
    """热门板块"""

    name: str
    code: str = ""
    hot_rank: int = 0  # 热度排名
    change_pct: float = 0.0  # 涨跌幅
    main_net_inflow: float = 0.0  # 主力净流入（亿）
    turnover_rate: float = 0.0  # 换手率
    up_count: int = 0  # 上涨家数
    down_count: int = 0  # 下跌家数
    leaders: List[LeaderStock] = field(default_factory=list)  # 龙头股列表
    heat_trend: str = ""  # 近3天热度趋势: 上升/下降/持平
    category: str = "concept"  # 分类: concept/industry/top_gainer
    category_name: str = "🔥 热门概念"  # 分类显示名
    hist: List[Dict] = field(default_factory=list)  # 历史走势


# ============================================
# 数据获取层
# ============================================


class DataFetcher:
    """数据获取器，整合 同花顺问财 + iFinD + AKShare + BaoStock + AData"""

    def __init__(self, wencai_cookie: str = "", use_cache: bool = True):
        self.ak = None
        self.adata = None
        self.pywencai = None
        self.baostock = None
        self.ifind = None  # iFinD 数据接口
        self.wencai_cookie = wencai_cookie or WENCAI_COOKIE
        self.use_cache = use_cache
        self.cache = get_cache_manager() if use_cache else None
        self.circuit_breaker = get_circuit_breaker()
        self._wencai_cookie_valid = True  # Cookie 有效性标记
        self._ifind_logged_in = False  # iFinD 登录状态
        self._init_libs()

    def _init_libs(self):
        """初始化数据库"""
        # 1. 同花顺问财 (优先)
        try:
            import pywencai

            self.pywencai = pywencai
            if self.wencai_cookie:
                print("[OK] 同花顺问财(pywencai) 已加载 (有cookie)")
                self._check_wencai_cookie()
            else:
                print(
                    "[WARN] 同花顺问财(pywencai) 已加载，但未配置cookie，部分功能受限"
                )
        except ImportError:
            print("[WARN] pywencai 未安装，运行: pip install pywencai")
            print("       注意: 需要安装 Node.js v16+")

        # 2. AKShare
        try:
            import akshare as ak

            self.ak = ak
            print("[OK] AKShare 已加载")
        except ImportError:
            print("[WARN] AKShare 未安装，运行: pip install akshare")

        # 3. BaoStock (新增 - 免费历史数据)
        try:
            import baostock as bs

            self.baostock = bs
            # 登录 BaoStock
            login_result = bs.login()
            if login_result.error_code == "0":
                print("[OK] BaoStock 已加载并登录")
            else:
                print(f"[WARN] BaoStock 登录失败: {login_result.error_msg}")
                self.baostock = None
        except ImportError:
            print(
                "[INFO] BaoStock 未安装，运行: pip install baostock (可选，用于历史K线)"
            )
        except Exception as e:
            print(f"[WARN] BaoStock 初始化失败: {e}")

        # 4. iFinD (同花顺专业数据接口)
        self._init_ifind()

        # 5. AData (备选)
        try:
            import adata

            self.adata = adata
            print("[OK] AData 已加载")
        except ImportError:
            print("[INFO] AData 未安装 (可选)")

    def _init_ifind(self):
        """初始化 iFinD 数据接口"""
        # iFinD 账号配置
        ifind_user = os.environ.get("IFIND_USER", "")
        ifind_pass = os.environ.get("IFIND_PASS", "")

        if not ifind_user or not ifind_pass:
            print("[INFO] iFinD 未配置 (设置 IFIND_USER 和 IFIND_PASS 环境变量)")
            return

        try:
            from iFinDPy import THS_iFinDLogin, THS_iFinDLogout

            self.ifind = {
                "login": THS_iFinDLogin,
                "logout": THS_iFinDLogout,
            }

            # 尝试登录
            result = THS_iFinDLogin(ifind_user, ifind_pass)
            if result == 0:
                self._ifind_logged_in = True
                print("[OK] iFinD 已登录")

                # 导入其他函数
                from iFinDPy import (
                    THS_HighFrequenceSequence,
                    THS_RealtimeQuotes,
                    THS_HistoryQuotes,
                    THS_BasicData,
                    THS_DateQuery,
                    THS_DataPool,
                )

                self.ifind.update(
                    {
                        "realtime": THS_RealtimeQuotes,
                        "history": THS_HistoryQuotes,
                        "basic": THS_BasicData,
                        "date_query": THS_DateQuery,
                        "data_pool": THS_DataPool,
                        "high_freq": THS_HighFrequenceSequence,
                    }
                )
            else:
                print(f"[WARN] iFinD 登录失败，错误码: {result}")
                print("       请检查账号密码是否正确")
        except ImportError:
            print("[INFO] iFinD (iFinDPy) 未安装")
            print("       下载地址: https://quantapi.51ifind.com/")
        except Exception as e:
            print(f"[WARN] iFinD 初始化失败: {e}")

    def _check_wencai_cookie(self):
        """检查问财Cookie是否有效"""
        if not self.pywencai or not self.wencai_cookie:
            return

        try:
            # 简单测试查询
            result = self.pywencai.get(
                query="上证指数", loop=False, log=False, cookie=self.wencai_cookie
            )
            if result is None or (hasattr(result, "empty") and result.empty):
                self._wencai_cookie_valid = False
                print("[WARN] ⚠️ 同花顺问财 Cookie 可能已过期!")
                print(
                    "       请重新获取: 登录 https://www.iwencai.com 后从浏览器复制 Cookie"
                )
                print("       设置环境变量: export WENCAI_COOKIE='your_new_cookie'")
            else:
                self._wencai_cookie_valid = True
        except Exception as e:
            if (
                "cookie" in str(e).lower()
                or "登录" in str(e)
                or "login" in str(e).lower()
            ):
                self._wencai_cookie_valid = False
                print(f"[WARN] ⚠️ 同花顺问财 Cookie 无效: {e}")
                print("       请重新获取 Cookie")

    def __del__(self):
        """析构时登出 BaoStock 和 iFinD"""
        if self.baostock:
            try:
                self.baostock.logout()
            except:
                pass

        if self._ifind_logged_in and self.ifind:
            try:
                self.ifind["logout"]()
            except:
                pass

    # ========================================
    # iFinD 数据接口 (优先级最高)
    # ========================================

    def get_ifind_hot_sectors(self, top_n: int = 20) -> List[Dict]:
        """通过 iFinD 获取热门概念板块"""
        if not self._ifind_logged_in or not self.ifind:
            return []

        results = []
        try:
            # 获取概念板块行情
            # THS_DataPool 获取板块数据
            data = self.ifind["data_pool"](
                "block",  # 板块数据池
                f"{datetime.now().strftime('%Y-%m-%d')};001005010",  # 概念板块
                "date:Y,thscode:Y,block_name:Y,change:Y,open:Y,high:Y,low:Y,close:Y",
            )

            if data and hasattr(data, "data") and data.data:
                import pandas as pd

                df = pd.DataFrame(data.data)
                df = df.sort_values("change", ascending=False).head(top_n)

                for _, row in df.iterrows():
                    results.append(
                        {
                            "name": row.get("block_name", ""),
                            "code": row.get("thscode", ""),
                            "change_pct": float(row.get("change", 0) or 0),
                            "source": "ifind",
                        }
                    )
                print(f"[OK] iFinD 获取到 {len(results)} 个热门板块")
        except Exception as e:
            print(f"[WARN] iFinD 板块数据获取失败: {e}")
            self.circuit_breaker.record_failure("ifind")

        return results

    def get_ifind_stock_kline(self, code: str, days: int = 60) -> List[Dict]:
        """通过 iFinD 获取股票K线数据"""
        if not self._ifind_logged_in or not self.ifind:
            return []

        results = []
        try:
            # 转换代码格式: 000001 -> 000001.SZ, 600001 -> 600001.SH
            if code.startswith("6"):
                ths_code = f"{code}.SH"
            else:
                ths_code = f"{code}.SZ"

            end_date = datetime.now().strftime("%Y-%m-%d")
            start_date = (datetime.now() - timedelta(days=days + 30)).strftime(
                "%Y-%m-%d"
            )

            # 获取历史行情
            data = self.ifind["history"](
                ths_code,
                "open,high,low,close,volume,amount,turnoverRatio",
                "",  # 参数
                start_date,
                end_date,
            )

            if data and hasattr(data, "data") and data.data:
                for item in data.data:
                    results.append(
                        {
                            "date": item.get("time", "")[:10],
                            "open": float(item.get("open", 0) or 0),
                            "high": float(item.get("high", 0) or 0),
                            "low": float(item.get("low", 0) or 0),
                            "close": float(item.get("close", 0) or 0),
                            "volume": float(item.get("volume", 0) or 0),
                            "amount": float(item.get("amount", 0) or 0),
                            "turnover": float(item.get("turnoverRatio", 0) or 0),
                        }
                    )
                self.circuit_breaker.record_success("ifind")
        except Exception as e:
            print(f"[WARN] iFinD K线获取失败: {e}")
            self.circuit_breaker.record_failure("ifind")

        return results

    def get_ifind_realtime_quote(self, codes: List[str]) -> Dict[str, Dict]:
        """通过 iFinD 获取实时行情"""
        if not self._ifind_logged_in or not self.ifind:
            return {}

        results = {}
        try:
            # 转换代码格式
            ths_codes = []
            for code in codes:
                if code.startswith("6"):
                    ths_codes.append(f"{code}.SH")
                else:
                    ths_codes.append(f"{code}.SZ")

            # 获取实时行情
            data = self.ifind["realtime"](
                ",".join(ths_codes), "latest,changeRatio,amount,volume,turnoverRatio"
            )

            if data and hasattr(data, "data") and data.data:
                for item in data.data:
                    code = item.get("thscode", "").split(".")[0]
                    results[code] = {
                        "price": float(item.get("latest", 0) or 0),
                        "change_pct": float(item.get("changeRatio", 0) or 0),
                        "amount": float(item.get("amount", 0) or 0),
                        "volume": float(item.get("volume", 0) or 0),
                        "turnover_rate": float(item.get("turnoverRatio", 0) or 0),
                    }
        except Exception as e:
            print(f"[WARN] iFinD 实时行情获取失败: {e}")

        return results

    # ========================================
    # 同花顺问财数据接口
    # ========================================

    def wencai_query(self, query: str, **kwargs) -> Optional[any]:
        """执行问财查询"""
        if not self.pywencai:
            return None

        try:
            params = {
                "query": query,
                "loop": False,  # 不循环分页，避免频繁请求
                "log": False,
            }
            if self.wencai_cookie:
                params["cookie"] = self.wencai_cookie
            params.update(kwargs)

            result = self.pywencai.get(**params)
            return result
        except Exception as e:
            print(f"[WARN] 问财查询失败: {e}")
            return None

    def get_wencai_hot_sectors(self, top_n: int = 20) -> List[Dict]:
        """通过问财获取热门板块"""
        results = []

        # 查询热门概念板块
        queries = [
            "今日涨幅排名前20的概念板块",
            "主力资金净流入排名前20的概念",
        ]

        for query in queries:
            df = self.wencai_query(query)
            if df is not None and hasattr(df, "iterrows"):
                for _, row in df.iterrows():
                    name = str(row.get("概念名称", row.get("板块名称", "")))
                    if not name or name in [r["name"] for r in results]:
                        continue
                    results.append(
                        {
                            "name": name,
                            "change_pct": float(
                                row.get("涨跌幅", row.get("概念涨跌幅", 0)) or 0
                            ),
                            "source": "wencai",
                        }
                    )
                print(f"[OK] 问财获取到 {len(results)} 个热门板块")
                break  # 成功获取后退出

        return results[:top_n]

    def get_wencai_hot_stocks(self, top_n: int = 100) -> Dict[str, int]:
        """通过问财获取人气排名"""
        hot_map = {}

        # 查询人气榜
        query = f"人气榜排行前{top_n}名"
        df = self.wencai_query(query)

        if df is not None and hasattr(df, "iterrows"):
            for i, row in df.iterrows():
                code = (
                    str(row.get("股票代码", row.get("code", "")))
                    .replace(".SH", "")
                    .replace(".SZ", "")
                )
                code = code.zfill(6)
                hot_map[code] = i + 1
            print(f"[OK] 问财获取到 {len(hot_map)} 只人气股票")

        return hot_map

    def get_wencai_sector_leaders(self, sector_name: str, top_n: int = 5) -> List[Dict]:
        """通过问财获取板块龙头股"""
        results = []

        # 查询板块成分股，按涨幅/人气排序
        queries = [
            f"{sector_name}概念 涨幅排名前{top_n}",
            f"{sector_name} 成分股 涨幅最大的{top_n}只",
        ]

        for query in queries:
            df = self.wencai_query(query)
            if df is not None and hasattr(df, "iterrows"):
                for _, row in df.iterrows():
                    code = (
                        str(row.get("股票代码", row.get("code", "")))
                        .replace(".SH", "")
                        .replace(".SZ", "")
                    )
                    results.append(
                        {
                            "code": code.zfill(6),
                            "name": str(row.get("股票简称", row.get("股票名称", ""))),
                            "price": float(row.get("最新价", row.get("现价", 0)) or 0),
                            "change_pct": float(row.get("涨跌幅", 0) or 0),
                            "turnover_rate": float(row.get("换手率", 0) or 0),
                        }
                    )
                if results:
                    break

        return results[:top_n]

    def get_wencai_strong_stocks(self) -> List[Dict]:
        """通过问财获取强势股（连板股、涨停股）"""
        results = []

        # 查询连板股
        query = "连续涨停天数大于1，非ST，非北交所，按连续涨停天数排序"
        df = self.wencai_query(query)

        if df is not None and hasattr(df, "iterrows"):
            for _, row in df.iterrows():
                code = (
                    str(row.get("股票代码", "")).replace(".SH", "").replace(".SZ", "")
                )
                results.append(
                    {
                        "code": code.zfill(6),
                        "name": str(row.get("股票简称", "")),
                        "price": float(row.get("最新价", 0) or 0),
                        "change_pct": float(row.get("涨跌幅", 0) or 0),
                        "limit_up_days": int(row.get("连续涨停天数", 0) or 0),
                    }
                )
            print(f"[OK] 问财获取到 {len(results)} 只连板股")

        return results

    def get_hot_concepts_by_popularity(self, top_n: int = 20) -> List[Dict]:
        """获取热门概念板块（基于人气/资金流向）- 支持缓存"""

        # 尝试从缓存获取（5分钟有效期）
        cache_key = f"hot_concepts_{top_n}"
        if self.cache:
            cached = self.cache.get_sector_cache(cache_key)
            if cached:
                print(f"[CACHE] 使用缓存的热门板块数据: {len(cached)} 个")
                return cached

        results = []

        # 方法0: 最优先使用 iFinD（专业数据）
        if self._ifind_logged_in and not self.circuit_breaker.is_open("ifind"):
            ifind_results = self.get_ifind_hot_sectors(top_n * 2)
            if ifind_results:
                results = ifind_results
                print(f"[OK] 使用 iFinD 数据: {len(results)} 个热门板块")

        # 方法1: 使用同花顺问财（检查 Cookie 有效性）
        if (
            not results
            and self.pywencai
            and self.wencai_cookie
            and self._wencai_cookie_valid
        ):
            if not self.circuit_breaker.is_open("wencai"):
                wencai_results = self.get_wencai_hot_sectors(top_n * 2)
                if wencai_results:
                    results = wencai_results
                    print(f"[OK] 使用问财数据: {len(results)} 个热门板块")
                    self.circuit_breaker.record_success("wencai")
                else:
                    self.circuit_breaker.record_failure("wencai")

        # 方法2: AKShare 东方财富概念板块 (补充或备用，带重试)
        if len(results) < top_n and self.ak:
            for attempt in range(3):
                try:
                    # 概念板块行情
                    df = self.ak.stock_board_concept_name_em()
                    df = df.head(top_n * 4)  # 多取一些用于筛选

                    existing_names = {r["name"] for r in results}
                    for _, row in df.iterrows():
                        name = row.get("板块名称", "")
                        # 过滤无效板块（昨日涨停、连板等）
                        if not is_valid_sector(name):
                            continue
                        if name in existing_names:
                            continue
                        results.append(
                            {
                                "name": name,
                                "code": row.get("板块代码", ""),
                                "change_pct": float(row.get("涨跌幅", 0) or 0),
                                "turnover_rate": float(row.get("换手率", 0) or 0),
                                "up_count": int(row.get("上涨家数", 0) or 0),
                                "down_count": int(row.get("下跌家数", 0) or 0),
                                "leader_stock": row.get("领涨股票", ""),
                                "source": "akshare_em",
                            }
                        )
                    print(f"[OK] AKShare 补充到 {len(results)} 个概念板块")
                    break
                except Exception as e:
                    if attempt < 2:
                        print(f"[WARN] AKShare 重试中... ({attempt + 1}/3)")
                        time.sleep(2 * (attempt + 1))
                    else:
                        print(f"[ERR] AKShare 概念板块获取失败: {e}")

        # 方法3: 使用资金流向数据作为备用
        if len(results) < top_n and self.ak:
            try:
                df = self.ak.stock_fund_flow_concept(symbol="即时")
                self._fund_flow_cache = df  # 缓存用于后续获取领涨股
                existing_names = {r["name"] for r in results}
                for _, row in df.head(top_n * 4).iterrows():
                    name = row.get("行业", "")  # 字段名是'行业'
                    # 过滤无效板块
                    if not is_valid_sector(name):
                        continue
                    if not name or name in existing_names:
                        continue
                    results.append(
                        {
                            "name": name,
                            "code": "",
                            "change_pct": float(row.get("行业-涨跌幅", 0) or 0),
                            "main_net_inflow": float(row.get("净额", 0) or 0)
                            / 100000000,
                            "up_count": int(row.get("公司家数", 0) or 0),
                            "down_count": 0,
                            "leader_stock": row.get("领涨股", ""),
                            "leader_change_pct": float(
                                row.get("领涨股-涨跌幅", 0) or 0
                            ),
                            "leader_price": float(row.get("当前价", 0) or 0),
                            "source": "akshare_fund_flow",
                        }
                    )
                print(f"[OK] 资金流向备用数据: {len(results)} 个概念板块")
            except Exception as e:
                print(f"[WARN] 资金流向备用数据获取失败: {e}")

        # 保存到缓存（5分钟有效期）
        final_results = results[:top_n]
        if self.cache and final_results:
            self.cache.set_sector_cache(cache_key, final_results, ttl_seconds=300)

        return final_results

    def get_concept_fund_flow(self, top_n: int = 20) -> List[Dict]:
        """获取概念板块资金流向（判断热度）"""
        results = []

        if self.ak:
            try:
                # 尝试新版 API
                df = self.ak.stock_fund_flow_concept(symbol="即时")
                df = df.head(top_n)

                for _, row in df.iterrows():
                    # 兼容不同版本的列名
                    name = row.get("名称", row.get("行业", ""))
                    # 净额单位是亿，不需要再除以1e8
                    net_inflow = float(
                        row.get("净额", row.get("主力净流入-净额", 0)) or 0
                    )
                    change_pct = float(
                        row.get("行业-涨跌幅", row.get("涨跌幅", 0)) or 0
                    )
                    results.append(
                        {
                            "name": name,
                            "main_net_inflow": net_inflow,  # 单位：亿
                            "main_net_inflow_pct": 0,  # 新 API 没有这个字段
                            "change_pct": change_pct,
                        }
                    )
                print(f"[OK] 获取到 {len(results)} 个板块资金流向")
            except Exception as e:
                print(f"[WARN] 概念资金流向获取失败: {e}")
                # 尝试备用 API
                try:
                    df = self.ak.stock_sector_fund_flow_rank(
                        indicator="今日", sector_type="概念资金流"
                    )
                    df = df.head(top_n)
                    for _, row in df.iterrows():
                        results.append(
                            {
                                "name": row.get("名称", ""),
                                "main_net_inflow": float(
                                    row.get("主力净流入-净额", 0) or 0
                                )
                                / 1e8,
                                "change_pct": float(row.get("涨跌幅", 0) or 0),
                            }
                        )
                    print(f"[OK] 备用API获取到 {len(results)} 个板块资金流向")
                except Exception as e2:
                    print(f"[ERR] 板块资金流向获取失败: {e2}")

        return results

    def get_stock_hot_rank_full(self, top_n: int = 100) -> List[Dict]:
        """获取完整的人气排名数据（包含代码、名称、价格等）"""
        results = []

        if self.ak:
            try:
                df = self.ak.stock_hot_rank_em()
                for _, row in df.head(top_n).iterrows():
                    code = (
                        str(row.get("代码", ""))
                        .replace("SZ", "")
                        .replace("SH", "")
                        .replace("BJ", "")
                    )
                    results.append(
                        {
                            "code": code,
                            "name": str(row.get("股票名称", "")).replace(
                                " ", ""
                            ),  # 去除空格
                            "price": float(row.get("最新价", 0) or 0),
                            "change_pct": float(row.get("涨跌幅", 0) or 0),
                            "rank": int(row.get("当前排名", 9999)),
                        }
                    )
            except Exception as e:
                print(f"[ERR] 人气榜获取失败: {e}")

        return results

    def get_stock_hot_rank(self, top_n: int = 100) -> Dict[str, int]:
        """获取个股人气排名"""
        hot_map = {}

        # 方法1: 优先使用同花顺问财人气榜
        if self.pywencai and self.wencai_cookie:
            hot_map = self.get_wencai_hot_stocks(top_n)
            if hot_map:
                print(f"[OK] 使用问财人气榜: {len(hot_map)} 只股票")
                return hot_map

        # 方法2: 东方财富人气榜
        if self.ak:
            try:
                df = self.ak.stock_hot_rank_em()
                for _, row in df.iterrows():
                    code = str(row.get("代码", "")).zfill(6)
                    rank = int(row.get("当前排名", 9999))
                    hot_map[code] = rank
                print(f"[OK] 东方财富人气榜: {len(hot_map)} 只股票")
            except Exception as e:
                print(f"[ERR] 人气排名获取失败: {e}")

        return hot_map

    def get_concept_constituents(self, concept_name: str) -> List[Dict]:
        """获取概念板块成分股"""
        results = []

        # 方法1: 优先使用同花顺问财
        if self.pywencai and self.wencai_cookie:
            wencai_results = self.get_wencai_sector_leaders(concept_name, top_n=20)
            if wencai_results:
                results = wencai_results
                print(f"[OK] 问财获取 {concept_name} 成分股: {len(results)} 只")
                return results

        # 方法2: AKShare 东方财富（带重试）
        if self.ak:
            for attempt in range(3):
                try:
                    time.sleep(1 + attempt)  # 递增延迟
                    df = self.ak.stock_board_concept_cons_em(symbol=concept_name)
                    for _, row in df.iterrows():
                        results.append(
                            {
                                "code": str(row.get("代码", "")).zfill(6),
                                "name": row.get("名称", ""),
                                "price": float(row.get("最新价", 0) or 0),
                                "change_pct": float(row.get("涨跌幅", 0) or 0),
                                "turnover_rate": float(row.get("换手率", 0) or 0),
                                "volume": float(row.get("成交量", 0) or 0),
                            }
                        )
                    break  # 成功则退出重试
                except Exception as e:
                    if attempt == 2:
                        print(f"[ERR] 获取 {concept_name} 成分股失败: {e}")

        return results

    def get_stock_kline(self, code: str, days: int = 60) -> List[Dict]:
        """获取股票K线数据（支持缓存和增量更新）"""

        # 1. 尝试从缓存获取
        if self.cache:
            cached = self.cache.get_cached_kline(code, days)
            if cached:
                # 检查是否需要增量更新（最新数据是否是今天）
                today = datetime.now().strftime("%Y-%m-%d")
                if cached[-1]["date"] >= today or not self._is_trading_day():
                    return cached[-days:]

                # 增量更新：只获取缺失的数据
                last_date = cached[-1]["date"]
                new_data = self._fetch_kline_incremental(code, last_date)
                if new_data:
                    self.cache.save_kline(code, new_data)
                    cached.extend(new_data)
                return cached[-days:]

        # 2. 全量获取
        results = self._fetch_kline_full(code, days)

        # 3. 保存到缓存
        if self.cache and results:
            self.cache.save_kline(code, results)

        return results

    def _is_trading_day(self) -> bool:
        """判断今天是否是交易日（简单判断：周一到周五）"""
        today = datetime.now()
        return today.weekday() < 5  # 0-4 是周一到周五

    def _fetch_kline_incremental(self, code: str, last_date: str) -> List[Dict]:
        """增量获取K线数据"""
        results = []
        start_date = (
            datetime.strptime(last_date, "%Y-%m-%d") + timedelta(days=1)
        ).strftime("%Y%m%d")
        end_date = datetime.now().strftime("%Y%m%d")

        if start_date >= end_date:
            return []

        # 使用 AKShare 获取增量数据
        if self.ak and not self.circuit_breaker.is_open("akshare_kline"):
            try:
                df = self.ak.stock_zh_a_hist(
                    symbol=code,
                    period="daily",
                    start_date=start_date,
                    end_date=end_date,
                    adjust="qfq",
                )
                for _, row in df.iterrows():
                    results.append(
                        {
                            "date": str(row.get("日期", "")),
                            "open": float(row.get("开盘", 0)),
                            "high": float(row.get("最高", 0)),
                            "low": float(row.get("最低", 0)),
                            "close": float(row.get("收盘", 0)),
                            "volume": float(row.get("成交量", 0)),
                            "amount": float(row.get("成交额", 0)),
                            "turnover": float(row.get("换手率", 0) or 0),
                        }
                    )
                self.circuit_breaker.record_success("akshare_kline")
            except Exception as e:
                self.circuit_breaker.record_failure("akshare_kline")

        return results

    def _fetch_kline_full(self, code: str, days: int) -> List[Dict]:
        """全量获取K线数据（优先级：iFinD > BaoStock > AKShare）"""
        results = []
        end_date = datetime.now().strftime("%Y%m%d")
        start_date = (datetime.now() - timedelta(days=days + 30)).strftime("%Y%m%d")

        # 方法0: iFinD (专业数据，最优先)
        if self._ifind_logged_in and not self.circuit_breaker.is_open("ifind"):
            results = self.get_ifind_stock_kline(code, days)
            if results:
                return results

        # 方法1: BaoStock (免费且稳定)
        if self.baostock and not self.circuit_breaker.is_open("baostock"):
            try:
                # 转换股票代码格式: 000001 -> sz.000001, 600001 -> sh.600001
                if code.startswith("6"):
                    bs_code = f"sh.{code}"
                elif code.startswith("0") or code.startswith("3"):
                    bs_code = f"sz.{code}"
                else:
                    bs_code = f"sz.{code}"

                rs = self.baostock.query_history_k_data_plus(
                    bs_code,
                    "date,open,high,low,close,volume,amount,turn",
                    start_date=start_date[:4]
                    + "-"
                    + start_date[4:6]
                    + "-"
                    + start_date[6:],
                    end_date=end_date[:4] + "-" + end_date[4:6] + "-" + end_date[6:],
                    frequency="d",
                    adjustflag="2",  # 前复权
                )

                while rs.error_code == "0" and rs.next():
                    row = rs.get_row_data()
                    if row[0]:  # 有数据
                        results.append(
                            {
                                "date": row[0],
                                "open": float(row[1]) if row[1] else 0,
                                "high": float(row[2]) if row[2] else 0,
                                "low": float(row[3]) if row[3] else 0,
                                "close": float(row[4]) if row[4] else 0,
                                "volume": float(row[5]) if row[5] else 0,
                                "amount": float(row[6]) if row[6] else 0,
                                "turnover": float(row[7]) if row[7] else 0,
                            }
                        )

                if results:
                    self.circuit_breaker.record_success("baostock")
                    return results
            except Exception as e:
                self.circuit_breaker.record_failure("baostock")
                print(f"[WARN] BaoStock 获取 {code} K线失败: {e}")

        # 方法2: AKShare (备选)
        if self.ak and not self.circuit_breaker.is_open("akshare_kline"):
            try:
                df = self.ak.stock_zh_a_hist(
                    symbol=code,
                    period="daily",
                    start_date=start_date,
                    end_date=end_date,
                    adjust="qfq",
                )

                for _, row in df.iterrows():
                    results.append(
                        {
                            "date": str(row.get("日期", "")),
                            "open": float(row.get("开盘", 0)),
                            "high": float(row.get("最高", 0)),
                            "low": float(row.get("最低", 0)),
                            "close": float(row.get("收盘", 0)),
                            "volume": float(row.get("成交量", 0)),
                            "amount": float(row.get("成交额", 0)),
                            "turnover": float(row.get("换手率", 0) or 0),
                        }
                    )

                if results:
                    self.circuit_breaker.record_success("akshare_kline")
            except Exception as e:
                self.circuit_breaker.record_failure("akshare_kline")
                print(f"[ERR] AKShare 获取 {code} K线失败: {e}")

        return results

    def get_xueqiu_hot(self) -> List[Dict]:
        """获取雪球热度榜"""
        results = []

        if self.ak:
            try:
                df = self.ak.stock_hot_follow_xq(symbol="最热门")
                for i, row in df.iterrows():
                    results.append(
                        {
                            "code": str(row.get("股票代码", "")).zfill(6),
                            "name": row.get("股票简称", ""),
                            "follow_count": int(row.get("关注", 0) or 0),
                            "rank": i + 1,
                        }
                    )
                print(f"[OK] 雪球热度榜获取到 {len(results)} 只股票")
            except Exception as e:
                print(f"[ERR] 雪球热度榜获取失败: {e}")

        return results

    def get_hot_industry_sectors(self, top_n: int = 10) -> List[Dict]:
        """获取热门行业板块"""
        results = []

        if self.ak:
            for attempt in range(3):
                try:
                    df = self.ak.stock_board_industry_name_em()
                    for _, row in df.head(top_n * 2).iterrows():
                        results.append(
                            {
                                "name": row.get("板块名称", ""),
                                "code": str(row.get("板块代码", "")),
                                "change_pct": float(row.get("涨跌幅", 0) or 0),
                                "up_count": int(row.get("上涨家数", 0) or 0),
                                "down_count": int(row.get("下跌家数", 0) or 0),
                                "turnover_rate": float(row.get("换手率", 0) or 0),
                                "main_net_inflow": float(row.get("主力净流入", 0) or 0)
                                / 100000000,  # 转为亿
                                "category": "industry",
                                "category_name": "🏭 热门行业",
                            }
                        )
                    print(f"[OK] 获取到 {len(results)} 个行业板块")
                    break
                except Exception as e:
                    if attempt < 2:
                        print(f"[WARN] 行业板块重试中... ({attempt + 1}/3)")
                        time.sleep(2 * (attempt + 1))
                    else:
                        print(f"[ERR] 行业板块获取失败: {e}")

            # 备用：使用行业资金流向
            if not results:
                try:
                    df = self.ak.stock_sector_fund_flow_rank(
                        indicator="今日", sector_type="行业资金流"
                    )
                    for _, row in df.head(top_n * 2).iterrows():
                        results.append(
                            {
                                "name": row.get("名称", ""),
                                "code": "",
                                "change_pct": float(row.get("今日涨跌幅", 0) or 0),
                                "main_net_inflow": float(
                                    row.get("今日主力净流入-净额", 0) or 0
                                )
                                / 100000000,
                                "up_count": 0,
                                "down_count": 0,
                                "category": "industry",
                                "category_name": "🏭 热门行业",
                            }
                        )
                    print(f"[OK] 行业资金流向备用数据: {len(results)} 个行业")
                except Exception as e:
                    print(f"[WARN] 行业资金流向备用数据获取失败: {e}")

        return results[:top_n]

    def get_industry_constituents(self, industry_name: str) -> List[Dict]:
        """获取行业板块成分股"""
        results = []

        if self.ak:
            try:
                df = self.ak.stock_board_industry_cons_em(symbol=industry_name)
                for _, row in df.head(10).iterrows():
                    results.append(
                        {
                            "code": str(row.get("代码", "")).zfill(6),
                            "name": row.get("名称", ""),
                            "price": float(row.get("最新价", 0) or 0),
                            "change_pct": float(row.get("涨跌幅", 0) or 0),
                            "turnover_rate": float(row.get("换手率", 0) or 0),
                        }
                    )
            except Exception as e:
                pass  # 静默失败

        return results


# ============================================
# 技术指标计算
# ============================================


class TechAnalyzer:
    """技术指标分析器"""

    @staticmethod
    def calc_ma(closes: List[float], period: int) -> float:
        """计算简单移动平均"""
        if len(closes) < period:
            return 0.0
        return sum(closes[-period:]) / period

    @staticmethod
    def calc_ema(closes: List[float], period: int) -> List[float]:
        """计算指数移动平均"""
        if len(closes) < period:
            return [0.0] * len(closes)

        emas = []
        multiplier = 2 / (period + 1)

        # 第一个值用SMA
        ema = sum(closes[:period]) / period
        emas.extend([0.0] * (period - 1))
        emas.append(ema)

        for price in closes[period:]:
            ema = (price - ema) * multiplier + ema
            emas.append(ema)

        return emas

    @staticmethod
    def calc_macd(
        closes: List[float], fast: int = 12, slow: int = 26, signal: int = 9
    ) -> Tuple[float, float, float]:
        """计算MACD指标"""
        if len(closes) < slow + signal:
            return 0.0, 0.0, 0.0

        ema_fast = TechAnalyzer.calc_ema(closes, fast)
        ema_slow = TechAnalyzer.calc_ema(closes, slow)

        # DIF = 快线 - 慢线
        dif = [f - s for f, s in zip(ema_fast, ema_slow)]

        # DEA = DIF的EMA
        dea = TechAnalyzer.calc_ema(dif[slow - 1 :], signal)

        if not dea:
            return 0.0, 0.0, 0.0

        current_dif = dif[-1]
        current_dea = dea[-1]
        macd_hist = (current_dif - current_dea) * 2

        return current_dif, current_dea, macd_hist

    @staticmethod
    def calc_kdj(
        highs: List[float],
        lows: List[float],
        closes: List[float],
        n: int = 9,
        m1: int = 3,
        m2: int = 3,
    ) -> Tuple[float, float, float]:
        """计算KDJ指标"""
        if len(closes) < n:
            return 50.0, 50.0, 50.0

        # RSV = (收盘价 - N日最低) / (N日最高 - N日最低) * 100
        lowest = min(lows[-n:])
        highest = max(highs[-n:])

        if highest == lowest:
            rsv = 50.0
        else:
            rsv = (closes[-1] - lowest) / (highest - lowest) * 100

        # K = 2/3 * 前K + 1/3 * RSV (简化计算，直接用当前RSV近似)
        k = rsv
        d = k  # 简化
        j = 3 * k - 2 * d

        return k, d, j

    @staticmethod
    def calc_rsi(
        closes: List[float], periods: List[int] = [6, 12, 24]
    ) -> Dict[int, float]:
        """计算RSI指标"""
        results = {}

        for period in periods:
            if len(closes) < period + 1:
                results[period] = 50.0
                continue

            gains = []
            losses = []

            for i in range(1, len(closes)):
                change = closes[i] - closes[i - 1]
                if change > 0:
                    gains.append(change)
                    losses.append(0)
                else:
                    gains.append(0)
                    losses.append(abs(change))

            # 计算平均涨跌
            avg_gain = sum(gains[-period:]) / period
            avg_loss = sum(losses[-period:]) / period

            if avg_loss == 0:
                results[period] = 100.0
            else:
                rs = avg_gain / avg_loss
                results[period] = 100 - (100 / (1 + rs))

        return results

    @staticmethod
    def calc_boll(
        closes: List[float], period: int = 20, std_mult: float = 2
    ) -> Tuple[float, float, float]:
        """计算布林带"""
        if len(closes) < period:
            return 0.0, 0.0, 0.0

        recent = closes[-period:]
        mid = sum(recent) / period

        # 标准差
        variance = sum((x - mid) ** 2 for x in recent) / period
        std = variance**0.5

        upper = mid + std_mult * std
        lower = mid - std_mult * std

        return upper, mid, lower

    def analyze(self, kline: List[Dict]) -> TechIndicators:
        """综合分析技术指标"""
        if len(kline) < 30:
            return TechIndicators()

        closes = [k["close"] for k in kline]
        highs = [k["high"] for k in kline]
        lows = [k["low"] for k in kline]
        current_price = closes[-1]

        indicators = TechIndicators()

        # MACD
        dif, dea, hist = self.calc_macd(closes)
        indicators.macd_dif = round(dif, 3)
        indicators.macd_dea = round(dea, 3)
        indicators.macd_hist = round(hist, 3)

        if dif > dea and hist > 0:
            indicators.macd_signal = "多头向上"
        elif dif > dea and hist < 0:
            indicators.macd_signal = "金叉形成"
        elif dif < dea and hist > 0:
            indicators.macd_signal = "死叉形成"
        else:
            indicators.macd_signal = "空头向下"

        # KDJ
        k, d, j = self.calc_kdj(highs, lows, closes)
        indicators.kdj_k = round(k, 2)
        indicators.kdj_d = round(d, 2)
        indicators.kdj_j = round(j, 2)

        if j > 100:
            indicators.kdj_signal = "严重超买"
        elif j > 80:
            indicators.kdj_signal = "超买"
        elif j < 0:
            indicators.kdj_signal = "严重超卖"
        elif j < 20:
            indicators.kdj_signal = "超卖"
        elif k > d:
            indicators.kdj_signal = "金叉向上"
        else:
            indicators.kdj_signal = "死叉向下"

        # RSI
        rsi = self.calc_rsi(closes)
        indicators.rsi_6 = round(rsi.get(6, 50), 2)
        indicators.rsi_12 = round(rsi.get(12, 50), 2)
        indicators.rsi_24 = round(rsi.get(24, 50), 2)

        rsi_avg = (indicators.rsi_6 + indicators.rsi_12) / 2
        if rsi_avg > 80:
            indicators.rsi_signal = "严重超买"
        elif rsi_avg > 70:
            indicators.rsi_signal = "超买"
        elif rsi_avg < 20:
            indicators.rsi_signal = "严重超卖"
        elif rsi_avg < 30:
            indicators.rsi_signal = "超卖"
        else:
            indicators.rsi_signal = "正常区间"

        # 布林带
        upper, mid, lower = self.calc_boll(closes)
        indicators.boll_upper = round(upper, 2)
        indicators.boll_mid = round(mid, 2)
        indicators.boll_lower = round(lower, 2)

        if current_price > upper:
            indicators.boll_signal = "突破上轨(强势)"
        elif current_price > mid:
            indicators.boll_signal = "中轨上方运行"
        elif current_price > lower:
            indicators.boll_signal = "中轨下方运行"
        else:
            indicators.boll_signal = "跌破下轨(弱势)"

        # MA均线
        indicators.ma5 = round(self.calc_ma(closes, 5), 2)
        indicators.ma10 = round(self.calc_ma(closes, 10), 2)
        indicators.ma20 = round(self.calc_ma(closes, 20), 2)
        indicators.ma60 = round(self.calc_ma(closes, 60), 2) if len(closes) >= 60 else 0

        # 判断均线排列
        if indicators.ma5 > indicators.ma10 > indicators.ma20:
            indicators.ma_signal = "多头排列"
        elif indicators.ma5 < indicators.ma10 < indicators.ma20:
            indicators.ma_signal = "空头排列"
        else:
            indicators.ma_signal = "震荡整理"

        return indicators


# ============================================
# 买入建议生成
# ============================================


class RecommendationEngine:
    """买入建议引擎 - 基于技术指标和历史走势，区分不同买入策略"""

    def generate(
        self,
        stock: StockInfo,
        indicators: TechIndicators,
        sector_rank: int,
        kline: List[Dict],
    ) -> BuyRecommendation:
        """生成买入建议（区分强势追涨和回调低吸两种策略）"""
        rec = BuyRecommendation()

        # 如果没有K线数据，无法做技术分析
        if not kline or len(kline) < 20:
            rec.score = 0
            rec.signal = "数据不足"
            rec.strategy = "观望"
            rec.risks.append("K线数据不足，无法进行技术分析")
            return rec

        current_price = kline[-1]["close"]

        # ========== 计算关键指标 ==========
        # 近期涨跌幅
        recent_5d = (
            (kline[-1]["close"] - kline[-5]["close"]) / kline[-5]["close"] * 100
            if len(kline) >= 5
            else 0
        )
        recent_20d = (
            (kline[-1]["close"] - kline[-20]["close"]) / kline[-20]["close"] * 100
            if len(kline) >= 20
            else 0
        )

        # 量能比
        vol_ratio = 1.0
        if len(kline) >= 10:
            recent_vol = sum(k["volume"] for k in kline[-5:]) / 5
            prev_vol = sum(k["volume"] for k in kline[-10:-5]) / 5
            if prev_vol > 0:
                vol_ratio = recent_vol / prev_vol

        # ========== 判断买入策略类型 ==========
        is_strong_momentum = False  # 强势追涨型
        is_pullback_buy = False  # 回调低吸型
        is_breakout = False  # 突破买入型

        # 策略1: 强势追涨型 - 热门板块龙头，趋势强劲
        if (
            indicators.ma_signal == "多头排列"
            and "多头" in indicators.macd_signal
            and stock.hot_rank <= 50
            and stock.hot_rank > 0
            and recent_5d > 0
            and recent_5d <= 25
        ):
            is_strong_momentum = True

        # 策略2: 回调低吸型 - 强势股回调到支撑位
        if (
            indicators.ma_signal == "多头排列"
            and ("超卖" in indicators.kdj_signal or "超卖" in indicators.rsi_signal)
            and current_price <= indicators.ma10 * 1.03
            and current_price >= indicators.ma20 * 0.97
        ):
            is_pullback_buy = True

        # 策略3: 突破买入型 - 放量突破关键位置
        if (
            vol_ratio > 1.5
            and current_price > indicators.ma5
            and current_price > indicators.ma10
            and indicators.macd_hist > 0
            and "金叉" in indicators.macd_signal
        ):
            is_breakout = True

        # ========== 综合评分 ==========
        score = 50  # 基础分

        # 1. MACD评分 (最高20分)
        if indicators.macd_signal:
            if "金叉" in indicators.macd_signal:
                score += 20
                rec.reasons.append(f"MACD金叉，买入信号")
            elif "多头向上" in indicators.macd_signal:
                score += 15
                rec.reasons.append(f"MACD多头向上，趋势良好")
            elif "多头" in indicators.macd_signal:
                score += 10
                rec.reasons.append(f"MACD多头排列")
            elif "死叉" in indicators.macd_signal:
                score -= 20
                rec.risks.append(f"MACD死叉，卖出信号")
            elif "空头向下" in indicators.macd_signal:
                score -= 15
                rec.risks.append(f"MACD空头向下，趋势转弱")
            elif "空头" in indicators.macd_signal:
                score -= 10
                rec.risks.append(f"MACD空头排列")

        # 2. KDJ评分 (最高15分)
        if indicators.kdj_signal:
            if "金叉" in indicators.kdj_signal:
                score += 15
                rec.reasons.append(f"KDJ金叉，短期买点")
            elif "超卖" in indicators.kdj_signal:
                score += 10
                rec.reasons.append(f"KDJ超卖区，反弹概率大")
            elif "死叉" in indicators.kdj_signal:
                score -= 10  # 降低惩罚
                rec.risks.append(f"KDJ高位，注意回调")
            elif "超买" in indicators.kdj_signal:
                # 强势股超买不一定是坏事，降低惩罚
                if is_strong_momentum:
                    score -= 3
                else:
                    score -= 8
                rec.risks.append(f"KDJ超买区，回调风险")

        # 3. RSI评分 (最高15分)
        if indicators.rsi_signal:
            if "严重超卖" in indicators.rsi_signal:
                score += 15
                rec.reasons.append(f"RSI严重超卖(<20)，超跌反弹机会")
            elif "超卖" in indicators.rsi_signal:
                score += 10
                rec.reasons.append(f"RSI超卖区，存在反弹机会")
            elif "严重超买" in indicators.rsi_signal:
                # 强势股超买惩罚减轻
                if is_strong_momentum:
                    score -= 5
                else:
                    score -= 12
                rec.risks.append(f"RSI超买(>80)，注意风控")
            elif "超买" in indicators.rsi_signal:
                if is_strong_momentum:
                    score -= 3
                else:
                    score -= 8
                rec.risks.append(f"RSI偏高，注意回调")

        # 4. 均线评分 (最高15分)
        if indicators.ma_signal:
            if indicators.ma_signal == "多头排列":
                score += 15
                rec.reasons.append("均线多头排列，中期趋势向上")
            elif indicators.ma_signal == "空头排列":
                score -= 15
                rec.risks.append("均线空头排列，中期趋势向下")

        # 5. 布林带评分 (最高5分)
        if indicators.boll_signal:
            if (
                "下轨" in indicators.boll_signal
                and "跌破" not in indicators.boll_signal
            ):
                score += 8
                rec.reasons.append("股价接近布林下轨，有支撑")
            elif "跌破下轨" in indicators.boll_signal:
                score -= 5
                rec.risks.append("跌破布林下轨，弱势")

        # 6. 近5日涨跌幅（根据策略调整）
        if len(kline) >= 5:
            if recent_5d > 30:
                score -= 12
                rec.risks.append(f"近5日涨{recent_5d:.1f}%，短期涨幅大")
            elif recent_5d > 20:
                score -= 8 if not is_strong_momentum else -3
                rec.risks.append(f"近5日涨{recent_5d:.1f}%，注意回调")
            elif recent_5d > 10:
                if is_strong_momentum:
                    score += 3  # 强势追涨型，温和上涨是好事
                rec.reasons.append(
                    f"近5日涨{recent_5d:.1f}%，走势强劲"
                ) if recent_5d > 5 else None
            elif recent_5d < -15:
                score += 10
                rec.reasons.append(f"近5日跌{abs(recent_5d):.1f}%，超跌反弹机会")
            elif recent_5d < -10:
                score += 5
                rec.reasons.append(f"近5日跌{abs(recent_5d):.1f}%，存在反弹空间")

        # 7. 近20日涨跌幅
        if len(kline) >= 20:
            if recent_20d > 60:
                score -= 8
                rec.risks.append(f"近20日涨{recent_20d:.1f}%，中期涨幅较大")
            elif recent_20d > 40:
                score -= 5 if not is_strong_momentum else 0
            elif recent_20d < -30:
                score += 5
                rec.reasons.append(f"近20日跌{abs(recent_20d):.1f}%，中期超跌")

        # 8. 量价配合
        if vol_ratio > 1.5 and kline[-1]["close"] > kline[-5]["close"]:
            score += 8
            rec.reasons.append("放量上涨，资金流入")
        elif vol_ratio > 2 and kline[-1]["close"] < kline[-5]["close"]:
            score -= 5
            rec.risks.append("放量下跌，资金流出")
        elif vol_ratio < 0.5:
            rec.risks.append("成交萎缩，关注度下降")

        # 9. 人气排名加分
        if stock.hot_rank > 0 and stock.hot_rank <= 10:
            score += 8
            rec.reasons.append(f"人气榜第{stock.hot_rank}名，关注度高")
        elif stock.hot_rank > 0 and stock.hot_rank <= 30:
            score += 5
            rec.reasons.append(f"人气榜第{stock.hot_rank}名")

        # 10. 板块排名加分
        if sector_rank <= 3:
            score += 5
            rec.reasons.append(f"所属板块排名第{sector_rank}")

        # ========== 确定买入策略和价格 ==========
        rec.score = max(0, min(100, score))

        # 设置买入价、止损价、目标价
        if is_strong_momentum:
            rec.strategy = "强势追涨"
            rec.buy_price = round(current_price * 1.01, 2)  # 可接受1%溢价买入
            rec.stop_loss = round(indicators.ma5 * 0.97, 2)  # 跌破5日线3%止损
            rec.target_price = round(current_price * 1.15, 2)  # 目标15%
        elif is_pullback_buy:
            rec.strategy = "回调低吸"
            rec.buy_price = round(indicators.ma10 * 0.99, 2)  # 回调到10日线附近买入
            rec.stop_loss = round(indicators.ma20 * 0.95, 2)  # 跌破20日线5%止损
            rec.target_price = round(current_price * 1.12, 2)  # 目标12%
        elif is_breakout:
            rec.strategy = "突破买入"
            rec.buy_price = round(current_price, 2)
            rec.stop_loss = round(indicators.ma10 * 0.97, 2)
            rec.target_price = round(current_price * 1.10, 2)
        else:
            rec.strategy = "观望"
            rec.buy_price = 0
            rec.stop_loss = 0
            rec.target_price = 0

        # ========== 生成最终信号 ==========
        # 可买入条件：分数>=55 且 有明确策略
        if rec.score >= 70 and rec.strategy != "观望":
            rec.signal = "强烈推荐"
            rec.buyable = True
        elif rec.score >= 60 and rec.strategy != "观望":
            rec.signal = "推荐买入"
            rec.buyable = True
        elif rec.score >= 55 and rec.strategy != "观望":
            rec.signal = "可以买入"
            rec.buyable = True
        elif rec.score >= 50:
            rec.signal = "中性观望"
            rec.buyable = False
        elif rec.score >= 40:
            rec.signal = "谨慎观望"
            rec.buyable = False
        else:
            rec.signal = "建议回避"
            rec.buyable = False

        return rec


# ============================================
# 主服务
# ============================================


class HotSectorService:
    """热门板块服务"""

    def __init__(self, use_cache: bool = True):
        self.fetcher = DataFetcher(use_cache=use_cache)
        self.analyzer = TechAnalyzer()
        self.recommender = RecommendationEngine()

    def get_hot_sectors(self, top_n: int = 10) -> List[HotSector]:
        """获取热门板块及龙头股"""
        print("\n" + "=" * 60)
        print("开始获取热门板块数据...")
        print("=" * 60)

        # 1. 获取概念板块行情
        concepts = self.fetcher.get_hot_concepts_by_popularity(top_n * 2)

        # 2. 获取资金流向
        fund_flows = self.fetcher.get_concept_fund_flow(top_n * 2)
        fund_flow_map = {f["name"]: f for f in fund_flows}

        # 3. 获取个股人气排名
        hot_rank_map = self.fetcher.get_stock_hot_rank()

        # 4. 综合排序（资金流入 + 涨跌幅 + 上涨家数）
        for c in concepts:
            flow = fund_flow_map.get(c["name"], {})
            c["main_net_inflow"] = flow.get("main_net_inflow", 0)
            # 综合热度分 = 资金流入权重0.4 + 涨幅权重0.3 + 上涨家数权重0.3
            c["heat_score"] = (
                c["main_net_inflow"] * 0.4
                + c["change_pct"] * 0.3
                + c["up_count"] * 0.01 * 0.3
            )

        # 按热度排序
        concepts.sort(key=lambda x: x["heat_score"], reverse=True)
        concepts = concepts[:top_n]

        # 5. 获取每个板块的龙头股
        hot_sectors = []

        for i, concept in enumerate(concepts):
            print(f"\n处理板块 [{i + 1}/{len(concepts)}]: {concept['name']}")

            sector = HotSector(
                name=concept["name"],
                code=concept.get("code", ""),
                hot_rank=i + 1,
                change_pct=concept["change_pct"],
                main_net_inflow=concept["main_net_inflow"],
                turnover_rate=concept.get("turnover_rate", 0),
                up_count=concept.get("up_count", 0),
                down_count=concept.get("down_count", 0),
            )

            # 获取成分股
            time.sleep(0.5)  # 避免请求过快
            constituents = self.fetcher.get_concept_constituents(concept["name"])

            if not constituents:
                hot_sectors.append(sector)
                continue

            # 为成分股添加人气排名
            for stock in constituents:
                stock["hot_rank"] = hot_rank_map.get(stock["code"], 9999)

            # 按人气排名排序，取龙一龙二
            constituents.sort(key=lambda x: x["hot_rank"])
            top_stocks = constituents[:2]

            # 分析龙头股
            for stock_data in top_stocks:
                print(f"  分析龙头: {stock_data['name']} ({stock_data['code']})")

                stock = StockInfo(
                    code=stock_data["code"],
                    name=stock_data["name"],
                    price=stock_data["price"],
                    change_pct=stock_data["change_pct"],
                    hot_rank=stock_data["hot_rank"],
                    turnover_rate=stock_data.get("turnover_rate", 0),
                )

                # 获取K线
                time.sleep(0.3)
                kline = self.fetcher.get_stock_kline(stock.code, days=60)

                # 技术分析
                indicators = self.analyzer.analyze(kline)

                # 生成建议
                recommendation = self.recommender.generate(
                    stock, indicators, sector.hot_rank, kline
                )

                # K线总结
                kline_summary = self._summarize_kline(kline, indicators)

                leader = LeaderStock(
                    stock=stock,
                    indicators=indicators,
                    recommendation=recommendation,
                    kline_summary=kline_summary,
                )

                sector.leaders.append(leader)

            hot_sectors.append(sector)

        return hot_sectors

    def get_multi_dimension_sectors(self, top_n: int = 3) -> Dict[str, List[HotSector]]:
        """多维度分析：热门概念 + 热门行业 + 涨幅领先"""
        print("\n" + "=" * 60)
        print("📊 A股板块多维度分析")
        print("=" * 60)

        # 获取人气排名（完整数据用于查找股票代码）
        hot_rank_full = self.fetcher.get_stock_hot_rank_full()
        hot_rank_map = {s["code"]: s["rank"] for s in hot_rank_full}
        # 创建名称到完整信息的映射（用于备用查找）
        self._hot_stock_by_name = {s["name"]: s for s in hot_rank_full}

        result = {
            "hot_concepts": [],
            "hot_industries": [],
            "top_gainers": [],
        }

        # 1. 热门概念板块（资金+人气）
        print("\n📊 获取热门概念板块...")
        concepts = self.fetcher.get_hot_concepts_by_popularity(top_n * 2)
        fund_flows = self.fetcher.get_concept_fund_flow(top_n * 2)
        fund_flow_map = {f["name"]: f for f in fund_flows}

        for c in concepts:
            flow = fund_flow_map.get(c["name"], {})
            c["main_net_inflow"] = flow.get("main_net_inflow", 0)
            c["heat_score"] = (
                c["main_net_inflow"] * 0.5
                + c["change_pct"] * 0.3
                + c["up_count"] * 0.02
            )

        concepts.sort(key=lambda x: x["heat_score"], reverse=True)

        for i, c in enumerate(concepts[:top_n]):
            sector = self._build_sector(
                c, i + 1, "concept", "🔥 热门概念", hot_rank_map
            )
            result["hot_concepts"].append(sector)

        # 2. 热门行业板块
        print("\n📊 获取热门行业板块...")
        industries = self.fetcher.get_hot_industry_sectors(top_n * 2)

        for i, ind in enumerate(industries[:top_n]):
            sector = self._build_industry_sector(ind, i + 1, hot_rank_map)
            result["hot_industries"].append(sector)

        # 3. 涨幅领先板块（去重）
        print("\n📊 获取涨幅领先板块...")
        existing_names = {
            s.name for s in result["hot_concepts"] + result["hot_industries"]
        }

        # 概念板块按涨幅排序
        all_concepts = self.fetcher.get_hot_concepts_by_popularity(50)
        all_concepts.sort(key=lambda x: x["change_pct"], reverse=True)

        gainer_count = 0
        for c in all_concepts:
            if c["name"] in existing_names:
                continue
            if gainer_count >= top_n:
                break
            c["category"] = "top_gainer"
            c["category_name"] = "📈 涨幅领先"
            sector = self._build_sector(
                c, gainer_count + 1, "top_gainer", "📈 涨幅领先", hot_rank_map
            )
            result["top_gainers"].append(sector)
            gainer_count += 1

        return result

    def _build_sector(
        self,
        data: Dict,
        rank: int,
        category: str,
        category_name: str,
        hot_rank_map: Dict,
    ) -> HotSector:
        """构建板块对象并获取龙头股"""
        sector = HotSector(
            name=data["name"],
            code=data.get("code", ""),
            hot_rank=rank,
            change_pct=data["change_pct"],
            main_net_inflow=data.get("main_net_inflow", 0),
            up_count=data.get("up_count", 0),
            down_count=data.get("down_count", 0),
            category=category,
            category_name=category_name,
        )

        # 获取成分股
        time.sleep(0.3)
        constituents = self.fetcher.get_concept_constituents(data["name"])

        # 如果成分股获取失败，使用资金流向中的领涨股作为备用
        if not constituents and data.get("leader_stock"):
            leader_name = data["leader_stock"].replace(" ", "")  # 去除空格
            # 尝试从人气榜查找股票代码
            stock_info = getattr(self, "_hot_stock_by_name", {}).get(leader_name, {})
            leader_code = stock_info.get("code", "")
            if is_valid_stock(leader_name, leader_code):
                constituents = [
                    {
                        "code": stock_info.get("code", ""),
                        "name": leader_name,
                        "price": stock_info.get("price", 0)
                        or data.get("leader_price", 0),
                        "change_pct": stock_info.get("change_pct", 0)
                        or data.get("leader_change_pct", 0),
                        "turnover_rate": 0,
                        "hot_rank": stock_info.get("rank", 9999),
                    }
                ]
                if stock_info.get("code"):
                    print(f"  [备用] 使用领涨股: {leader_name} ({stock_info['code']})")
                else:
                    print(f"  [备用] 使用领涨股: {leader_name} (无代码)")

        self._add_leaders_to_sector(sector, constituents, hot_rank_map)

        return sector

    def _build_industry_sector(
        self, data: Dict, rank: int, hot_rank_map: Dict
    ) -> HotSector:
        """构建行业板块对象"""
        sector = HotSector(
            name=data["name"],
            code=data.get("code", ""),
            hot_rank=rank,
            change_pct=data["change_pct"],
            main_net_inflow=data.get("main_net_inflow", 0),
            up_count=data.get("up_count", 0),
            down_count=data.get("down_count", 0),
            category="industry",
            category_name="🏭 热门行业",
        )

        # 获取行业成分股
        time.sleep(0.3)
        constituents = self.fetcher.get_industry_constituents(data["name"])
        self._add_leaders_to_sector(sector, constituents, hot_rank_map)

        return sector

    def _add_leaders_to_sector(
        self, sector: HotSector, constituents: List[Dict], hot_rank_map: Dict
    ):
        """为板块添加龙头股分析"""
        if not constituents:
            return

        # 过滤 ST 股票和门槛板块
        valid_stocks = [
            s
            for s in constituents
            if is_valid_stock(s.get("name", ""), s.get("code", ""))
        ]
        if not valid_stocks:
            return

        # 添加人气排名并排序
        for stock in valid_stocks:
            stock["hot_rank"] = hot_rank_map.get(stock["code"], 9999)
        valid_stocks.sort(key=lambda x: x["hot_rank"])

        # 取龙一龙二
        for stock_data in valid_stocks[:2]:
            stock = StockInfo(
                code=stock_data["code"],
                name=stock_data["name"],
                price=stock_data["price"],
                change_pct=stock_data["change_pct"],
                hot_rank=stock_data["hot_rank"],
                turnover_rate=stock_data.get("turnover_rate", 0),
            )

            # 获取K线和分析
            time.sleep(0.2)
            kline = self.fetcher.get_stock_kline(stock.code, days=60)
            indicators = self.analyzer.analyze(kline)
            recommendation = self.recommender.generate(
                stock, indicators, sector.hot_rank, kline
            )
            kline_summary = self._summarize_kline(kline, indicators)

            leader = LeaderStock(
                stock=stock,
                indicators=indicators,
                recommendation=recommendation,
                kline_summary=kline_summary,
            )
            sector.leaders.append(leader)

    def _summarize_kline(self, kline: List[Dict], indicators: TechIndicators) -> str:
        """总结K线形态"""
        if len(kline) < 5:
            return "数据不足"

        summaries = []

        # 近期趋势
        close_5d_ago = kline[-5]["close"]
        close_now = kline[-1]["close"]
        change_5d = (close_now - close_5d_ago) / close_5d_ago * 100

        if change_5d > 10:
            summaries.append("近5日强势上涨")
        elif change_5d > 3:
            summaries.append("近5日温和上涨")
        elif change_5d > -3:
            summaries.append("近5日横盘整理")
        elif change_5d > -10:
            summaries.append("近5日小幅回调")
        else:
            summaries.append("近5日大幅下跌")

        # 均线位置
        if indicators.ma5 > 0 and close_now > indicators.ma5:
            summaries.append("站上5日均线")
        elif indicators.ma5 > 0:
            summaries.append("跌破5日均线")

        # 量能
        if len(kline) >= 10:
            recent_vol = sum(k["volume"] for k in kline[-5:]) / 5
            prev_vol = sum(k["volume"] for k in kline[-10:-5]) / 5
            if prev_vol > 0:
                vol_ratio = recent_vol / prev_vol
                if vol_ratio > 1.5:
                    summaries.append("量能放大")
                elif vol_ratio < 0.6:
                    summaries.append("量能萎缩")

        return "，".join(summaries)

    def _collect_buyable_stocks(
        self, analysis: Dict[str, List[HotSector]]
    ) -> List[Dict]:
        """收集所有可买入的标的，按评分排序"""
        buyable = []

        all_sectors = (
            analysis.get("hot_concepts", [])
            + analysis.get("hot_industries", [])
            + analysis.get("top_gainers", [])
        )

        seen_codes = set()  # 去重
        for sector in all_sectors:
            for leader in sector.leaders:
                rec = leader.recommendation
                if rec.buyable and leader.stock.code not in seen_codes:
                    seen_codes.add(leader.stock.code)
                    buyable.append(
                        {
                            "stock": leader.stock,
                            "indicators": leader.indicators,
                            "recommendation": rec,
                            "sector_name": sector.name,
                            "sector_category": sector.category_name,
                        }
                    )

        # 按评分排序
        buyable.sort(key=lambda x: x["recommendation"].score, reverse=True)
        return buyable

    def generate_multi_report(self, analysis: Dict[str, List[HotSector]]) -> str:
        """生成多维度分析报告"""
        now = datetime.now()

        report = []
        report.append("=" * 60)
        report.append("📊 A股热门板块投资建议报告")
        report.append(f"📅 生成时间: {now.strftime('%Y-%m-%d %H:%M')}")
        report.append("=" * 60)

        # ========== 第一部分：精选买入标的 ==========
        buyable_stocks = self._collect_buyable_stocks(analysis)

        report.append("")
        report.append("🎯 【精选买入标的】")
        report.append("=" * 60)

        if buyable_stocks:
            for i, item in enumerate(buyable_stocks[:6], 1):  # 最多展示6个
                stock = item["stock"]
                rec = item["recommendation"]
                ind = item["indicators"]

                signal_emoji = {
                    "强烈推荐": "🟢🟢",
                    "推荐买入": "🟢",
                    "可以买入": "🟡",
                }.get(rec.signal, "⚪")

                report.append(f"\n{i}. {signal_emoji} {stock.name} ({stock.code})")
                report.append(
                    f"   所属板块: {item['sector_name']} ({item['sector_category']})"
                )
                report.append(
                    f"   当前价: ¥{stock.price:.2f} | 今日涨跌: {stock.change_pct:+.2f}%"
                )
                report.append(f"   评分: {rec.score}分 | 策略: {rec.strategy}")

                if rec.buy_price > 0:
                    report.append(f"   📍 买入价: ¥{rec.buy_price:.2f}")
                    report.append(
                        f"   🛑 止损价: ¥{rec.stop_loss:.2f} ({((rec.stop_loss - rec.buy_price) / rec.buy_price * 100):+.1f}%)"
                    )
                    report.append(
                        f"   🎯 目标价: ¥{rec.target_price:.2f} ({((rec.target_price - rec.buy_price) / rec.buy_price * 100):+.1f}%)"
                    )

                report.append(f"   📈 技术面: {ind.macd_signal} | {ind.ma_signal}")

                if rec.reasons:
                    report.append(f"   ✅ 买入理由: {'; '.join(rec.reasons[:2])}")
                if rec.risks:
                    report.append(f"   ⚠️ 风险提示: {'; '.join(rec.risks[:2])}")
        else:
            report.append("\n   暂无符合条件的买入标的")
            report.append("   (当前市场可能处于调整期，建议观望)")

        # ========== 第二部分：板块分析详情 ==========
        report.append("")
        report.append("")
        report.append("📋 【板块详细分析】")
        report.append("=" * 60)

        sections = [
            ("🔥 一、热门概念板块（资金+人气）", analysis.get("hot_concepts", [])),
            ("🏭 二、热门行业板块（行业轮动）", analysis.get("hot_industries", [])),
            ("📈 三、涨幅领先板块（强势板块）", analysis.get("top_gainers", [])),
        ]

        rank = 1
        for section_title, sectors in sections:
            report.append(f"\n{section_title}")
            report.append("-" * 40)

            for sector in sectors:
                report.append(f"\n【{rank}】{sector.name} ({sector.category_name})")
                report.append(f"   今日涨跌: {sector.change_pct:+.2f}%")

                if sector.main_net_inflow != 0:
                    report.append(f"   主力净流入: {sector.main_net_inflow:.2f}亿")

                if sector.up_count or sector.down_count:
                    report.append(
                        f"   上涨: {sector.up_count}家 | 下跌: {sector.down_count}家"
                    )

                # 龙头股
                if sector.leaders:
                    report.append("")
                    for i, leader in enumerate(sector.leaders[:2]):
                        rank_name = "龙一" if i == 0 else "龙二"
                        stock = leader.stock
                        ind = leader.indicators
                        rec = leader.recommendation

                        # 可买入标记
                        buyable_mark = "【可买】" if rec.buyable else ""

                        report.append(
                            f"   {rank_name}: {stock.name} ({stock.code}) {buyable_mark}"
                        )
                        report.append(
                            f"   当前价: ¥{stock.price:.2f} | 涨跌: {stock.change_pct:+.2f}%"
                        )

                        if stock.hot_rank < 9999:
                            report.append(f"   🏆 人气排名: 第{stock.hot_rank}名")

                        report.append(
                            f"   📈 MACD: {ind.macd_signal} | KDJ: {ind.kdj_signal}"
                        )
                        report.append(
                            f"   📊 RSI: {ind.rsi_signal} | 均线: {ind.ma_signal}"
                        )

                        signal_emoji = {
                            "强烈推荐": "🟢🟢",
                            "推荐买入": "🟢",
                            "可以买入": "🟡",
                            "中性观望": "⚪",
                            "谨慎观望": "🟠",
                            "建议回避": "🔴",
                        }.get(rec.signal, "⚪")

                        report.append(
                            f"   💡 建议: {signal_emoji} {rec.signal} (评分: {rec.score}) | 策略: {rec.strategy}"
                        )

                        if rec.buyable and rec.buy_price > 0:
                            report.append(
                                f"   📍 买入¥{rec.buy_price:.2f} | 止损¥{rec.stop_loss:.2f} | 目标¥{rec.target_price:.2f}"
                            )

                        if rec.reasons:
                            report.append(f"   ✅ 优势: {'; '.join(rec.reasons[:2])}")
                        if rec.risks:
                            report.append(f"   ⚠️ 风险: {'; '.join(rec.risks[:2])}")

                        report.append("")

                rank += 1

            report.append("")

        # ========== 第三部分：总结 ==========
        report.append("=" * 60)
        report.append("📌 操作建议汇总:")

        if buyable_stocks:
            report.append(f"   今日发现 {len(buyable_stocks)} 个可买入标的:")
            for item in buyable_stocks[:3]:
                stock = item["stock"]
                rec = item["recommendation"]
                report.append(
                    f"   • {stock.name}({stock.code}): {rec.strategy}，买入价¥{rec.buy_price:.2f}"
                )
        else:
            report.append("   今日暂无推荐买入标的，建议观望")

        report.append("")
        report.append("⚠️ 免责声明: 以上分析仅供参考，不构成投资建议。")
        report.append("   投资有风险，入市需谨慎。请根据自身情况做出决策。")

        return "\n".join(report)

    def generate_report(self, sectors: List[HotSector]) -> str:
        """生成推送报告（单一维度，兼容旧版本）"""
        now = datetime.now()

        report = []
        report.append("=" * 50)
        report.append(f"📊 A股热门板块龙头分析报告")
        report.append(f"📅 {now.strftime('%Y-%m-%d %H:%M')}")
        report.append("=" * 50)
        report.append("")

        for sector in sectors:
            report.append(f"🔥 【{sector.hot_rank}】{sector.name}")
            report.append(
                f"   涨跌: {sector.change_pct:+.2f}% | 主力净流入: {sector.main_net_inflow:.2f}亿"
            )
            report.append(f"   上涨: {sector.up_count}家 | 下跌: {sector.down_count}家")
            report.append("")

            for i, leader in enumerate(sector.leaders):
                rank_name = "龙一" if i == 0 else "龙二"
                stock = leader.stock
                ind = leader.indicators
                rec = leader.recommendation

                report.append(f"   {rank_name}: {stock.name} ({stock.code})")
                report.append(
                    f"   价格: ¥{stock.price:.2f} | 涨跌: {stock.change_pct:+.2f}%"
                )

                if stock.hot_rank < 9999:
                    report.append(f"   🏆 人气排名: 第{stock.hot_rank}名")

                report.append(f"   📈 技术指标:")
                report.append(f"      MACD: {ind.macd_signal}")
                report.append(
                    f"      KDJ: K={ind.kdj_k:.1f} D={ind.kdj_d:.1f} J={ind.kdj_j:.1f} ({ind.kdj_signal})"
                )
                report.append(
                    f"      RSI: {ind.rsi_6:.1f}/{ind.rsi_12:.1f} ({ind.rsi_signal})"
                )
                report.append(f"      均线: {ind.ma_signal}")
                report.append(f"      布林: {ind.boll_signal}")

                report.append(f"   📊 K线: {leader.kline_summary}")

                signal_emoji = {
                    "强烈推荐": "🟢🟢",
                    "推荐关注": "🟢",
                    "中性观望": "🟡",
                    "谨慎观望": "🟠",
                    "建议回避": "🔴",
                }.get(rec.signal, "⚪")

                report.append(
                    f"   💡 建议: {signal_emoji} {rec.signal} (评分: {rec.score})"
                )

                if rec.reasons:
                    report.append(f"   ✅ 优势: {'; '.join(rec.reasons[:3])}")
                if rec.risks:
                    report.append(f"   ⚠️ 风险: {'; '.join(rec.risks[:3])}")

                report.append("")

            report.append("-" * 50)
            report.append("")

        report.append("⚠️ 免责声明: 以上分析仅供参考，不构成投资建议。")
        report.append("   投资有风险，入市需谨慎。")

        return "\n".join(report)

    def generate_json_report(self, sectors: List[HotSector]) -> Dict:
        """生成JSON格式报告（用于API推送）"""
        # 收集可买入标的
        buyable_stocks = []
        seen_codes = set()

        for s in sectors:
            for l in s.leaders:
                if l.recommendation.buyable and l.stock.code not in seen_codes:
                    seen_codes.add(l.stock.code)
                    buyable_stocks.append(
                        {
                            "code": l.stock.code,
                            "name": l.stock.name,
                            "price": l.stock.price,
                            "change_pct": l.stock.change_pct,
                            "hot_rank": l.stock.hot_rank,
                            "sector": s.name,
                            "sector_category": s.category_name,
                            "score": l.recommendation.score,
                            "signal": l.recommendation.signal,
                            "strategy": l.recommendation.strategy,
                            "buy_price": l.recommendation.buy_price,
                            "stop_loss": l.recommendation.stop_loss,
                            "target_price": l.recommendation.target_price,
                            "reasons": l.recommendation.reasons,
                            "risks": l.recommendation.risks,
                        }
                    )

        # 按评分排序
        buyable_stocks.sort(key=lambda x: x["score"], reverse=True)

        return {
            "type": "hot_sector_analysis",
            "timestamp": datetime.now().isoformat(),
            "buyable_count": len(buyable_stocks),
            "buyable_stocks": buyable_stocks,  # 精选买入标的（置顶）
            "sectors": [
                {
                    "name": s.name,
                    "rank": s.hot_rank,
                    "change_pct": s.change_pct,
                    "main_net_inflow": s.main_net_inflow,
                    "category": s.category,
                    "category_name": s.category_name,
                    "up_count": s.up_count,
                    "down_count": s.down_count,
                    "leaders": [
                        {
                            "code": l.stock.code,
                            "name": l.stock.name,
                            "price": l.stock.price,
                            "change_pct": l.stock.change_pct,
                            "hot_rank": l.stock.hot_rank,
                            "recommendation": {
                                "score": l.recommendation.score,
                                "signal": l.recommendation.signal,
                                "buyable": l.recommendation.buyable,
                                "strategy": l.recommendation.strategy,
                                "buy_price": l.recommendation.buy_price,
                                "stop_loss": l.recommendation.stop_loss,
                                "target_price": l.recommendation.target_price,
                                "reasons": l.recommendation.reasons,
                                "risks": l.recommendation.risks,
                            },
                            "indicators": asdict(l.indicators),
                            "kline_summary": l.kline_summary,
                        }
                        for l in s.leaders
                    ],
                }
                for s in sectors
            ],
        }


# ============================================
# 推送渠道
# ============================================


class PushChannel:
    """推送渠道基类"""

    def push(self, content: str, json_data: Dict = None) -> bool:
        raise NotImplementedError


class WebhookPush(PushChannel):
    """Webhook 推送（支持企业微信、飞书、Discord）"""

    def __init__(self, webhook_url: str, channel_type: str = "wechat"):
        self.webhook_url = webhook_url
        self.channel_type = channel_type

    def push(self, content: str, json_data: Dict = None) -> bool:
        try:
            if self.channel_type == "wechat":
                payload = {"msgtype": "text", "text": {"content": content}}
            elif self.channel_type == "feishu":
                payload = {"msg_type": "text", "content": {"text": content}}
            elif self.channel_type == "discord":
                # Discord 有2000字符限制，需要分段
                payload = {"content": content[:2000]}
            else:
                payload = {"text": content}

            resp = requests.post(self.webhook_url, json=payload, timeout=10)
            return resp.status_code == 200
        except Exception as e:
            print(f"[ERR] Webhook推送失败: {e}")
            return False


class TelegramPush(PushChannel):
    """Telegram 推送"""

    def __init__(self, bot_token: str, chat_id: str):
        self.bot_token = bot_token
        self.chat_id = chat_id

    def push(self, content: str, json_data: Dict = None) -> bool:
        try:
            url = f"https://api.telegram.org/bot{self.bot_token}/sendMessage"
            # Telegram 有4096字符限制
            payload = {
                "chat_id": self.chat_id,
                "text": content[:4000],
                "parse_mode": "HTML",
            }
            resp = requests.post(url, json=payload, timeout=10)
            return resp.status_code == 200
        except Exception as e:
            print(f"[ERR] Telegram推送失败: {e}")
            return False


class FilePush(PushChannel):
    """文件输出（用于调试）"""

    def __init__(self, output_dir: str = "data"):
        self.output_dir = output_dir
        os.makedirs(output_dir, exist_ok=True)

    def push(self, content: str, json_data: Dict = None) -> bool:
        try:
            timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")

            # 保存文本报告
            txt_path = os.path.join(self.output_dir, f"hot_sector_{timestamp}.txt")
            with open(txt_path, "w", encoding="utf-8") as f:
                f.write(content)
            print(f"[OK] 报告已保存: {txt_path}")

            # 保存JSON数据
            if json_data:
                json_path = os.path.join(
                    self.output_dir, f"hot_sector_{timestamp}.json"
                )
                with open(json_path, "w", encoding="utf-8") as f:
                    json.dump(json_data, f, ensure_ascii=False, indent=2)
                print(f"[OK] JSON已保存: {json_path}")

            return True
        except Exception as e:
            print(f"[ERR] 文件保存失败: {e}")
            return False


# ============================================
# 主程序
# ============================================


def load_config():
    """从 config.yaml 加载推送配置"""
    import yaml

    config_path = os.environ.get("NEWS_CONFIG", "config.yaml")
    if not os.path.exists(config_path):
        return {}

    with open(config_path, "r", encoding="utf-8") as f:
        return yaml.safe_load(f) or {}


def show_cache_status():
    """显示缓存状态"""
    cache = get_cache_manager()
    circuit = get_circuit_breaker()

    print("\n" + "=" * 60)
    print("📊 缓存和数据源状态")
    print("=" * 60)

    # 缓存数据库大小
    if os.path.exists(cache.db_path):
        size_mb = os.path.getsize(cache.db_path) / 1024 / 1024
        print(f"\n📁 缓存数据库: {cache.db_path}")
        print(f"   大小: {size_mb:.2f} MB")

    # 统计缓存数据
    with sqlite3.connect(cache.db_path) as conn:
        kline_count = conn.execute(
            "SELECT COUNT(DISTINCT stock_code) FROM kline_cache"
        ).fetchone()[0]
        kline_days = conn.execute("SELECT COUNT(*) FROM kline_cache").fetchone()[0]
        sector_count = conn.execute("SELECT COUNT(*) FROM sector_cache").fetchone()[0]
        hot_rank_count = conn.execute("SELECT COUNT(*) FROM hot_rank_cache").fetchone()[
            0
        ]

    print(f"\n📈 K线缓存: {kline_count} 只股票, {kline_days} 条数据")
    print(f"🔥 板块缓存: {sector_count} 条")
    print(f"🏆 人气排名缓存: {hot_rank_count} 条")

    # 熔断器状态
    print("\n⚡ 数据源熔断状态:")
    status = circuit.get_status()
    if status:
        for source, state in status.items():
            print(f"   {source}: {state}")
    else:
        print("   所有数据源正常")

    print()


def clear_cache(cache_type: str = "all"):
    """清理缓存"""
    cache = get_cache_manager()

    print("\n" + "=" * 60)
    print("🗑️ 清理缓存")
    print("=" * 60)

    with sqlite3.connect(cache.db_path) as conn:
        if cache_type in ["all", "kline"]:
            conn.execute("DELETE FROM kline_cache")
            print("✅ K线缓存已清理")

        if cache_type in ["all", "sector"]:
            conn.execute("DELETE FROM sector_cache")
            print("✅ 板块缓存已清理")

        if cache_type in ["all", "hot_rank"]:
            conn.execute("DELETE FROM hot_rank_cache")
            print("✅ 人气排名缓存已清理")

        conn.commit()

    # 压缩数据库
    with sqlite3.connect(cache.db_path) as conn:
        conn.execute("VACUUM")

    print("\n✅ 缓存清理完成!")


def warmup_cache(stock_codes: List[str] = None):
    """预热缓存 - 提前获取常用股票的K线数据"""
    print("\n" + "=" * 60)
    print("🔥 缓存预热")
    print("=" * 60)

    fetcher = DataFetcher(use_cache=True)
    cache = get_cache_manager()

    # 如果没有指定股票，获取热门股票列表
    if not stock_codes:
        print("\n📊 获取热门股票列表...")
        hot_stocks = fetcher.get_stock_hot_rank_full(top_n=50)
        stock_codes = [s["code"] for s in hot_stocks if s.get("code")]
        print(f"   找到 {len(stock_codes)} 只热门股票")

    if not stock_codes:
        print("[WARN] 未找到股票列表")
        return

    # 预热K线数据
    print(f"\n📈 预热 {len(stock_codes)} 只股票的K线数据...")
    success_count = 0
    skip_count = 0

    for i, code in enumerate(stock_codes):
        # 检查是否已有缓存
        cached = cache.get_cached_kline(code, 60)
        if cached and len(cached) >= 50:
            skip_count += 1
            continue

        # 获取K线数据（会自动缓存）
        kline = fetcher.get_stock_kline(code, days=60)
        if kline:
            success_count += 1

        # 显示进度
        if (i + 1) % 10 == 0:
            print(
                f"   进度: {i + 1}/{len(stock_codes)} (成功: {success_count}, 跳过: {skip_count})"
            )

        # 控制请求频率
        time.sleep(0.2)

    print(f"\n✅ 缓存预热完成!")
    print(f"   新增: {success_count} 只股票")
    print(f"   跳过(已缓存): {skip_count} 只股票")


def cleanup_old_cache(days: int = 30):
    """清理过期缓存数据"""
    print("\n" + "=" * 60)
    print("🧹 清理过期缓存")
    print("=" * 60)

    cache = get_cache_manager()
    cutoff = (datetime.now() - timedelta(days=days)).strftime("%Y-%m-%d")

    with sqlite3.connect(cache.db_path) as conn:
        # 清理旧K线数据
        cursor = conn.execute(
            "SELECT COUNT(*) FROM kline_cache WHERE trade_date < ?", (cutoff,)
        )
        old_kline_count = cursor.fetchone()[0]
        conn.execute("DELETE FROM kline_cache WHERE trade_date < ?", (cutoff,))

        # 清理过期板块缓存
        cursor = conn.execute(
            "SELECT COUNT(*) FROM sector_cache WHERE created_at < ?", (cutoff,)
        )
        old_sector_count = cursor.fetchone()[0]
        conn.execute("DELETE FROM sector_cache WHERE created_at < ?", (cutoff,))

        # 清理过期人气排名
        cursor = conn.execute(
            "SELECT COUNT(*) FROM hot_rank_cache WHERE updated_at < ?", (cutoff,)
        )
        old_rank_count = cursor.fetchone()[0]
        conn.execute("DELETE FROM hot_rank_cache WHERE updated_at < ?", (cutoff,))

        conn.commit()
        conn.execute("VACUUM")

    print(f"   K线数据: 删除 {old_kline_count} 条")
    print(f"   板块缓存: 删除 {old_sector_count} 条")
    print(f"   人气排名: 删除 {old_rank_count} 条")
    print("\n✅ 清理完成!")


def main():
    """主函数"""
    import argparse

    parser = argparse.ArgumentParser(description="A股热门板块龙头分析系统 (优化版)")
    parser.add_argument(
        "--mode",
        choices=["single", "multi"],
        default="multi",
        help="分析模式: single=单一维度, multi=多维度(默认)",
    )
    parser.add_argument(
        "--top", type=int, default=3, help="每个维度展示的板块数量(默认3)"
    )
    parser.add_argument("--status", action="store_true", help="显示缓存和数据源状态")
    parser.add_argument(
        "--clear-cache",
        choices=["all", "kline", "sector", "hot_rank"],
        help="清理缓存: all=全部, kline=K线, sector=板块, hot_rank=人气排名",
    )
    parser.add_argument(
        "--warmup", action="store_true", help="预热缓存: 提前获取热门股票K线数据"
    )
    parser.add_argument(
        "--cleanup", type=int, metavar="DAYS", help="清理N天前的过期缓存数据"
    )
    parser.add_argument(
        "--no-cache", action="store_true", help="禁用缓存，强制从API获取数据"
    )
    parser.add_argument(
        "--json-only", action="store_true", help="只输出JSON格式(用于Go集成)"
    )
    parser.add_argument("--quiet", "-q", action="store_true", help="静默模式，减少输出")
    args = parser.parse_args()

    # 处理工具命令
    if args.status:
        show_cache_status()
        return

    if args.clear_cache:
        clear_cache(args.clear_cache)
        return

    if args.warmup:
        warmup_cache()
        return

    if args.cleanup:
        cleanup_old_cache(args.cleanup)
        return

    # 静默模式下减少输出
    if not args.quiet and not args.json_only:
        print("\n" + "=" * 60)
        print("🚀 A股热门板块龙头分析系统 (优化版)")
        print("=" * 60)

    # 重定向标准输出（静默模式）
    original_stdout = sys.stdout
    if args.quiet:
        sys.stdout = open(os.devnull, "w")

    # 初始化服务
    service = HotSectorService(use_cache=not args.no_cache)

    if args.mode == "multi":
        # 多维度分析（热门概念 + 热门行业 + 涨幅领先）
        analysis = service.get_multi_dimension_sectors(top_n=args.top)

        all_sectors = (
            analysis.get("hot_concepts", [])
            + analysis.get("hot_industries", [])
            + analysis.get("top_gainers", [])
        )

        if not all_sectors:
            if args.quiet:
                sys.stdout = original_stdout
            print("[ERR] 未获取到热门板块数据", file=sys.stderr)
            sys.exit(1)

        # 生成多维度报告
        text_report = service.generate_multi_report(analysis)
        json_report = service.generate_json_report(all_sectors)
    else:
        # 单一维度分析（兼容旧版本）
        sectors = service.get_hot_sectors(top_n=args.top * 3)

        if not sectors:
            if args.quiet:
                sys.stdout = original_stdout
            print("[ERR] 未获取到热门板块数据", file=sys.stderr)
            sys.exit(1)

        text_report = service.generate_report(sectors)
        json_report = service.generate_json_report(sectors)

    # 恢复标准输出
    if args.quiet:
        sys.stdout = original_stdout

    # JSON 模式：只输出 JSON 到标准输出（用于 Go 集成）
    if args.json_only:
        print(json.dumps(json_report, ensure_ascii=False, indent=2))
        return

    # 打印报告
    if not args.quiet:
        print("\n" + text_report)

    # 保存到文件
    file_push = FilePush(output_dir="data/reports")
    file_push.push(text_report, json_report)

    # 加载配置并推送
    config = load_config()
    channels = config.get("channels", [])

    for ch in channels:
        if not ch.get("enabled", False):
            continue

        ch_type = ch.get("type")
        ch_name = ch.get("name")

        if not args.quiet:
            print(f"\n推送到: {ch_name} ({ch_type})")

        if ch_type == "wechat" and ch.get("webhook"):
            push = WebhookPush(ch["webhook"], "wechat")
            push.push(text_report)

        elif ch_type == "feishu" and ch.get("webhook"):
            push = WebhookPush(ch["webhook"], "feishu")
            push.push(text_report)

        elif ch_type == "telegram":
            opts = ch.get("options", {})
            if opts.get("bot_token") and opts.get("chat_id"):
                push = TelegramPush(opts["bot_token"], opts["chat_id"])
                push.push(text_report)

        elif ch_type == "discord" and ch.get("webhook"):
            push = WebhookPush(ch["webhook"], "discord")
            push.push(text_report)

    if not args.quiet:
        print("\n✅ 热门板块分析完成!")


if __name__ == "__main__":
    main()
