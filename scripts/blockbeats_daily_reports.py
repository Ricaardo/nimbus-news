#!/usr/bin/env python3
"""BlockBeats 日报 —— BTC / AI / 股票 三大板块，AI 提炼。

两个模式：
   morning (07:30): important + onchain + ai + original → AI 总结
   evening  (19:00): important + onchain + ai + original + 市场数据 → AI 总结

用法:
   BLOCKBEATS_API_KEY=... python3 scripts/blockbeats_daily_reports.py --mode morning
   BLOCKBEATS_API_KEY=... python3 scripts/blockbeats_daily_reports.py --mode evening
"""
import argparse
import json
import os
import re
import sys
import time
from collections import defaultdict
from datetime import datetime, timezone

# ── 导入标准库 urllib (避免 requests 依赖) ──
import urllib.error
import urllib.request

BASE_URL = "https://api-pro.theblockbeats.info"
_API_KEY = ""
TIMEOUT = 15

# DeepSeek 配置（从环境变量或 news .env 读取）
_LLM_URL = "https://api.deepseek.com/v1/chat/completions"
_LLM_KEY = ""
# 50s:含重试(2 次)最坏 50+2+50=102s + 数据抓取 ~15s = ~117s,
# 须低于平台 script 源 120s 超时(否则重试被 Go 侧 context 杀掉)。
_LLM_TIMEOUT = 50
_LLM_MODEL = "deepseek-v4-flash"


# ── 分类关键词 ──

class Topics:
    BTC = "btc"
    AI = "ai"
    STOCK = "stock"

# BTC 专属关键词（出现即判为 BTC 板块）
BTC_SPECIFIC_KEYWORDS = [
    "btc", "bitcoin", "比特币", "satoshi", "saylor", "微策略",
    "lightning network", "闪电网络", "ordinal", "符文", "现货比特币",
    "比特币etf", "btc etf", "ibit", "fbtc", "bitfinex",
]

# 屏蔽关键词：非 BTC 山寨币/meme/NFT/公链/交易所（defi/稳定币 保留）
BLOCK_KEYWORDS = [
    "eth", "ethereum", "以太坊", "sol", "solana", "avax", "polygon",
    "matic", "sui", "aptos", "atom", "cosmos", "dot", "polkadot",
    "meme", "nft", "空投", "airdrop",
    "公链", "layer2", "l2", "rollup", "跨链", "bridge",
    "hyperliquid", "hype", "dydx", "uniswap", "aave", "lido",
    "binance", "币安", "okx", "bybit", "coinbase",
    "pump.fun", "pumpfun", "moonshot",
    "质押", "staking", "restaking", "再质押",
    "合约爆仓", "liquidation",
    "代币", "token",
]

AI_KEYWORDS = [
    "ai", "人工智能", "模型", "openai", "kimi", "gpt", "claude",
    "anthropic", "llm", "大模型", "deepseek", "groq", "llama",
    "mistral", "gemini", "谷歌", "nvidia", "英伟达", "马斯克",
    "spacex", "agent", "智能体",
]
STOCK_KEYWORDS = [
    "股", "a股", "港股", "美股", "上证", "深证", "创业板",
    "宏观", "fed", "美联储", "cpi", "利率", "通胀", "就业",
    "美债", "美元", "指数", "反垄断", "ipo", "上市",
    "苹果", "谷歌", "微软", "amazon", "meta", "特斯拉",
    "nvidia", "财报", "央行", "地缘", "制裁",
]


def classify_item(title: str, content: str) -> tuple:
    """赛道标注: 返回 (topic, emoji), 非 BTC 加密返回 (None, None) 屏蔽。"""
    text = (title + " " + content).lower()
    btc_w = sum(2 for kw in BTC_SPECIFIC_KEYWORDS if kw in text)
    block_w = sum(1 for kw in BLOCK_KEYWORDS if kw in text)
    ai_w = sum(1 for kw in AI_KEYWORDS if kw in text)
    stock_w = sum(1 for kw in STOCK_KEYWORDS if kw in text)

    if block_w > 0 and btc_w == 0:
        return None, None

    if btc_w > 0 and btc_w >= ai_w:
        return Topics.BTC, "🔗"
    elif ai_w > 0 and ai_w >= stock_w:
        return Topics.AI, "🤖"
    elif stock_w > 0:
        return Topics.STOCK, "📈"
    return Topics.STOCK, "📈"


def fetch_json(url: str) -> dict | None:
    req = urllib.request.Request(url, headers={"api-key": _API_KEY})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
            data = json.loads(resp.read().decode())
            if data.get("status") == 0:
                return data
            return None
    except Exception as e:
        sys.stderr.write(f"[blockbeats] fetch failed: {e}\n")
        return None


def fetch_newsflash(endpoint: str, size: int = 10) -> list:
    url = f"{BASE_URL}{endpoint}?size={size}"
    resp = fetch_json(url)
    if resp is None:
        return []
    data = resp.get("data", {})
    items = data.get("data", []) if isinstance(data, dict) else data
    return items


def fetch_data(endpoint: str) -> dict | list | None:
    url = BASE_URL + endpoint
    data = fetch_json(url)
    if data is None:
        return None
    return data.get("data")


def strip_html(text: str) -> str:
    return re.sub(r"<[^>]+>", "", text).strip()


def extract_content(item: dict) -> str:
    raw = item.get("content", "") or ""
    plain = strip_html(raw)
    plain = re.sub(
        r"^BlockBeats\s*(?:消息)?[，,]\s*"
        r"(?:\S+\s+\d+\s*[日月]\s*\d+[日号]?[，,]\s*)?"
        r"(?:据\s*)?", "", plain
    )
    if len(plain) > 120:
        plain = plain[:120] + "…"
    return plain


def format_item(item: dict) -> dict | None:
    title = item.get("title", "").strip()
    if not title:
        return None
    content = extract_content(item)
    create_time = item.get("create_time", "")
    t_short = ""
    if create_time and " " in create_time:
        t_short = create_time.split()[1][:5]
    topic, emoji = classify_item(title, content)
    if topic is None:
        return None
    return {
        "topic": topic,
        "emoji": emoji,
        "time": t_short,
        "title": title,
        "content": content,
        "id": item.get("id"),
    }


def fetch_and_classify() -> dict:
    """拉取 + 分类 + 去重，返回 {topic: [item_dict, ...]}"""
    items_important = fetch_newsflash("/v1/newsflash/important", size=10)
    items_onchain = fetch_newsflash("/v1/newsflash/onchain", size=8)
    items_ai = fetch_newsflash("/v1/newsflash/ai", size=8)
    items_original = fetch_newsflash("/v1/newsflash/original", size=6)

    seen_ids = set()
    buckets = defaultdict(list)

    for item in items_important + items_onchain + items_ai + items_original:
        fmt = format_item(item)
        if fmt is None or fmt["id"] in seen_ids:
            continue
        seen_ids.add(fmt["id"])
        buckets[fmt["topic"]].append(fmt)

    for k in buckets:
        buckets[k].sort(key=lambda x: x["time"], reverse=True)

    return buckets


def load_llm_config():
    """从环境/ news .env 读取 LLM 配置。"""
    global _LLM_KEY, _LLM_URL, _LLM_MODEL
    # 环境变量优先
    _LLM_KEY = os.environ.get("DEEPSEEK_API_KEY") or os.environ.get("LLM_API_KEY", "")
    _LLM_URL = os.environ.get("LLM_API_URL", _LLM_URL)
    _LLM_MODEL = os.environ.get("LLM_MODEL", _LLM_MODEL)
    if _LLM_KEY:
        return
    # 回退：读 news .env
    env_paths = [
        os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".env"),
        os.path.expanduser("~/nimbus-os/news/.env"),
    ]
    for env_path in env_paths:
        try:
            with open(env_path) as f:
                for line in f:
                    line = line.strip()
                    if line.startswith("DEEPSEEK_API_KEY="):
                        _LLM_KEY = line.split("=", 1)[1].strip().strip('"').strip("'")
                        return
        except FileNotFoundError:
            continue


def call_deepseek(prompt: str) -> str | None:
    """调用 DeepSeek API，返回 AI 总结文本，失败返回 None。首次失败重试 1 次(偶发限流/抖动)。"""
    if not _LLM_KEY:
        sys.stderr.write("[blockbeats] LLM not configured\n")
        return None

    # max_tokens 8000: deepseek-v4-flash 为推理模型,思考(reasoning_content)先消耗
    # tokens 且长度不稳定(实测 54~数千);4000 时思考一长输出即被截断为空
    # (finish_reason=length, content="")。8000 给思考留足余量。
    payload = json.dumps({
        "model": _LLM_MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0.6,
        "max_tokens": 8000,
    }, ensure_ascii=False).encode()

    for attempt in range(2):
        req = urllib.request.Request(_LLM_URL, data=payload, headers={
            "Authorization": f"Bearer {_LLM_KEY}",
            "Content-Type": "application/json",
        })
        try:
            with urllib.request.urlopen(req, timeout=_LLM_TIMEOUT) as resp:
                data = json.loads(resp.read().decode())
                return data["choices"][0]["message"]["content"].strip()
        except Exception as e:
            sys.stderr.write(f"[blockbeats] LLM call failed (attempt {attempt + 1}/2): {e}\n")
            if attempt == 0:
                time.sleep(2)
    return None


def build_prompt(mode: str, buckets: dict, extra_data: str = "") -> str:
    """构建送给 AI 的 prompt。"""
    section_labels = {
        Topics.BTC: ("🔗 BTC", "比特币/加密市场"),
        Topics.AI: ("🤖 AI", "人工智能"),
        Topics.STOCK: ("📈 股票宏观", "股票/宏观/地缘"),
    }

    parts = []
    for topic, (label, desc) in section_labels.items():
        items = buckets.get(topic, [])
        if not items:
            continue
        parts.append(f"## {desc}")
        for it in items[:10]:
            line = f"- [{it['time']}] {it['title']}"
            if it["content"]:
                line += f" | {it['content']}"
            parts.append(line)

    raw_items = "\n".join(parts)

    period = "早报" if mode == "morning" else "晚报"

    prompt = f"""你是专业财经编辑。以下是一组 BlockBeats 快讯，请提炼为一份简洁的 {period}，直接用于 Discord 推送。

## 格式要求
- 三个板块各用 3-5 个要点概括核心内容
- 每个要点一句话，突出关键数据、趋势、标的
- 不要逐条翻译，提炼主题和叙事线索
- 关键数字用 **粗体**
- 板块标题: **🔗 BTC** / **🤖 AI** / **📈 股票宏观**
- 总字数 ≤500 字
- 不要加 "总结" "以下是" 等引导语，直接出正文

## 原始快讯
{raw_items}
"""
    if extra_data:
        prompt += f"\n## 补充数据\n{extra_data}\n"

    return prompt


def render(mode: str) -> str:
    """拉取 → AI 总结 → 输出。AI 失败则回退为格式化原文。"""
    now = datetime.now(timezone.utc)
    time_str = now.strftime("%Y-%m-%d %H:%M UTC")

    buckets = fetch_and_classify()

    # 晚报额外拉取市场数据
    extra_data = ""
    if mode == "evening":
        data_lines = []
        btc_etf = fetch_data("/v1/data/btc_etf")
        if btc_etf and isinstance(btc_etf, list) and len(btc_etf) > 0:
            latest = btc_etf[-1]
            inflow = float(latest.get("day_net_inflow_million", 0))
            total = float(latest.get("total_net_inflow_million", 0))
            icon = "🟢" if inflow >= 0 else "🔴"
            data_lines.append(f"BTC ETF 净流入: {icon} {inflow:+.1f}M (累计 {total:,.0f}M)")
        stablecoin = fetch_data("/v1/data/stablecoin_marketcap")
        if stablecoin and isinstance(stablecoin, dict):
            for k in ("usdt", "usdc"):
                records = stablecoin.get(k, [])
                if records:
                    cap = float(records[-1].get("market_cap", 0)) / 1e9
                    data_lines.append(f"{k.upper()} 市值: ${cap:.1f}B")
        if data_lines:
            extra_data = "\n".join(data_lines)

    # 是否有内容
    total_items = sum(len(v) for v in buckets.values())
    if total_items == 0:
        header = "🌅" if mode == "morning" else "🌆"
        label = "早报" if mode == "morning" else "晚报"
        return f"{header} BlockBeats {label} · BTC · AI · 股票\n{time_str}\n\n（暂无新快讯）"

    # 尝试 AI 总结
    load_llm_config()
    prompt = build_prompt(mode, buckets, extra_data)
    ai_summary = call_deepseek(prompt)

    if ai_summary:
        header = "🌅" if mode == "morning" else "🌆"
        label = "早报" if mode == "morning" else "晚报"
        return f"{header} BlockBeats {label} · BTC · AI · 股票\n{time_str}\n\n{ai_summary}"

    # AI 失败 → 回退格式化原文
    sys.stderr.write("[blockbeats] AI failed, falling back to raw format\n")
    return _fallback_render(mode, buckets, extra_data, now)


def _fallback_render(mode: str, buckets: dict, extra_data: str, now: datetime) -> str:
    """AI 不可用时的兜底：格式化原文输出。"""
    b = []
    header = "🌅" if mode == "morning" else "🌆"
    label = "早报" if mode == "morning" else "晚报"
    b.append(f"{header} BlockBeats {label} · BTC · AI · 股票")
    b.append(now.strftime("%Y-%m-%d %H:%M UTC"))

    def build(items, label, max_n=10):
        lines = [f"**{label}**"]
        for i, it in enumerate(items):
            if i >= max_n:
                break
            t = f"**{it['time']}** {it['emoji']} {it['title']}"
            if it["content"]:
                t += f" — {it['content']}"
            lines.append(t)
        if not items:
            lines.append("（暂无）")
        return lines

    if extra_data and mode == "evening":
        b.append("")
        b.append("**📊 市场数据**")
        for line in extra_data.strip().split("\n"):
            b.append(f"  {line}")

    b.append("")
    b.extend(build(buckets.get(Topics.BTC, []), "🔗 BTC"))
    b.append("")
    b.extend(build(buckets.get(Topics.AI, []), "🤖 AI"))
    b.append("")
    b.extend(build(buckets.get(Topics.STOCK, []), "📈 股票 · 宏观"))
    return "\n".join(b)


def main():
    parser = argparse.ArgumentParser(description="BlockBeats 日报 (AI 总结)")
    parser.add_argument("--mode", choices=["morning", "evening"], required=True)
    parser.add_argument("--api-key", help="BlockBeats API key")
    parser.add_argument("--json-only", action="store_true", help="输出 JSON")
    parser.add_argument("--raw", action="store_true", help="跳过 AI，直接出原文")
    args = parser.parse_args()

    global _API_KEY
    _API_KEY = args.api_key or os.environ.get("BLOCKBEATS_API_KEY") or os.environ.get("BBP_API_KEY", "")
    if not _API_KEY:
        print("错误: 请设置 BLOCKBEATS_API_KEY 环境变量或 --api-key", file=sys.stderr)
        sys.exit(1)

    # --raw 模式跳过 AI
    if args.raw:
        buckets = fetch_and_classify()
        now = datetime.now(timezone.utc)
        extra_data = ""
        if args.mode == "evening":
            extra_data = ""  # simplified
        report = _fallback_render(args.mode, buckets, extra_data, now)
    else:
        report = render(args.mode)

    if args.json_only:
        label = "早报" if args.mode == "morning" else "晚报"
        print(json.dumps({
            "title": f"BlockBeats {label}",
            "content": report,
            "time": datetime.now(timezone.utc).isoformat(),
        }, ensure_ascii=False))
    else:
        print(report)


if __name__ == "__main__":
    main()
