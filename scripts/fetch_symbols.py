#!/usr/bin/env python3
"""
使用 AKShare 抓取各市场标的数据，生成本地索引库
运行: pip install akshare && python scripts/fetch_symbols.py
"""

import json
import os
from datetime import datetime

def fetch_a_shares():
    """获取 A 股列表"""
    import akshare as ak
    print("正在获取 A 股数据...")
    try:
        # 获取所有 A 股代码和名称
        df = ak.stock_info_a_code_name()
        result = []
        for _, row in df.iterrows():
            code = str(row['code'])
            name = row['name']
            # 判断交易所
            if code.startswith('6'):
                symbol = f"{code}.SS"
                exchange = "SSE"
            elif code.startswith(('0', '3')):
                symbol = f"{code}.SZ"
                exchange = "SZSE"
            else:
                symbol = code
                exchange = "OTHER"

            result.append({
                "symbol": symbol,
                "code": code,
                "name": name,
                "type": "stock",
                "market": "CN",
                "exchange": exchange
            })
        print(f"  获取到 {len(result)} 只 A 股")
        return result
    except Exception as e:
        print(f"  获取 A 股失败: {e}")
        return []

def fetch_hk_stocks():
    """获取港股列表"""
    import akshare as ak
    print("正在获取港股数据...")
    result = []
    seen = set()

    # 方法1: 尝试东方财富港股实时行情
    try:
        df = ak.stock_hk_spot_em()
        for _, row in df.iterrows():
            code = str(row.get('代码', '')).zfill(5)
            name = row.get('名称', '')
            if not code or code in seen:
                continue
            seen.add(code)
            result.append({
                "symbol": f"{code}.HK",
                "code": code,
                "name": name,
                "type": "stock",
                "market": "HK",
                "exchange": "HKEX"
            })
        print(f"  从东方财富获取到 {len(result)} 只港股")
    except Exception as e:
        print(f"  东方财富港股接口失败: {e}")

    # 方法2: 尝试新浪港股
    if len(result) < 100:
        try:
            df = ak.stock_hk_spot()
            for _, row in df.iterrows():
                symbol = str(row.get('symbol', ''))
                name = row.get('name', '')
                code = symbol.replace('.HK', '').zfill(5)
                if not code or code in seen:
                    continue
                seen.add(code)
                result.append({
                    "symbol": f"{code}.HK",
                    "code": code,
                    "name": name,
                    "type": "stock",
                    "market": "HK",
                    "exchange": "HKEX"
                })
            print(f"  从新浪补充后共 {len(result)} 只港股")
        except Exception as e:
            print(f"  新浪港股接口失败: {e}")

    # 方法3: 添加热门港股备用列表（确保常用股票可查）
    popular_hk = get_hk_popular_stocks()
    for item in popular_hk:
        if item["code"] not in seen:
            seen.add(item["code"])
            result.append(item)

    print(f"  港股总计: {len(result)} 只")
    return result

def get_hk_popular_stocks():
    """港股热门股票备用列表"""
    stocks = [
        # 恒指成分股 & 科技股
        ("00700", "腾讯控股"), ("09988", "阿里巴巴-SW"), ("03690", "美团-W"),
        ("01810", "小米集团-W"), ("09999", "网易-S"), ("09618", "京东集团-SW"),
        ("09888", "百度集团-SW"), ("01024", "快手-W"), ("00981", "中芯国际"),
        ("09961", "携程集团-S"), ("02015", "理想汽车-W"), ("09866", "蔚来-SW"),
        ("09868", "小鹏汽车-W"), ("00285", "比亚迪电子"), ("01211", "比亚迪股份"),
        ("02382", "舜宇光学科技"), ("00241", "阿里健康"), ("06618", "京东健康"),
        ("02269", "药明生物"), ("01833", "平安好医生"),
        # 消费
        ("09633", "农夫山泉"), ("06862", "海底捞"), ("09992", "泡泡玛特"),
        ("02020", "安踏体育"), ("02331", "李宁"), ("01928", "金沙中国有限公司"),
        ("00027", "银河娱乐"), ("06969", "思摩尔国际"), ("00291", "华润啤酒"),
        ("00168", "青岛啤酒股份"), ("00322", "康师傅控股"), ("06186", "中国飞鹤"),
        ("02319", "蒙牛乳业"), ("00220", "统一企业中国"), ("01579", "颐海国际"),
        ("09987", "百胜中国"), ("06098", "碧桂园服务"),
        # 金融
        ("00005", "汇丰控股"), ("01299", "友邦保险"), ("02318", "中国平安"),
        ("02628", "中国人寿"), ("01339", "中国人民保险集团"), ("00388", "香港交易所"),
        ("03988", "中国银行"), ("01398", "工商银行"), ("00939", "建设银行"),
        ("03968", "招商银行"), ("01288", "农业银行"), ("00998", "中信银行"),
        ("06881", "中国银河"), ("03908", "中金公司"), ("06030", "中信证券"),
        ("06066", "中信建投证券"),
        # 地产
        ("02007", "碧桂园"), ("01109", "华润置地"), ("00688", "中国海外发展"),
        ("00017", "新世界发展"), ("00012", "恒基兆业地产"), ("00016", "新鸿基地产"),
        ("00001", "长和"), ("00002", "中电控股"), ("00003", "香港中华煤气"),
        ("00006", "电能实业"), ("00011", "恒生银行"), ("00066", "港铁公司"),
        # 能源 & 原材料
        ("00857", "中国石油股份"), ("00386", "中国石油化工股份"), ("00883", "中国海洋石油"),
        ("01088", "中国神华"), ("03993", "洛阳钼业"), ("02600", "中国铝业"),
        ("00358", "江西铜业股份"), ("01898", "中煤能源"),
        # 通信 & 科技
        ("00941", "中国移动"), ("00728", "中国电信"), ("00762", "中国联通"),
        ("00763", "中兴通讯"), ("02018", "瑞声科技"),
        # 其他
        ("00175", "吉利汽车"), ("02238", "广汽集团"), ("00489", "东风集团股份"),
        ("01919", "中远海控"), ("02866", "中远海发"), ("06808", "高鑫零售"),
        ("00267", "中信股份"), ("00019", "太古股份公司A"), ("00083", "信和置业"),
        ("01113", "长实集团"), ("00823", "领展房产基金"),
    ]
    return [{"symbol": f"{s[0]}.HK", "code": s[0], "name": s[1], "type": "stock", "market": "HK", "exchange": "HKEX"} for s in stocks]

def fetch_us_stocks():
    """获取美股列表"""
    import akshare as ak
    print("正在获取美股数据...")
    try:
        df = ak.stock_us_spot_em()
        result = []
        for _, row in df.iterrows():
            code = row['代码']
            name = row['名称']
            # 清理代码格式
            symbol = code.replace('.', '-') if '.' in code else code
            result.append({
                "symbol": symbol,
                "code": code,
                "name": name,
                "type": "stock",
                "market": "US",
                "exchange": "US"
            })
        print(f"  获取到 {len(result)} 只美股")
        return result
    except Exception as e:
        print(f"  获取美股失败: {e}")
        return []

def fetch_crypto():
    """获取加密货币列表"""
    import akshare as ak
    print("正在获取加密货币数据...")
    try:
        df = ak.crypto_name_url_table()
        result = []
        for _, row in df.iterrows():
            name = row.get('名称', row.get('name', ''))
            code = row.get('代码', row.get('symbol', ''))
            if not code:
                continue
            result.append({
                "symbol": f"{code.upper()}USDT",
                "code": code.upper(),
                "name": name,
                "type": "crypto",
                "market": "CRYPTO",
                "exchange": "BINANCE"
            })
        print(f"  获取到 {len(result)} 个加密货币")
        return result
    except Exception as e:
        print(f"  获取加密货币失败: {e}")
        # 使用备用列表
        return get_crypto_fallback()

def get_crypto_fallback():
    """加密货币备用列表"""
    cryptos = [
        ("BTC", "比特币"), ("ETH", "以太坊"), ("BNB", "币安币"), ("XRP", "瑞波币"),
        ("SOL", "Solana"), ("ADA", "艾达币"), ("DOGE", "狗狗币"), ("TRX", "波场"),
        ("DOT", "波卡"), ("MATIC", "Polygon"), ("SHIB", "柴犬币"), ("LTC", "莱特币"),
        ("AVAX", "雪崩"), ("LINK", "Chainlink"), ("ATOM", "Cosmos"), ("UNI", "Uniswap"),
        ("ETC", "以太经典"), ("XLM", "恒星币"), ("FIL", "Filecoin"), ("NEAR", "Near"),
        ("APT", "Aptos"), ("ARB", "Arbitrum"), ("OP", "Optimism"), ("SUI", "Sui"),
        ("INJ", "Injective"), ("SEI", "Sei"), ("TON", "Toncoin"), ("PEPE", "Pepe"),
        ("WIF", "dogwifhat"), ("BONK", "Bonk"), ("FLOKI", "Floki"), ("MEME", "Memecoin"),
    ]
    return [{"symbol": f"{c[0]}USDT", "code": c[0], "name": c[1], "type": "crypto", "market": "CRYPTO", "exchange": "BINANCE"} for c in cryptos]

def fetch_futures():
    """获取期货列表"""
    import akshare as ak
    print("正在获取期货数据...")
    try:
        # 国内期货
        df = ak.futures_zh_spot()
        result = []
        for _, row in df.iterrows():
            symbol = row['symbol']
            name = row['name'] if 'name' in row else symbol
            result.append({
                "symbol": symbol,
                "code": symbol,
                "name": name,
                "type": "futures",
                "market": "CN",
                "exchange": "FUTURES"
            })
        print(f"  获取到 {len(result)} 个期货")
        return result
    except Exception as e:
        print(f"  获取期货失败: {e}")
        return get_futures_fallback()

def get_futures_fallback():
    """期货备用列表"""
    futures = [
        ("GC=F", "黄金", "COMEX"), ("SI=F", "白银", "COMEX"), ("CL=F", "WTI原油", "NYMEX"),
        ("BZ=F", "布伦特原油", "ICE"), ("NG=F", "天然气", "NYMEX"), ("HG=F", "铜", "COMEX"),
        ("PL=F", "铂金", "NYMEX"), ("PA=F", "钯金", "NYMEX"),
        ("ZC=F", "玉米", "CBOT"), ("ZS=F", "大豆", "CBOT"), ("ZW=F", "小麦", "CBOT"),
    ]
    return [{"symbol": f[0], "code": f[0], "name": f[1], "type": "futures", "market": "US", "exchange": f[2]} for f in futures]

def fetch_forex():
    """获取外汇列表"""
    print("正在获取外汇数据...")
    # 外汇使用固定列表
    forex = [
        ("EURUSD=X", "EUR/USD", "欧元/美元"),
        ("USDJPY=X", "USD/JPY", "美元/日元"),
        ("GBPUSD=X", "GBP/USD", "英镑/美元"),
        ("USDCNY=X", "USD/CNY", "美元/人民币"),
        ("USDCNH=X", "USD/CNH", "美元/离岸人民币"),
        ("AUDUSD=X", "AUD/USD", "澳元/美元"),
        ("USDCAD=X", "USD/CAD", "美元/加元"),
        ("USDCHF=X", "USD/CHF", "美元/瑞郎"),
        ("USDHKD=X", "USD/HKD", "美元/港币"),
        ("DX-Y.NYB", "DXY", "美元指数"),
    ]
    result = [{"symbol": f[0], "code": f[1], "name": f[2], "type": "forex", "market": "FX", "exchange": "FOREX"} for f in forex]
    print(f"  获取到 {len(result)} 个外汇")
    return result

def fetch_indices():
    """获取指数列表"""
    print("正在获取指数数据...")
    indices = [
        # 美国
        ("^GSPC", "SPX", "标普500"),
        ("^IXIC", "IXIC", "纳斯达克"),
        ("^DJI", "DJI", "道琼斯"),
        ("^RUT", "RUT", "罗素2000"),
        ("^VIX", "VIX", "恐慌指数"),
        # 中国
        ("000001.SS", "SHCOMP", "上证指数"),
        ("399001.SZ", "SZCOMP", "深证成指"),
        ("399006.SZ", "CHINEXT", "创业板指"),
        ("000300.SS", "CSI300", "沪深300"),
        ("000905.SS", "CSI500", "中证500"),
        ("000852.SS", "CSI1000", "中证1000"),
        # 香港
        ("^HSI", "HSI", "恒生指数"),
        ("^HSTECH", "HSTECH", "恒生科技"),
        ("^HSCE", "HSCE", "国企指数"),
        # 其他
        ("^N225", "N225", "日经225"),
        ("^GDAXI", "DAX", "德国DAX"),
        ("^FTSE", "FTSE", "富时100"),
    ]
    result = [{"symbol": i[0], "code": i[1], "name": i[2], "type": "index", "market": "INDEX", "exchange": "INDEX"} for i in indices]
    print(f"  获取到 {len(result)} 个指数")
    return result

def fetch_etfs():
    """获取 ETF 列表"""
    print("正在获取 ETF 数据...")
    etfs = [
        ("SPY", "SPY", "SPDR标普500"),
        ("QQQ", "QQQ", "纳指100ETF"),
        ("DIA", "DIA", "道指ETF"),
        ("IWM", "IWM", "罗素2000ETF"),
        ("ARKK", "ARKK", "ARK创新ETF"),
        ("GLD", "GLD", "黄金ETF"),
        ("SLV", "SLV", "白银ETF"),
        ("USO", "USO", "原油ETF"),
        ("TLT", "TLT", "20年期国债ETF"),
        ("VXX", "VXX", "恐慌ETF"),
        ("KWEB", "KWEB", "中概互联网ETF"),
        ("FXI", "FXI", "中国大盘ETF"),
        ("EEM", "EEM", "新兴市场ETF"),
        ("XLF", "XLF", "金融ETF"),
        ("XLK", "XLK", "科技ETF"),
        ("XLE", "XLE", "能源ETF"),
    ]
    result = [{"symbol": e[0], "code": e[1], "name": e[2], "type": "etf", "market": "US", "exchange": "NYSE"} for e in etfs]
    print(f"  获取到 {len(result)} 个 ETF")
    return result

def build_search_index(all_symbols):
    """构建搜索索引"""
    index = {
        "by_symbol": {},  # symbol -> item
        "by_code": {},    # code -> item
        "by_name": {},    # name -> item (用于精确匹配)
        "names": [],      # 所有名称列表 (用于模糊搜索)
    }

    for item in all_symbols:
        symbol = item["symbol"].upper()
        code = item["code"].upper()
        name = item["name"]

        index["by_symbol"][symbol] = item
        index["by_code"][code] = item
        index["by_name"][name] = item
        index["names"].append({"name": name, "symbol": symbol})

    return index

def main():
    print("=" * 50)
    print("AKShare 标的数据抓取工具")
    print("=" * 50)

    all_symbols = []

    # 抓取各市场数据
    all_symbols.extend(fetch_a_shares())
    all_symbols.extend(fetch_hk_stocks())
    all_symbols.extend(fetch_us_stocks())
    all_symbols.extend(fetch_crypto())
    all_symbols.extend(fetch_futures())
    all_symbols.extend(fetch_forex())
    all_symbols.extend(fetch_indices())
    all_symbols.extend(fetch_etfs())

    print(f"\n总计获取 {len(all_symbols)} 个标的")

    # 构建索引
    print("\n正在构建搜索索引...")
    index = build_search_index(all_symbols)

    # 保存数据
    output_dir = "data/index"
    os.makedirs(output_dir, exist_ok=True)

    # 保存完整数据
    with open(f"{output_dir}/symbols.json", "w", encoding="utf-8") as f:
        json.dump(all_symbols, f, ensure_ascii=False, indent=2)

    # 保存搜索索引
    with open(f"{output_dir}/search_index.json", "w", encoding="utf-8") as f:
        json.dump(index, f, ensure_ascii=False)

    # 保存元数据
    meta = {
        "updated_at": datetime.now().isoformat(),
        "total_count": len(all_symbols),
        "counts": {
            "a_shares": len([s for s in all_symbols if s["market"] == "CN" and s["type"] == "stock"]),
            "hk_stocks": len([s for s in all_symbols if s["market"] == "HK"]),
            "us_stocks": len([s for s in all_symbols if s["market"] == "US" and s["type"] == "stock"]),
            "crypto": len([s for s in all_symbols if s["type"] == "crypto"]),
            "futures": len([s for s in all_symbols if s["type"] == "futures"]),
            "forex": len([s for s in all_symbols if s["type"] == "forex"]),
            "indices": len([s for s in all_symbols if s["type"] == "index"]),
            "etfs": len([s for s in all_symbols if s["type"] == "etf"]),
        }
    }
    with open(f"{output_dir}/meta.json", "w", encoding="utf-8") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2)

    print(f"\n数据已保存到 {output_dir}/")
    print(f"  - symbols.json: 完整标的数据")
    print(f"  - search_index.json: 搜索索引")
    print(f"  - meta.json: 元数据")

    print("\n各市场统计:")
    for market, count in meta["counts"].items():
        print(f"  {market}: {count}")

if __name__ == "__main__":
    main()
