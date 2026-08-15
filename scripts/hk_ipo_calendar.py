#!/usr/bin/env python3
"""
港股IPO日历
推送内容：
- 今日可申购：今天可以申购的港股IPO
- 近期可申购：未来N天可申购的港股IPO
- 申购建议：基于评分模型的资金分配建议

数据源：
1. Google News 实时搜索
2. 富途网页数据
3. 模拟数据（备用）
"""

import argparse
import json
import sqlite3
import sys
import re
from dataclasses import dataclass, asdict
from datetime import datetime, timedelta
from typing import Dict, List, Optional
from pathlib import Path


@dataclass
class HKIPOStock:
    """港股IPO信息"""
    code: str                    # 股票代码 (如 09988)
    name: str                    # 公司名称
    apply_start: str             # 申购开始日期
    apply_end: str               # 申购截止日期
    list_date: str               # 上市日期
    dark_date: str               # 暗盘日期
    unfreeze_date: str           # 解冻日
    price: float                 # 发行价(港元)
    market_cap: float            # 市值(亿港元)
    pe_ratio: float              # 市盈率
    industry: str                # 行业
    sponsor: str                 # 保荐人
    cornerstone_investors: str   # 基石投资者
    oversubscription: float       # 超额认购倍数
    win_rate: float              # 一手中签率(%)
    lot_size: int                # 每手股数


class CacheManager:
    """缓存管理器"""

    def __init__(self, cache_dir: str = "data/cache"):
        self.cache_dir = Path(cache_dir)
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        self.db_path = self.cache_dir / "hk_ipo_cache.db"
        self._init_db()

    def _init_db(self):
        with sqlite3.connect(self.db_path) as conn:
            # IPO基础信息表
            conn.execute("""
                CREATE TABLE IF NOT EXISTS hk_ipo_stocks (
                    code TEXT PRIMARY KEY,
                    name TEXT,
                    apply_start TEXT,
                    apply_end TEXT,
                    list_date TEXT,
                    dark_date TEXT,
                    unfreeze_date TEXT,
                    price REAL,
                    market_cap REAL,
                    pe_ratio REAL,
                    industry TEXT,
                    sponsor TEXT,
                    cornerstone_investors TEXT,
                    oversubscription REAL,
                    win_rate REAL,
                    lot_size INTEGER,
                    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                )
            """)

            # 历史IPO数据表（用于分析）
            conn.execute("""
                CREATE TABLE IF NOT EXISTS hk_ipo_history (
                    code TEXT PRIMARY KEY,
                    name TEXT,
                    list_date TEXT,
                    price REAL,
                    close_price REAL,
                    change_pct REAL,
                    industry TEXT,
                    sponsor TEXT,
                    oversubscription REAL,
                    win_rate REAL
                )
            """)

            # 保荐人历史表现表
            conn.execute("""
                CREATE TABLE IF NOT EXISTS hk_sponsor_performance (
                    sponsor TEXT PRIMARY KEY,
                    total_count INTEGER,
                    avg_first_day_pct REAL,
                    win_count INTEGER,
                    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                )
            """)

            # 推送历史表
            conn.execute("""
                CREATE TABLE IF NOT EXISTS push_history (
                    id INTEGER PRIMARY KEY,
                    push_type TEXT,
                    push_date TEXT,
                    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                )
            """)

            # 资金分配记录表
            conn.execute("""
                CREATE TABLE IF NOT EXISTS hk_ipo_allocation (
                    id INTEGER PRIMARY KEY,
                    code TEXT,
                    name TEXT,
                    score REAL,
                    allocated_amount REAL,
                    allocation_date TEXT,
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

    def save_ipo_data(self, stocks: List[HKIPOStock]):
        with sqlite3.connect(self.db_path) as conn:
            for stock in stocks:
                conn.execute("""
                    INSERT OR REPLACE INTO hk_ipo_stocks
                    (code, name, apply_start, apply_end, list_date, dark_date,
                     unfreeze_date, price, market_cap, pe_ratio, industry,
                     sponsor, cornerstone_investors, oversubscription, win_rate, lot_size)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """, (stock.code, stock.name, stock.apply_start, stock.apply_end,
                      stock.list_date, stock.dark_date, stock.unfreeze_date,
                      stock.price, stock.market_cap, stock.pe_ratio, stock.industry,
                      stock.sponsor, stock.cornerstone_investors, stock.oversubscription,
                      stock.win_rate, stock.lot_size))

    def get_sponsor_performance(self) -> Dict[str, Dict]:
        """获取保荐人历史表现"""
        result = {}
        with sqlite3.connect(self.db_path) as conn:
            cur = conn.execute("SELECT * FROM hk_sponsor_performance")
            for row in cur.fetchall():
                result[row[0]] = {
                    'sponsor': row[0],
                    'total_count': row[1],
                    'avg_first_day_pct': row[2],
                    'win_count': row[3]
                }
        return result

    def get_industry_avg_change(self, industry: str) -> Optional[float]:
        """获取行业平均首日涨跌幅"""
        with sqlite3.connect(self.db_path) as conn:
            cur = conn.execute(
                "SELECT AVG(change_pct) FROM hk_ipo_history WHERE industry=?",
                (industry,)
            )
            row = cur.fetchone()
            return row[0] if row and row[0] else None


class HKIPOCalendarFetcher:
    """港股IPO日历数据获取器"""

    def __init__(self, use_cache: bool = True, quiet: bool = False,
                 lookahead_days: int = 14):
        self.quiet = quiet
        self.cache = CacheManager() if use_cache else None
        self.lookahead_days = lookahead_days

        try:
            import akshare as ak
            self.ak = ak
        except ImportError:
            print("[ERROR] AKShare 未安装", file=sys.stderr)
            sys.exit(1)

    def _log(self, msg: str):
        if not self.quiet:
            print(msg, file=sys.stderr)

    def _safe_float(self, val, default: float = 0.0) -> float:
        if val is None:
            return default
        try:
            import math
            if isinstance(val, str):
                val = val.replace('%', '').replace(',', '').strip()
                if val == '' or val == '--' or val.lower() in ('nan', '-', 'N/A'):
                    return default
            result = float(val)
            return default if math.isnan(result) else result
        except (ValueError, TypeError):
            return default

    def _safe_int(self, val, default: int = 0) -> int:
        if val is None:
            return default
        try:
            if isinstance(val, str):
                val = val.replace(',', '').strip()
                if val == '' or val == '--' or val.lower() in ('nan', '-', 'N/A'):
                    return default
            return int(float(val))
        except (ValueError, TypeError):
            return default

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

    def _parse_hk_code(self, code: str) -> str:
        """解析港股代码"""
        code = str(code).strip()
        # 移除空格和特殊字符
        code = re.sub(r'\s+', '', code)
        # 港股代码通常是5位数字
        if len(code) == 5:
            return code
        elif len(code) == 6:
            # 可能是以0开头的6位代码
            if code.startswith('0'):
                return code[1:]  # 0开头的取后5位
            return code
        return code

    def _fetch_from_web(self) -> List[HKIPOStock]:
        """从网页抓取港股IPO数据"""
        stocks = []

        try:
            import requests
            from bs4 import BeautifulSoup

            headers = {
                'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36'
            }

            # 方法1: 从东方财富港股频道抓取
            url = "https://quote.eastmoney.com/hk/"
            resp = requests.get(url, headers=headers, timeout=15)
            if resp.status_code == 200:
                self._log("[INFO] 从东方财富获取港股数据")

            # 方法2: 从富途牛牛获取
            url2 = "https://www.futunn.com/hk/ipo"
            resp2 = requests.get(url2, headers=headers, timeout=15)
            if resp2.status_code == 200:
                soup = BeautifulSoup(resp2.text, 'html.parser')
                # 查找页面中的IPO数据
                # 尝试从script中提取数据
                scripts = soup.find_all('script')
                for script in scripts:
                    if script.string:
                        # 查找IPO相关数据
                        if 'IPO' in script.string or 'ipo' in script.string:
                            # 尝试提取JSON数据
                            import json
                            import re
                            matches = re.findall(r'\{[^{}]*?(?:code|name|price)[^{}]*?\}', script.string)
                            for match in matches:
                                try:
                                    data = json.loads(match)
                                    if 'code' in data and 'name' in data:
                                        self._log(f"[INFO] Found IPO data: {data}")
                                except:
                                    pass

        except ImportError:
            self._log("[WARN] requests或beautifulsoup4未安装，使用模拟数据")
        except Exception as e:
            self._log(f"[WARN] 网页抓取失败: {e}")

        return stocks

    def _fetch_from_futu(self) -> List[HKIPOStock]:
        """从富途网页获取港股IPO数据"""
        stocks = []

        try:
            import requests

            headers = {
                'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36'
            }

            url = "https://www.futunn.com/quote/hk/ipo"
            resp = requests.get(url, headers=headers, timeout=15)

            if resp.status_code != 200:
                return stocks

            # 提取已完成上市的IPO数据
            idx = resp.text.find('"ipo_finished_list"')
            if idx != -1:
                region = resp.text[idx:idx+80000]

                codes = re.findall(r'"stockCode":"([^"]+)"', region)
                names = re.findall(r'"name":"([^"]+)"', region)
                prices = re.findall(r'"ipoPrice":"([^"]+)"', region)
                first_day = re.findall(r'"firstDayPcr":"([^"]+)"', region)
                listing_dates = re.findall(r'"listingDate":(\d+)', region)
                industries = re.findall(r'"industry":"([^"]+)"', region)
                market_vals = re.findall(r'"marketVal":"([^"]+)"', region)

                self._log(f"[INFO] 从富途获取到 {len(codes)} 只已完成上市的IPO")

                for i in range(len(codes)):
                    if i >= 20:  # 限制数量
                        break

                    code = codes[i]
                    name = names[i] if i < len(names) else ""
                    price_str = prices[i] if i < len(prices) else "0"

                    try:
                        price = float(price_str) if price_str != '--' else 0.0
                    except:
                        price = 0.0

                    # 转换日期
                    list_date = ""
                    if i < len(listing_dates):
                        try:
                            ts = int(listing_dates[i])
                            list_date = datetime.fromtimestamp(ts).strftime('%Y-%m-%d')
                        except:
                            pass

                    industry = industries[i] if i < len(industries) else ""
                    market_cap_str = market_vals[i] if i < len(market_vals) else "0"
                    # 处理市值（亿/万）
                    market_cap = 0.0
                    if '亿' in market_cap_str:
                        try:
                            market_cap = float(market_cap_str.replace('亿', '')) * 100000000
                        except:
                            pass
                    elif '万' in market_cap_str:
                        try:
                            market_cap = float(market_cap_str.replace('万', '')) * 10000
                        except:
                            pass

                    stock = HKIPOStock(
                        code=code,
                        name=name,
                        apply_start="",  # 已完成上市，无申购信息
                        apply_end="",
                        list_date=list_date,
                        dark_date="",
                        unfreeze_date="",
                        price=price,
                        market_cap=market_cap,
                        pe_ratio=0,
                        industry=industry,
                        sponsor="",
                        cornerstone_investors="",
                        oversubscription=0,
                        win_rate=0,
                        lot_size=1000
                    )
                    stocks.append(stock)

            # 检查正在申购的IPO
            applying_idx = resp.text.find('"ipo_applying_list"')
            if applying_idx != -1:
                applying_region = resp.text[applying_idx:applying_idx+10000]
                applying_codes = re.findall(r'"stockCode":"([^"]+)"', applying_region)
                applying_names = re.findall(r'"name":"([^"]+)"', applying_region)
                applying_prices = re.findall(r'"ipoPrice":"([^"]+)"', applying_region)

                if applying_codes:
                    self._log(f"[INFO] 从富途获取到 {len(applying_codes)} 只正在申购的IPO")

                    for i in range(len(applying_codes)):
                        code = applying_codes[i]
                        name = applying_names[i] if i < len(applying_names) else ""
                        price_str = applying_prices[i] if i < len(applying_prices) else "0"

                        try:
                            price = float(price_str) if price_str != '--' else 0.0
                        except:
                            price = 0.0

                        today = datetime.now()
                        apply_start = today.strftime('%Y-%m-%d')
                        apply_end = (today + timedelta(days=4)).strftime('%Y-%m-%d')
                        list_date = (today + timedelta(days=7)).strftime('%Y-%m-%d')
                        dark_date = (today + timedelta(days=6)).strftime('%Y-%m-%d')
                        unfreeze_date = (today + timedelta(days=5)).strftime('%Y-%m-%d')

                        stock = HKIPOStock(
                            code=code,
                            name=name,
                            apply_start=apply_start,
                            apply_end=apply_end,
                            list_date=list_date,
                            dark_date=dark_date,
                            unfreeze_date=unfreeze_date,
                            price=price,
                            market_cap=0,
                            pe_ratio=0,
                            industry="",
                            sponsor="",
                            cornerstone_investors="",
                            oversubscription=0,
                            win_rate=0,
                            lot_size=1000
                        )
                        stocks.insert(0, stock)  # 插入到开头

        except ImportError:
            self._log("[WARN] requests未安装")
        except Exception as e:
            self._log(f"[WARN] 富途数据获取失败: {e}")

        return stocks

    def _fetch_from_google_news(self) -> List[HKIPOStock]:
        """从Google News获取港股IPO实时数据"""
        stocks = []
        seen_codes = set()

        try:
            import feedparser
            from urllib.parse import quote
            from datetime import datetime, timedelta

            keywords = [
                "港股 招股 入场费",
                "港股 IPO 招股",
                "港股 新股 公开发售",
            ]

            today = datetime.now()

            for kw in keywords:
                encoded_kw = quote(kw)
                url = f"https://news.google.com/rss/search?q={encoded_kw}&ceid=CN:zh&hl=zh-CN&num=50"

                try:
                    feed = feedparser.parse(url)
                    for entry in feed.entries[:30]:
                        title = entry.title

                        # 提取股票代码
                        import re
                        codes = re.findall(r'\b(\d{5})\.HK\b', title, re.IGNORECASE)
                        if not codes:
                            codes = re.findall(r'\((\d{5})\)', title)

                        for code in codes:
                            # 过滤无效代码
                            if code in seen_codes:
                                continue
                            # 过滤知名公司代码（如09988阿里），这些通常是新闻中提到的关联公司
                            if code == '09988':  # 阿里影业
                                continue
                            seen_codes.add(code)

                            # 提取公司名称 - 优先从代码前的名称提取
                            name = ""
                            # 模式: "公司名(代码.HK)" 或 "公司名 代码"
                            name_patterns = [
                                r'([^\s(0-9]+?(?:集团|控股|股份|有限|科技|医疗|智能|云|新能源|物业|服务|教育|传媒|金融|投资|地产|建筑|工程|户外|健康|出行|自动化|机械|机器人|知行))[\(（]',
                                r'[\(【]([^\)】]+?)[\)】]\s*\(\d{5}\.HK\)',
                                r'([^\s\-]+?)(?:\(|（|\s+\d{5})',
                            ]
                            for pattern in name_patterns:
                                name_match = re.search(pattern, title)
                                if name_match:
                                    name = name_match.group(1).strip()
                                    # 清理
                                    name = re.sub(r'^[《新股》【新股】IPO]+', '', name)
                                    name = name.strip()
                                    if name and len(name) >= 2:
                                        break

                            # 进一步清理
                            name = re.sub(r'^(新股|IPO)[:：\s]+', '', name)

                            # 处理单字母名称（如W）
                            if len(name) <= 2:
                                # 尝试从"公司名-W"格式提取
                                w_match = re.search(r'([^\(]+)-W[\(（]', title)
                                if w_match:
                                    name = w_match.group(1).strip() + "-W"
                                # 尝试提取"公司名-W(代码)"
                                elif '-W' in title:
                                    w_match2 = re.search(r'([^\-]+)-W', title)
                                    if w_match2:
                                        name = w_match2.group(1).strip() + "-W"

                            # 清理前缀标记
                            name = re.sub(r'^[《新股》【新股】IPO\-]+', '', name)

                            # 提取发行价
                            price = 0.0
                            price_match = re.search(r'([\d.]+)\s*(?:港元|元)', title)
                            if price_match:
                                try:
                                    price = float(price_match.group(1))
                                except:
                                    pass

                            if price == 0:
                                price_match = re.search(r'定价[:：]?\s*([\d.]+)', title)
                                if price_match:
                                    try:
                                        price = float(price_match.group(1))
                                    except:
                                        pass

                            # 提取入场费
                            entrance_fee = 0
                            fee_match = re.search(r'入场费\s*([\d,]+)', title)
                            if fee_match:
                                try:
                                    entrance_fee = int(fee_match.group(1).replace(',', ''))
                                except:
                                    pass

                            # 每手股数
                            lot_size = 1000
                            lot_match = re.search(r'每手\s*(\d+)\s*股', title)
                            if lot_match:
                                try:
                                    lot_size = int(lot_match.group(1))
                                except:
                                    pass

                            # 超额认购倍数
                            oversubscription = 0.0
                            over_match = re.search(r'(\d+(?:\.\d+)?)\s*倍', title)
                            if over_match:
                                try:
                                    oversubscription = float(over_match.group(1))
                                except:
                                    pass

                            # 判断状态
                            status = "待定"
                            if '招股' in title or '申购' in title:
                                status = "申购中"
                            elif '暗盘' in title:
                                status = "暗盘"
                            elif '上市' in title:
                                status = "已上市"

                            # 设置日期
                            apply_start = ""
                            apply_end = ""
                            list_date = ""
                            dark_date = ""
                            unfreeze_date = ""

                            if status == "申购中":
                                apply_start = today.strftime('%Y-%m-%d')
                                apply_end = (today + timedelta(days=4)).strftime('%Y-%m-%d')
                                list_date = (today + timedelta(days=7)).strftime('%Y-%m-%d')
                                dark_date = (today + timedelta(days=6)).strftime('%Y-%m-%d')
                                unfreeze_date = (today + timedelta(days=5)).strftime('%Y-%m-%d')

                            # 判断行业
                            industry = ""
                            industries = {
                                '科技': ['科技', '智能', 'AI', '云', '软件'],
                                '医疗': ['医疗', '生物', '医药', '健康'],
                                '新能源': ['新能源', '电池', '光伏', '电动车'],
                                '物业': ['物业', '地产'],
                                '金融': ['金融', '银行', '保险'],
                                '消费': ['消费', '零售', '餐饮'],
                                '物流': ['物流', '供应链'],
                                '教育': ['教育'],
                                '机械': ['机械', '机器人'],
                            }
                            for ind, kws in industries.items():
                                for kw_item in kws:
                                    if kw_item in title:
                                        industry = ind
                                        break
                                if industry:
                                    break

                            stock = HKIPOStock(
                                code=code,
                                name=name,
                                apply_start=apply_start,
                                apply_end=apply_end,
                                list_date=list_date,
                                dark_date=dark_date,
                                unfreeze_date=unfreeze_date,
                                price=price,
                                market_cap=0,
                                pe_ratio=0,
                                industry=industry,
                                sponsor="",
                                cornerstone_investors="",
                                oversubscription=oversubscription,
                                win_rate=0,
                                lot_size=lot_size
                            )
                            stocks.append(stock)

                except Exception as e:
                    self._log(f"[WARN] Google News获取失败: {e}")

        except ImportError:
            self._log("[WARN] feedparser未安装")
        except Exception as e:
            self._log(f"[WARN] Google News获取异常: {e}")

        return stocks

    def get_hk_ipo_list(self) -> List[HKIPOStock]:
        """获取港股IPO列表"""
        stocks = []

        try:
            # 尝试使用akshare获取港股IPO数据
            # 由于akshare可能没有直接的港股IPO接口，我们尝试多个数据源
            df = None

            # 方法1: 尝试东方财富港股IPO
            try:
                df = self.ak.stock_hk_ipo_cninfo()
            except:
                pass

            if df is None or df.empty:
                # 优先从富途获取数据（更快更稳定）
                self._log("[INFO] 尝试从富途获取港股IPO数据...")
                stocks = self._fetch_from_futu()

                # 如果富途没有正在申购的IPO，尝试Google News获取实时招股信息
                if not stocks:
                    self._log("[INFO] 富途无数据，尝试从Google News获取...")
                    stocks = self._fetch_from_google_news()

                if not stocks:
                    self._log("[INFO] 无法获取真实港股IPO数据，使用模拟数据")
                    stocks = self._get_mock_data()
                return stocks

            self._log(f"[INFO] 获取到 {len(df)} 条港股IPO记录")

            for _, row in df.iterrows():
                try:
                    code = self._parse_hk_code(row.get('股票代码', ''))
                    if not code:
                        continue

                    stock = HKIPOStock(
                        code=code,
                        name=str(row.get('公司名称', row.get('公司简称', ''))),
                        apply_start=self._safe_date(row.get('申购开始日期')),
                        apply_end=self._safe_date(row.get('申购截止日期')),
                        list_date=self._safe_date(row.get('上市日期')),
                        dark_date=self._safe_date(row.get('暗盘日期')),
                        unfreeze_date=self._safe_date(row.get('解冻日')),
                        price=self._safe_float(row.get('发行价')),
                        market_cap=self._safe_float(row.get('市值亿')),
                        pe_ratio=self._safe_float(row.get('市盈率')),
                        industry=str(row.get('行业', '')),
                        sponsor=str(row.get('保荐人', '')),
                        cornerstone_investors=str(row.get('基石投资者', '')),
                        oversubscription=self._safe_float(row.get('超额认购倍数')),
                        win_rate=self._safe_float(row.get('一手中签率')),
                        lot_size=self._safe_int(row.get('每手股数', 1000))
                    )

                    # 过滤有效数据
                    if stock.apply_start or stock.apply_end:
                        stocks.append(stock)

                except Exception as e:
                    self._log(f"[WARN] 解析IPO记录失败: {e}")
                    continue

        except Exception as e:
            self._log(f"[ERROR] 获取港股IPO数据失败: {e}")
            stocks = self._get_mock_data()

        # 如果没有获取到数据，使用模拟数据用于演示
        if not stocks:
            stocks = self._get_mock_data()

        return stocks

    def _get_mock_data(self) -> List[HKIPOStock]:
        """获取模拟数据用于演示"""
        today = datetime.now()
        stocks = []

        # 模拟一只近期可申购的IPO
        apply_start = (today + timedelta(days=1)).strftime('%Y-%m-%d')
        apply_end = (today + timedelta(days=4)).strftime('%Y-%m-%d')
        list_date = (today + timedelta(days=7)).strftime('%Y-%m-%d')
        dark_date = (today + timedelta(days=6)).strftime('%Y-%m-%d')
        unfreeze_date = (today + timedelta(days=5)).strftime('%Y-%m-%d')

        stocks.append(HKIPOStock(
            code="09988",
            name="阿里影业",
            apply_start=apply_start,
            apply_end=apply_end,
            list_date=list_date,
            dark_date=dark_date,
            unfreeze_date=unfreeze_date,
            price=2.80,
            market_cap=45.6,
            pe_ratio=18.5,
            industry="影视娱乐",
            sponsor="摩根士丹利、瑞信",
            cornerstone_investors="高瓴资本、OrbiMed",
            oversubscription=120.5,
            win_rate=35.0,
            lot_size=1000
        ))

        # 再添加一只
        apply_start2 = (today + timedelta(days=3)).strftime('%Y-%m-%d')
        apply_end2 = (today + timedelta(days=6)).strftime('%Y-%m-%d')
        list_date2 = (today + timedelta(days=10)).strftime('%Y-%m-%d')
        dark_date2 = (today + timedelta(days=9)).strftime('%Y-%m-%d')
        unfreeze_date2 = (today + timedelta(days=8)).strftime('%Y-%m-%d')

        stocks.append(HKIPOStock(
            code="09618",
            name="京东物流",
            apply_start=apply_start2,
            apply_end=apply_end2,
            list_date=list_date2,
            dark_date=dark_date2,
            unfreeze_date=unfreeze_date2,
            price=68.0,
            market_cap=450.0,
            pe_ratio=25.3,
            industry="物流",
            sponsor="高盛、摩根大通",
            cornerstone_investors="软银愿景、高瓴资本",
            oversubscription=85.2,
            win_rate=28.0,
            lot_size=100
        ))

        # 添加一只已申购结束即将上市的
        apply_start3 = (today - timedelta(days=2)).strftime('%Y-%m-%d')
        apply_end3 = (today - timedelta(days=1)).strftime('%Y-%m-%d')
        list_date3 = (today + timedelta(days=2)).strftime('%Y-%m-%d')
        dark_date3 = (today + timedelta(days=1)).strftime('%Y-%m-%d')
        unfreeze_date3 = today.strftime('%Y-%m-%d')

        stocks.append(HKIPOStock(
            code="09999",
            name="网易云音乐",
            apply_start=apply_start3,
            apply_end=apply_end3,
            list_date=list_date3,
            dark_date=dark_date3,
            unfreeze_date=unfreeze_date3,
            price=125.0,
            market_cap=520.0,
            pe_ratio=35.8,
            industry="在线音乐",
            sponsor="中金、摩根大通",
            cornerstone_investors="OrbiMed、网易",
            oversubscription=200.0,
            win_rate=15.0,
            lot_size=50
        ))

        return stocks

    def calculate_score(self, stock: HKIPOStock) -> Dict:
        """计算IPO评分"""
        score = 0.0
        factors = {
            'fundamental': 0.0,   # 基本面
            'industry': 0.0,     # 板块
            'subscription': 0.0, # 申购数据
            'sponsor': 0.0,       # 保荐人
            'history': 0.0       # 历史表现
        }

        # 1. 基本面评分 (25分)
        # 市值评分：合理市值范围加分
        if 20 <= stock.market_cap <= 500:
            factors['fundamental'] = 15.0
        elif stock.market_cap > 500:
            factors['fundamental'] = 10.0
        else:
            factors['fundamental'] = 5.0

        # PE评分：合理PE加分
        if 0 < stock.pe_ratio <= 20:
            factors['fundamental'] += 10.0
        elif 20 < stock.pe_ratio <= 30:
            factors['fundamental'] += 7.0
        elif stock.pe_ratio > 30:
            factors['fundamental'] += 3.0

        # 基石投资者加分
        if stock.cornerstone_investors and stock.cornerstone_investors not in ('', '-', '无'):
            factors['fundamental'] += 5.0

        # 2. 板块评分 (15分)
        preferred_industries = ['科技', '生物医药', '新经济', '互联网', '医疗', 'AI', '云计算']
        if any(ind in stock.industry for ind in preferred_industries):
            factors['industry'] = 15.0
        elif stock.industry:
            factors['industry'] = 8.0
        else:
            factors['industry'] = 5.0

        # 3. 申购数据评分 (25分)
        # 超额认购倍数：越高说明市场认可度高
        if stock.oversubscription >= 100:
            factors['subscription'] = 15.0
        elif stock.oversubscription >= 50:
            factors['subscription'] = 12.0
        elif stock.oversubscription >= 20:
            factors['subscription'] = 8.0
        elif stock.oversubscription > 0:
            factors['subscription'] = 5.0
        else:
            factors['subscription'] = 3.0

        # 一手中签率：越低说明越难中，但也说明热度高
        if 0 < stock.win_rate <= 20:
            factors['subscription'] += 10.0
        elif 20 < stock.win_rate <= 50:
            factors['subscription'] += 7.0
        elif stock.win_rate > 50:
            factors['subscription'] += 4.0

        # 4. 保荐人评分 (15分)
        # 根据保荐人历史表现评分
        top_sponsors = ['摩根士丹利', '高盛', '摩根大通', '中金', '瑞信', '花旗', '汇丰']
        if any(sp in stock.sponsor for sp in top_sponsors):
            factors['sponsor'] = 15.0
        elif stock.sponsor:
            factors['sponsor'] = 10.0
        else:
            factors['sponsor'] = 5.0

        # 5. 历史表现评分 (20分)
        # 这里简化处理，实际应该查询同类IPO历史表现
        # 热门行业历史表现通常较好
        if any(ind in stock.industry for ind in ['科技', '互联网', '新经济']):
            factors['history'] = 15.0
        elif any(ind in stock.industry for ind in ['生物医药', '医疗']):
            factors['history'] = 12.0
        else:
            factors['history'] = 8.0

        score = factors['fundamental'] + factors['industry'] + factors['subscription'] + factors['sponsor'] + factors['history']

        return {
            'total_score': round(score, 1),
            'factors': factors,
            'recommendation': self._get_recommendation(score)
        }

    def _get_recommendation(self, score: float) -> str:
        """根据评分获取建议"""
        if score >= 80:
            return "强烈建议申购"
        elif score >= 70:
            return "建议重点申购"
        elif score >= 60:
            return "可以申购"
        elif score >= 50:
            return "谨慎申购"
        else:
            return "不建议申购"

    def calculate_allocation(self, stocks: List[HKIPOStock], total_fund: float = 5000000,
                           min_score: float = 60.0) -> List[Dict]:
        """计算资金分配建议"""
        # 先计算每只股票的评分
        scored_stocks = []
        for stock in stocks:
            score_info = self.calculate_score(stock)
            scored_stocks.append({
                'stock': stock,
                'score': score_info['total_score'],
                'recommendation': score_info['recommendation'],
                'factors': score_info['factors']
            })

        # 过滤低于最低评分的股票
        scored_stocks = [s for s in scored_stocks if s['score'] >= min_score]

        # 按评分排序
        scored_stocks.sort(key=lambda x: x['score'], reverse=True)

        # 资金分配算法
        allocations = []
        remaining_fund = total_fund

        # 计算总分
        total_score = sum(s['score'] for s in scored_stocks)

        for i, item in enumerate(scored_stocks):
            # 按评分比例分配，但保证最少分配
            if total_score > 0:
                # 基础分配：按评分比例
                base_allocation = (item['score'] / total_score) * total_fund
            else:
                base_allocation = total_fund / len(scored_stocks)

            # 调整：高分项目多分配
            if i == 0:
                # 第一名多分配
                allocation = min(base_allocation * 1.3, remaining_fund)
            elif i == 1:
                allocation = min(base_allocation * 1.1, remaining_fund)
            else:
                allocation = min(base_allocation, remaining_fund)

            # 确保单笔分配不超过总资金的40%
            allocation = min(allocation, total_fund * 0.4)

            # 确保最低分配（如果有足够资金）
            min_allocation = 1000000  # 100万最低
            if remaining_fund >= min_allocation and allocation < min_allocation:
                allocation = min_allocation

            if allocation < 100000:  # 小于10万不分配
                continue

            allocations.append({
                'code': item['stock'].code,
                'name': item['stock'].name,
                'score': item['score'],
                'recommendation': item['recommendation'],
                'allocated_amount': int(allocation),
                'apply_start': item['stock'].apply_start,
                'apply_end': item['stock'].apply_end,
                'price': item['stock'].price,
                'oversubscription': item['stock'].oversubscription,
                'win_rate': item['stock'].win_rate
            })

            remaining_fund -= allocation

            if remaining_fund < 1000000:  # 剩余资金不足100万
                break

        return allocations

    def check_fund_conflicts(self, stocks: List[HKIPOStock]) -> List[Dict]:
        """检测资金时间冲突"""
        conflicts = []

        for i, stock1 in enumerate(stocks):
            if not stock1.apply_start or not stock1.apply_end:
                continue

            for stock2 in stocks[i+1:]:
                if not stock2.apply_start or not stock2.apply_end:
                    continue

                # 检查申购期间是否有重叠
                if stock1.apply_start <= stock2.apply_end and stock2.apply_start <= stock1.apply_end:
                    # 检查解冻日是否冲突
                    if stock1.unfreeze_date and stock2.unfreeze_date:
                        if stock1.unfreeze_date != stock2.unfreeze_date:
                            conflicts.append({
                                'stock1': {'code': stock1.code, 'name': stock1.name,
                                          'unfreeze_date': stock1.unfreeze_date},
                                'stock2': {'code': stock2.code, 'name': stock2.name,
                                          'unfreeze_date': stock2.unfreeze_date},
                                'type': '资金冻结重叠',
                                'suggestion': f"优先保证{stock1.name if stock1.unfreeze_date > stock2.unfreeze_date else stock2.name}"
                            })

        return conflicts

    def run(self) -> Dict:
        """执行数据获取"""
        today = datetime.now()
        today_str = today.strftime('%Y-%m-%d')
        future_date = (today + timedelta(days=self.lookahead_days)).strftime('%Y-%m-%d')

        # 检查是否需要推送
        should_push = True
        if self.cache:
            last_push = self.cache.get_last_push_date("hk_ipo_calendar")
            if last_push == today_str:
                should_push = False
                self._log(f"[INFO] 今日已推送过港股IPO日历")

        # 获取数据
        all_stocks = self.get_hk_ipo_list()

        # 缓存数据
        if self.cache and all_stocks:
            self.cache.save_ipo_data(all_stocks)

        # 分类
        today_apply = []      # 今日可申购
        upcoming_apply = []   # 近期可申购
        in_progress = []      # 申购进行中
        recently_listed = []  # 近期上市（已完成IPO）

        for stock in all_stocks:
            if stock.apply_start and stock.apply_end:
                # 有申购日期的IPO
                if stock.apply_start <= today_str <= stock.apply_end:
                    in_progress.append(stock)
                elif stock.apply_start > today_str and stock.apply_start <= future_date:
                    upcoming_apply.append(stock)
                elif stock.apply_start == today_str:
                    today_apply.append(stock)
            elif stock.list_date:
                # 已完成上市的IPO，归类为近期上市
                recently_listed.append(stock)

        # 计算评分和分配
        apply_stocks = in_progress + today_apply + upcoming_apply
        scores = []
        for stock in apply_stocks:
            score_info = self.calculate_score(stock)
            scores.append({
                'stock': stock,
                'score': score_info['total_score'],
                'recommendation': score_info['recommendation']
            })

        # 也为近期上市的IPO计算评分（供参考）
        for stock in recently_listed:
            score_info = self.calculate_score(stock)
            scores.append({
                'stock': stock,
                'score': score_info['total_score'],
                'recommendation': score_info['recommendation']
            })

        # 资金分配建议
        total_fund = 5000000  # 500万总资金
        allocations = self.calculate_allocation(apply_stocks, total_fund)

        # 资金冲突检测
        conflicts = self.check_fund_conflicts(apply_stocks)

        # 按日期排序
        upcoming_apply.sort(key=lambda x: x.apply_start)

        # 保存推送记录
        has_content = bool(today_apply or in_progress or upcoming_apply or allocations)
        if self.cache and should_push and has_content:
            self.cache.set_push_date("hk_ipo_calendar", today_str)

        result = {
            "type": "hk_ipo_calendar_report",
            "timestamp": today.isoformat(),
            "date": today_str,
            "should_push": should_push and has_content,
            "summary": {
                "today_apply": len(today_apply),
                "in_progress": len(in_progress),
                "upcoming_apply": len(upcoming_apply),
                "recently_listed": len(recently_listed),
                "total_available": len(apply_stocks) + len(recently_listed)
            },
            "today_apply": [asdict(s) for s in today_apply],
            "in_progress": [asdict(s) for s in in_progress],
            "upcoming_apply": [asdict(s) for s in upcoming_apply[:10]],
            "recently_listed": [asdict(s) for s in recently_listed[:10]],
            "scores": [
                {
                    'code': s['stock'].code,
                    'name': s['stock'].name,
                    'score': s['score'],
                    'recommendation': s['recommendation']
                }
                for s in scores[:10]
            ],
            "allocations": allocations,
            "conflicts": conflicts,
            "config": {
                "total_fund": total_fund,
                "margin_ratio": 10,
                "min_score": 60
            }
        }

        return result


def main():
    parser = argparse.ArgumentParser(description='港股IPO日历')
    parser.add_argument('--json-only', action='store_true', help='仅输出JSON')
    parser.add_argument('--no-cache', action='store_true', help='禁用缓存')
    parser.add_argument('--quiet', '-q', action='store_true', help='静默模式')
    parser.add_argument('--force', action='store_true', help='强制推送')
    parser.add_argument('--lookahead', type=int, default=14, help='提前展示天数')
    parser.add_argument('--total-fund', type=float, default=5000000, help='总资金(默认500万)')

    args = parser.parse_args()

    fetcher = HKIPOCalendarFetcher(
        use_cache=not args.no_cache,
        quiet=args.quiet or args.json_only,
        lookahead_days=args.lookahead
    )
    result = fetcher.run()

    if args.force:
        result['should_push'] = True

    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
