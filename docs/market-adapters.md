# 行情适配器（Market Adapters）

`internal/market` 通过一组**行情适配器**拉取报价和 K 线。每个适配器实现
`QuoteAdapter`（`GetQuote`）与可选的 `KLineAdapter`（`GetKLines`），按优先级注册，
服务层 `MarketService` 对每个标的**按优先级逐个尝试，首个成功即返回**，失败自动回退下一个。

## 优先级与分工

| 优先级 | 适配器 | 覆盖 | 说明 |
|---|---|---|---|
| 0 | **Longbridge** | 港股 / A股 / 美股 / 港股指数 | 持牌实时，质量最高，Go 壳调 Python longport |
| 1 | Futu | 港股 / A股 | Futu OpenD，本地 11111 端口 |
| 5 | Binance | 加密货币 | |
| 6 | OKX | 加密货币 | |
| 7 | AKShare | A股备用 | 龙虎榜/资金流等独家数据另走 news-dashboard |
| 10 | Yahoo | 兜底 | 美股/日/英/德指数、全球兜底 |

数值越小越优先。Longbridge 凭证缺失或拉取失败时，自动落到 Futu / AKShare / Yahoo，
**不会让整条链路报错**。

## Longbridge 适配器

Go 壳 `internal/market/adapters/longbridge.go` 通过 subprocess 调用
`scripts/longbridge_helper.py`（命令 `snapshot` / `kline`），与 Futu 适配器同一模式
（避开 Go SDK 的 cgo 风险，复用已验证的 Python longport）。

### 覆盖与符号格式

服务层 `SymbolResolver` 先把别名/中文名归一，再交给适配器。Longbridge 处理这些形态：

| 市场 | news 内部符号 | → Longbridge | 示例 |
|---|---|---|---|
| 港股 | `700.HK` / `00700.HK` | 原样（前导零都认） | 腾讯 700.HK |
| A股(沪) | `600519.SS` | `600519.SH` | 茅台 |
| A股(深) | `000001.SZ` | 原样 | 平安银行 |
| 美股 | 裸 ticker `AAPL` / `AAPL.US` | `AAPL.US` | 苹果 |
| 港股指数 | `^HSI` / `^HSTECH` / `^HSCE` | `HSI.HK` / `HSTECH.HK` / `HSCEI.HK` | 恒指/恒科/国企 |
| A股指数 | `000300.SS` 等 | `000300.SH` | 沪深300 |

**美股/日/英/德指数**（`^GSPC` `^IXIC` `^DJI` `^N225` `^FTSE` `^GDAXI` `^VIX` …）
Longbridge OpenAPI 不供 → 适配器 `CanHandle` 返回 false → 由 Yahoo 兜底。

> 报价的**名称**：longport `quote()` 不返 name，helper 顺带 best-effort 调
> `static_info()` 补中文名（个股+指数都有，如「腾讯控股」「恒生指数」）；失败则
> 名称回退为符号，不影响报价。

### K 线周期约定（⚠ 与 Futu 一致）

两个适配器可互换，周期键约定必须一致：

| key | 含义 |
|---|---|
| `1d` | 日线 |
| `1w` | 周线 |
| `1m` | **月线**（不是 1 分钟！与 Futu `K_MON` 一致） |
| `5m` `15m` `30m` `60m` | 分钟线 |
| `1min` | 1 分钟（Longbridge 专有，Futu 无） |

K 线统一用**前复权**（`AdjustType.ForwardAdjust`，对齐 Futu `request_history_kline` 的 QFQ 默认），
保证跨适配器技术指标连续。

### 不在适配器范围内（Item 2 边界）

适配器只做**报价 + K 线**（接口只需这两个方法）。Longbridge 更丰富的数据
——盘口/逐笔/分时、财务三表、估值、机构评级、资金流等——**走 Longbridge MCP**
（`mcp__longbridge__*` 工具，已挂在 nimbus），不塞进行情适配器。这是设计边界，不是缺口。

## 凭证与依赖

```env
# .env（不提交，模板见 .env.example）
LONGPORT_APP_KEY=...
LONGPORT_APP_SECRET=...
LONGPORT_ACCESS_TOKEN=...
```

- 申请：<https://open.longportapp.com/>
- Python 依赖：`longport`（在 `scripts/requirements.txt`，`make python-deps` 安装）
- 注入：`bin/launch-platform.sh` 启动时 `set -a; source .env`，凭证进 launchd 进程环境，
  Go 适配器经 `os.Getenv` 读取。三件套缺任一 → 静默让位。

## 运维

### Token 过期监控（最重要）

Longbridge **ACCESS_TOKEN 会过期**（默认约 90 天）。过期后适配器静默让位 Futu/AKShare
——属**隐性降级**，行情仍出但不再是 Longbridge 质量源，需主动监控：

```bash
make smoke-longbridge        # 或 bash scripts/longbridge_smoke.sh
```

四个标的（港/美/A/港指）全绿则链路正常；失败多半是 token 过期。
可挂 cron / launchd 定期跑，失败告警。重新签发 token 后更新 `.env` 的
`LONGPORT_ACCESS_TOKEN` 并重启服务。

### 排查

- **行情看着像降级了**：先 `make smoke-longbridge`。若失败 → token / 权限 / 网络。
- **某标的总走不到 Longbridge**：确认 `CanHandle` 接受该符号格式（见上表），
  以及 `SymbolResolver` 产出的是 `.SS/.SZ/.HK/.US` 形态而非 Futu 前缀式 `SH.600519`。
- **JSON 解析失败**：longport 是 Rust 扩展，启动时会向 fd 1 打一张 market-access 横幅表；
  `longbridge_helper.py` 已用 `os.dup2` 在 fd 级抑制（`silence_stdout`）。若改动该脚本，
  务必保留这层抑制，否则横幅污染 stdout。

## 测试

```bash
# 单元测试（纯逻辑：CanHandle / 符号转换 / 指数映射，无网络，随普通 CI 跑）
go test ./internal/market/adapters/ -run Longbridge

# 集成测试（实盘，需凭证 + 网络，build tag 隔离，凭证缺失自动 skip）
set -a && source .env && set +a
go test -tags=integration ./internal/market/adapters/ -run LongbridgeLive -v
```
