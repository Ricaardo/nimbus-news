# nimbusd 集成候选

`cmd/nimbusd` 是 Go-first 合并方案的 shadow candidate，同时托管 news、data plane、control store 和只读 SignalEvent 查询/渲染。它不是现网切换入口，不修改 launchd，也不会启用 Discord、微信、飞书或 Telegram 渠道。候选目录必须先通过显式运维初始化创建严格的 `0600` `.nimbus-candidate-root` v1 marker；进程启动绝不会自动创建 marker。

候选没有网络地址和数据路径默认值；必须逐项传 flag 或对应的 `NIMBUSD_*` 环境变量。`--candidate-root` 必须是预先创建的隔离目录，news DB、control DB、shadow feed 和 signal store 必须是该目录内互不相同的文件；父目录 symlink 逃逸会被拒绝。`docs/ports.yaml` 登记的 active 和 retired 端口全部不可使用。新闻只写独立的 `--shadow-feed`，SignalEvent 只读取独立的 `--signal-store`，API 的修改配置类请求返回 `405`。

示例（仅本机候选端口）：

```bash
mkdir -p ./data/candidate
go run ./cmd/nimbusd --init-candidate-root ./data/candidate
go run ./cmd/nimbusd \
  --config ./config.platform.yaml \
  --candidate-root ./data/candidate \
  --news-addr 127.0.0.1:18081 \
  --news-store ./data/candidate/news.db \
  --shadow-feed ./data/candidate/breaking.jsonl \
  --signal-store ./data/candidate/signals.jsonl \
  --data-addr 127.0.0.1:18821 \
  --dataplane-addr 127.0.0.1:18800 \
  --control-db ./data/candidate/control.db
```

Signal shadow endpoints 与旧 Python gateway 的 query/render 形状兼容：`GET /api/signals/query`、`GET /api/signals/render/news_push`、`GET /api/signals/render/screener_factor`、`GET /api/signals/render/nimbus_skill`。候选不暴露 fetch，因此不会双抓或双推。

## Warehouse prepared candidate

Warehouse 默认完全关闭。只有同时显式提供 `--warehouse-root` 与 `--warehouse-manifest`（或对应 `NIMBUSD_WAREHOUSE_*` 环境变量），并且两个路径都位于 `--candidate-root` 内时，进程才会读取并校验指定的 staging manifest；它不会扫描目录、复制、发布或提供查询。`GET /api/candidate/warehouse/status` 返回候选状态，其中 `serving` 与 `production_owner` 固定为 `false`。

离线 producer 与原子 publisher 的契约见顶层 `docs/contracts/warehouse-staging-v1.md`。publisher 必须由后续显式运维命令触发；当前 `nimbusd` 不调用它，也不接管现网 `warehouse-svc:8814`。
