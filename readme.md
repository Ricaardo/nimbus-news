# News-Fetcher 投资信息平台

News-Fetcher 是一个 Go + Vue 的投资信息聚合平台，负责拉取市场新闻、A 股聚合报告、行情快照和 LLM 点评，并通过企业微信、Discord、Telegram 等渠道推送。

## 当前结构

```text
cmd/platform/              # 唯一服务入口
internal/source/           # 数据源与聚合报告
internal/channel/          # 推送渠道
internal/market/           # 行情适配器与缓存
internal/api/              # 管理后台 API
internal/config/           # YAML 配置加载与热更新
scripts/                   # A 股聚合报告依赖的 Python 脚本
web/                       # Vue 管理后台
docs/                      # 部署、规划和维护文档
```

## 配置约定

平台配置分为可提交模板和本地运行文件：

- `config.platform.yaml.example`: 当前 15 源/4 渠道生产拓扑的无密钥模板
- `config.platform.yaml`: launchd/裸机生产运行配置，已加入 `.gitignore`
- `config.yaml.example`: Docker/旧开发入口的兼容模板
- `config.yaml`: Docker/旧开发运行配置，已加入 `.gitignore`
- `.env.example`: 可提交的环境变量模板
- `.env`: 本地/生产密钥和端口配置，已加入 `.gitignore`

launchd/裸机生产首次部署：

```bash
cp config.platform.yaml.example config.platform.yaml
cp .env.example .env
```

然后只在 `.env` 或 launchd 环境中填写密钥。`config.platform.yaml` 保留
`${VAR}` 引用，不要把 webhook、token 或 API key 写成明文。服务启动时会展开这些
引用；配置管理 API 的无关更新会继续保留磁盘上的环境变量占位符。

未配置 `routing` 的源继续使用 legacy `delivery_mode` / `briefing_target`；新配置应优先使用
typed `routing`，不要在同一个源上混用两套字段。BlockBeats 脚本仅从
`BLOCKBEATS_API_KEY` 环境变量读取凭据。

修改 plist 的 `EnvironmentVariables` 后，`kickstart -k` 仍可能沿用旧环境，必须重新注册：

```bash
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.news.platform.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.news.platform.plist
```

仅重新构建二进制，或由 `launch-platform.sh` 重新读取 `.env` 时，才使用
`launchctl kickstart -k gui/$(id -u)/com.news.platform`。

Docker 开发仍可运行 `make init`，它会从兼容模板生成 `config.yaml`。

## 本地开发

```bash
make init
make dev-backend
make dev-frontend
```

默认地址：

- 后端 API: `http://localhost:8081/api`
- 前端管理后台: `http://localhost:3000`

直接运行后端：

```bash
go run ./cmd/platform -config config.platform.yaml
```

## Docker 部署

```bash
make init
make deploy
make health
```

部署入口统一为 `Makefile` + `docker-compose.yml`：

- `make deploy`: 构建并启动后端和前端
- `make logs`: 查看容器日志
- `make restart`: 重启服务
- `make down`: 停止服务
- `make clean`: 清理本项目构建镜像，保留数据

默认端口可在 `.env` 覆盖：

```env
FRONTEND_PORT=80
BACKEND_API_PORT=8081
WEBHOOK_PORT=8080
WXOFFICIAL_PORT=8082
```

生产数据写入 `data/`，前端依赖写入 `web/node_modules/`，这些生成物不会再提交到 Git。

## Python 脚本依赖

A 股聚合报告会调用 `scripts/` 下的 Python 脚本。裸机运行时先安装依赖：

```bash
make python-deps
```

标的搜索索引由脚本生成，不提交生成物：

```bash
make symbols
```

## 常用命令

```bash
make help
make build
make up
make logs-backend
make logs-frontend
make backup
```

## 文档入口

- [部署说明](docs/deployment.md)
- [文档索引](docs/README.md)
- [路线图](docs/roadmap.md)
- [A 股基准数据底座规划](docs/baseline-design.md)
