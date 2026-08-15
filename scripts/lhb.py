#!/usr/bin/env python3
"""
龙虎榜数据
每日收盘后推送，追踪机构和游资动向

数据内容：
- 机构席位净买入/净卖出
- 知名游资动向
- 营业部买卖排行
"""

import argparse
import json
import os
import sqlite3
import sys
from dataclasses import dataclass, asdict, field
from datetime import datetime, timedelta
from typing import Dict, List, Optional, Set
from pathlib import Path

# 导入交易日判断工具
try:
    from utils import is_trading_day, get_last_trading_day
except ImportError:
    is_trading_day = lambda d=None: True  # 降级：假设都是交易日
    get_last_trading_day = lambda d=None: datetime.now().strftime('%Y-%m-%d')


# 知名游资营业部列表
FAMOUS_TRADERS = {
    "中国银河证券深圳利雅南路": "赵老哥",
    "华鑫证券上海宛平南路": "章盟主",
    "国泰君安上海江苏路": "章盟主",
    "光大证券宁波解放南路": "涨停板敢死队",
    "华泰证券深圳益田路荣超商务中心": "深圳帮",
    "东方证券上海浦东新区银城中路": "顶级游资",
    "华泰证券成都南一环路第二": "成都帮",
    "中信证券上海溧阳路": "溧阳路",
    "国泰君安南京太平南路": "作手新一",
    "东方财富证券拉萨团结路第二": "拉萨帮",
    "东方财富证券拉萨东环路第二": "拉萨帮",
    "财通证券杭州上塘路": "杭州帮",
}


@dataclass
class LHBStock:
    """龙虎榜个股"""
    code: str
    name: str
    close: float
    change_pct: float
    reason: str             # 上榜原因
    buy_amount: float       # 买入金额（万）
    sell_amount: float      # 卖出金额（万）
    net_amount: float       # 净买入（万）
    turnover_rate: float    # 换手率


@dataclass
class SeatDetail:
    """席位明细"""
    seat_name: str          # 营业部名称
    buy_amount: float       # 买入金额
    sell_amount: float      # 卖出金额
    net_amount: float       # 净买入
    is_institution: bool    # 是否机构
    famous_trader: str      # 知名游资名称（如有）


@dataclass
class InstitutionSummary:
    """机构动向汇总"""
    buy_stocks: List[dict]      # 机构净买入股票
    sell_stocks: List[dict]     # 机构净卖出股票
    total_buy: float            # 机构总买入
    total_sell: float           # 机构总卖出


@dataclass
class FamousTraderActivity:
    """知名游资活动"""
    trader_name: str
    seat_name: str
    stocks: List[dict]          # 参与的股票


class CacheManager:
    """缓存管理器"""

    def __init__(self, cache_dir: str = "data/cache"):
        self.cache_dir = Path(cache_dir)
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        self.db_path = self.cache_dir / "lhb_cache.db"
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


class LHBFetcher:
    """龙虎榜数据获取器"""

    def __init__(self, use_cache: bool = True, quiet: bool = False,
                 min_net_amount: float = 5000, exclude_boards: List[str] = None):
        """
        Args:
            min_net_amount: 最小净买入/卖出金额（万元），用于过滤
            exclude_boards: 排除的板块 ['kcb', 'cyb', 'bse', 'st', 'b']
        """
        self.quiet = quiet
        self.cache = CacheManager() if use_cache else None
        self.min_net_amount = min_net_amount
        self.exclude_boards = exclude_boards or []

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
        if val is None:
            return 0.0
        try:
            if isinstance(val, str):
                val = val.replace('%', '').replace(',', '').replace('亿', '').replace('万', '').strip()
                if val == '' or val == '--' or val.lower() == 'nan':
                    return 0.0
            import math
            result = float(val)
            if math.isnan(result):
                return 0.0
            return result
        except (ValueError, TypeError):
            return 0.0

    def _should_exclude(self, code: str, name: str) -> bool:
        """检查是否应该排除"""
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
            if board == 'b' and (code.startswith('200') or code.startswith('900')):
                return True
        return False

    def get_lhb_stocks(self, date: str) -> List[LHBStock]:
        """获取龙虎榜股票列表"""
        stocks = []
        try:
            df = self.ak.stock_lhb_detail_em(start_date=date, end_date=date)
            if df is None or df.empty:
                self._log(f"[WARN] 龙虎榜数据为空")
                return stocks

            self._log(f"[INFO] 获取到 {len(df)} 条龙虎榜记录")

            for _, row in df.iterrows():
                code = str(row.get('代码', '')).zfill(6)
                name = str(row.get('名称', ''))

                if self._should_exclude(code, name):
                    continue

                # 使用正确的列名
                buy_amount = self._safe_float(row.get('龙虎榜买入额', 0))
                sell_amount = self._safe_float(row.get('龙虎榜卖出额', 0))
                net_amount = self._safe_float(row.get('龙虎榜净买额', buy_amount - sell_amount))

                stocks.append(LHBStock(
                    code=code,
                    name=name,
                    close=self._safe_float(row.get('收盘价', 0)),
                    change_pct=self._safe_float(row.get('涨跌幅', 0)),
                    reason=str(row.get('上榜原因', '')),
                    buy_amount=buy_amount,
                    sell_amount=sell_amount,
                    net_amount=net_amount,
                    turnover_rate=self._safe_float(row.get('换手率', 0))
                ))

        except Exception as e:
            self._log(f"[WARN] 获取龙虎榜列表失败: {e}")

        return stocks

    def get_institution_activity(self, date: str) -> InstitutionSummary:
        """获取机构席位活动"""
        buy_stocks = []
        sell_stocks = []
        total_buy = 0
        total_sell = 0

        try:
            # 机构买卖统计（包含买入和卖出数据）
            df = self.ak.stock_lhb_jgmmtj_em(start_date=date, end_date=date)
            if df is not None and not df.empty:
                self._log(f"[INFO] 机构动向记录: {len(df)}")
                for _, row in df.iterrows():
                    code = str(row.get('代码', '')).zfill(6)
                    name = str(row.get('名称', ''))
                    if self._should_exclude(code, name):
                        continue

                    buy_amt = self._safe_float(row.get('机构买入总额', 0))
                    sell_amt = self._safe_float(row.get('机构卖出总额', 0))
                    net_amt = self._safe_float(row.get('机构买入净额', buy_amt - sell_amt))
                    change_pct = self._safe_float(row.get('涨跌幅', 0))
                    buy_count = int(row.get('买方机构数', 0))
                    sell_count = int(row.get('卖方机构数', 0))

                    total_buy += buy_amt
                    total_sell += sell_amt

                    # 净买入 > 0 的归入买入列表
                    if net_amt > 0:
                        buy_stocks.append({
                            'code': code,
                            'name': name,
                            'amount': net_amt,  # 使用净买入
                            'change_pct': change_pct,
                            'buy_count': buy_count
                        })
                    # 净卖出的归入卖出列表
                    elif net_amt < 0:
                        sell_stocks.append({
                            'code': code,
                            'name': name,
                            'amount': abs(net_amt),  # 使用净卖出的绝对值
                            'change_pct': change_pct,
                            'sell_count': sell_count
                        })

        except Exception as e:
            self._log(f"[WARN] 获取机构动向失败: {e}")

        # 按金额排序
        buy_stocks.sort(key=lambda x: x['amount'], reverse=True)
        sell_stocks.sort(key=lambda x: x['amount'], reverse=True)

        return InstitutionSummary(
            buy_stocks=buy_stocks[:10],
            sell_stocks=sell_stocks[:10],
            total_buy=total_buy,
            total_sell=total_sell
        )

    def get_famous_traders(self, date: str) -> List[FamousTraderActivity]:
        """获取知名游资活动"""
        trader_stocks = {}  # {(trader_name, seat_name): [stock_info, ...]}

        try:
            # 1. 获取当日所有龙虎榜股票
            df = self.ak.stock_lhb_detail_em(start_date=date, end_date=date)
            if df is None or df.empty:
                return []

            self._log(f"[INFO] 龙虎榜股票数: {len(df)}")
            date_fmt = date.replace('-', '')

            # 2. 遍历每只股票，获取席位详情
            for _, row in df.iterrows():
                code = str(row.get('代码', '')).zfill(6)
                name = str(row.get('名称', ''))
                change_pct = self._safe_float(row.get('涨跌幅', 0))

                if self._should_exclude(code, name):
                    continue

                # 获取买入席位详情
                try:
                    buy_df = self.ak.stock_lhb_stock_detail_em(
                        symbol=code, date=date_fmt, flag="买入"
                    )
                    if buy_df is not None and not buy_df.empty:
                        for _, seat_row in buy_df.iterrows():
                            seat_name = str(seat_row.get('营业部名称', ''))

                            # 检查是否是知名游资
                            trader_name = None
                            for key, tname in FAMOUS_TRADERS.items():
                                if key in seat_name:
                                    trader_name = tname
                                    break

                            if trader_name:
                                key = (trader_name, seat_name)
                                if key not in trader_stocks:
                                    trader_stocks[key] = []

                                buy_amount = self._safe_float(seat_row.get('买入金额', 0))
                                sell_amount = self._safe_float(seat_row.get('卖出金额', 0))

                                trader_stocks[key].append({
                                    'code': code,
                                    'name': name,
                                    'buy_amount': buy_amount,
                                    'sell_amount': sell_amount,
                                    'change_pct': change_pct
                                })
                except Exception:
                    pass  # 个别股票查询失败不影响整体

        except Exception as e:
            self._log(f"[WARN] 获取游资活动失败: {e}")

        # 构建结果
        activities = []
        for (trader_name, seat_name), stocks in trader_stocks.items():
            if stocks:
                activities.append(FamousTraderActivity(
                    trader_name=trader_name,
                    seat_name=seat_name,
                    stocks=stocks
                ))

        # 按股票数量排序
        activities.sort(key=lambda x: len(x.stocks), reverse=True)
        return activities

    def get_top_seats(self, date: str, top_n: int = 10) -> List[dict]:
        """获取营业部买入排行"""
        seats = []
        try:
            df = self.ak.stock_lhb_hyyyb_em(start_date=date, end_date=date)
            if df is None or df.empty:
                return seats

            for _, row in df.iterrows():
                seat = str(row.get('营业部名称', ''))
                buy_total = self._safe_float(row.get('买入总金额', 0))
                sell_total = self._safe_float(row.get('卖出总金额', 0))
                net_amount = self._safe_float(row.get('总买卖净额', buy_total - sell_total))

                seats.append({
                    'name': seat,
                    'buy_total': buy_total,
                    'sell_total': sell_total,
                    'net_amount': net_amount,
                    'stock_count': int(row.get('买入个股数', 0)) + int(row.get('卖出个股数', 0)),
                    'is_famous': any(key in seat for key in FAMOUS_TRADERS.keys())
                })

            seats.sort(key=lambda x: x['net_amount'], reverse=True)

        except Exception as e:
            self._log(f"[WARN] 获取营业部排行失败: {e}")

        return seats[:top_n]

    def run(self, date: str = None) -> Dict:
        """执行数据获取"""
        now = datetime.now()

        if date is None:
            # 如果今天不是交易日，使用最近的交易日
            if not is_trading_day(now):
                date = get_last_trading_day(now).replace('-', '')
                self._log(f"[INFO] 今天非交易日，使用最近交易日 {date}")
            else:
                date = now.strftime('%Y%m%d')
        else:
            date = date.replace('-', '')

        today_str = now.strftime('%Y-%m-%d')
        date_display = f"{date[:4]}-{date[4:6]}-{date[6:]}"

        # 检查是否需要推送
        should_push = True
        if self.cache:
            last_push = self.cache.get_last_push_date("lhb")
            if last_push == date_display:
                should_push = False
                self._log(f"[INFO] {date_display} 已推送过龙虎榜")

        # 获取数据
        self._log(f"[INFO] 获取龙虎榜数据 {date}...")
        stocks = self.get_lhb_stocks(date)

        self._log("[INFO] 获取机构动向...")
        institution = self.get_institution_activity(date)

        self._log("[INFO] 获取知名游资...")
        famous = self.get_famous_traders(date)

        self._log("[INFO] 获取营业部排行...")
        top_seats = self.get_top_seats(date)

        # 保存推送记录
        if self.cache and should_push and (stocks or institution.buy_stocks or famous):
            self.cache.set_push_date("lhb", date_display)

        # 过滤净买入/卖出金额较小的
        stocks = [s for s in stocks if abs(s.net_amount) >= self.min_net_amount]
        stocks.sort(key=lambda x: x.net_amount, reverse=True)

        result = {
            "type": "lhb_report",
            "timestamp": datetime.now().isoformat(),
            "date": date_display,
            "should_push": should_push,
            "summary": {
                "total_stocks": len(stocks),
                "institution_buy_count": len(institution.buy_stocks),
                "institution_sell_count": len(institution.sell_stocks),
                "famous_trader_count": len(famous)
            },
            "stocks": [asdict(s) for s in stocks[:20]],
            "institution": asdict(institution),
            "famous_traders": [asdict(f) for f in famous],
            "top_seats": top_seats
        }

        return result


def main():
    parser = argparse.ArgumentParser(description='龙虎榜数据')
    parser.add_argument('--date', type=str, help='日期 YYYYMMDD')
    parser.add_argument('--json-only', action='store_true', help='仅输出JSON')
    parser.add_argument('--no-cache', action='store_true', help='禁用缓存')
    parser.add_argument('--quiet', '-q', action='store_true', help='静默模式')
    parser.add_argument('--force', action='store_true', help='强制推送')
    parser.add_argument('--min-amount', type=float, default=5000, help='最小净额(万)')
    parser.add_argument('--exclude-boards', type=str, default='', help='排除板块,逗号分隔')

    args = parser.parse_args()

    exclude_boards = [b.strip() for b in args.exclude_boards.split(',') if b.strip()]

    fetcher = LHBFetcher(
        use_cache=not args.no_cache,
        quiet=args.quiet or args.json_only,
        min_net_amount=args.min_amount,
        exclude_boards=exclude_boards
    )
    result = fetcher.run(args.date)

    if args.force:
        result['should_push'] = True

    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
