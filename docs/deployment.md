# 部署说明

## 首次初始化

```bash
make init
```

该命令会在本地创建：

- `config.yaml`
- `.env`
- `data/cache`
- `data/index`
- `data/reports`

`config.yaml` 和 `.env` 都是本地文件，不提交到 Git。

## 配置原则

运行配置只有一个入口：`config.yaml`。

模板文件 `config.yaml.example` 中的渠道默认关闭，生产部署时按需启用，并在 `.env` 中补齐对应密钥。服务启动时会展开 `${VAR}`。

常用环境变量：

```env
FRONTEND_PORT=80
BACKEND_API_PORT=8081
WEBHOOK_PORT=8080
WXOFFICIAL_PORT=8082
MINIMAX_API_KEY=
FEISHU_APP_ID=
FEISHU_APP_SECRET=
WECHAT_WEBHOOK_URL=
DISCORD_PUSH_WEBHOOK=
DISCORD_BOT_TOKEN=
FINNHUB_API_KEY=
FRED_API_KEY=
LONGPORT_APP_KEY=
LONGPORT_APP_SECRET=
LONGPORT_ACCESS_TOKEN=
```

> Longbridge 行情适配器（港股/A股/美股，优先级最高）需以上三件套，缺失则静默让位
> Futu/AKShare。详见 [行情适配器](market-adapters.md)。token 会过期（约 90 天），
> 用 `make smoke-longbridge` 监控。

## Docker 部署

```bash
make deploy
make health
```

服务拓扑：

- `backend`: Go 主服务，读取 `/app/config.yaml`，数据写入 `/app/data`
- `frontend`: Vue + Nginx，代理 `/api/` 到 `backend:8081/api/`

默认访问地址：

- 前端: `http://localhost:${FRONTEND_PORT:-80}`
- 后端 API: `http://localhost:${BACKEND_API_PORT:-8081}/api`
- API 健康检查: `http://localhost:${BACKEND_API_PORT:-8081}/api/health`
- 前端健康检查: `http://localhost:${FRONTEND_PORT:-80}/health`

常用命令：

```bash
make logs
make logs-backend
make logs-frontend
make restart
make down
make clean
```

## 本地开发

```bash
make init
make python-deps
make dev-backend
make dev-frontend
```

后端默认：

```bash
go run ./cmd/platform -config config.yaml
```

前端默认：

```bash
cd web
npm install
npm run dev
```

## 数据与生成物

以下路径为运行期生成物，不提交：

- `data/*.db`
- `data/cache/`
- `data/index/`
- `data/reports/`
- `bin/`
- `web/node_modules/`
- 根目录构建出的可执行文件

重新生成标的索引：

```bash
make symbols
```

备份数据：

```bash
make backup
```

## Digest topics v4 显式迁移

该迁移不会随平台启动自动执行。默认命令是只读 dry-run，输出仅含计数、
守恒 hash、marker 状态和预计更新数，不包含新闻正文。

先停止平台后复制数据库，或使用平台 owner-mediated snapshot 生成已验证副本，再演练：

```bash
cp data/platform.db data/platform.topics-v4.rehearsal.db
go run ./cmd/digest-migrate \
  --db data/platform.topics-v4.rehearsal.db \
  --config config.platform.yaml > /tmp/topics-v4-dry-run.json

BEFORE_HASH=$(jq -r .before_hash /tmp/topics-v4-dry-run.json)
go run ./cmd/digest-migrate \
  --db data/platform.topics-v4.rehearsal.db \
  --config config.platform.yaml \
  --mode apply \
  --expected-before-hash "$BEFORE_HASH"
```

确认 `before_count == after_count`、`before_hash == after_hash`、
`applied == true` 和 `marker_present == true`。再次 apply 应返回
`already_marked == true` 且不改数据。

生产执行必须先停止平台并保留原库备份；不要对仍由平台持有的 BoltDB 执行：

```bash
cp data/platform.db data/platform.pre-topics-v4.db
go run ./cmd/digest-migrate --db data/platform.db --config config.platform.yaml \
  > /tmp/topics-v4-production-dry-run.json
BEFORE_HASH=$(jq -r .before_hash /tmp/topics-v4-production-dry-run.json)
go run ./cmd/digest-migrate --db data/platform.db --config config.platform.yaml \
  --mode apply --expected-before-hash "$BEFORE_HASH"
```

hash 不匹配时 apply 会在写入前失败。需要回滚时，保持平台停止并用
`data/platform.pre-topics-v4.db` 替换目标库，再启动和验收；不要尝试手工删除 marker。
