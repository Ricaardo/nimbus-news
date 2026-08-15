# 部署说明（当前架构：VPS 自构建，push 即部署，2026-08-16 起）

## 拓扑

- **代码仓**：本地 `~/nimbus-news`（唯一 git 仓库，两仓制：nimbus-os 持 datasources/guanfu 模块，经 go.mod replace 引用）
- **运行机**：VPS（`root@45.77.26.44`），部署目录 `/opt/news`
- **服务**：systemd `news-platform.service`（`Type=simple`，ExecStart=`deploy/launch-vps.sh`，TZ=Asia/Shanghai）
- **进程**：`./bin/platform -config config.platform.vps.yaml`，API 仅监听 127.0.0.1:8081

VPS 关键目录：

```
/opt/news/
├── bin/platform              # 部署二进制（VPS 本地构建，owner root）
├── scripts/ → src/nimbus-news/scripts   # git 软链，随源码自动更新
├── deploy/  → src/nimbus-news/deploy    # git 软链（脚本/服务文件）
├── src/nimbus-news/          # git 源码仓（deploy key 认证）
├── src/nimbus-os/            # git 源码仓（datasources/guanfu 模块）
├── venv/                     # python + akshare/ecocal（脚本源依赖）
├── config.platform.vps.yaml  # 含密钥，gitignored，本地为真源
├── .env                      # 密钥真源（不提交、本地无副本）
├── data/ logs/ backups/ tmp/ # 运行数据/日志/备份/构建临时目录
└── .deployed-commit          # 当前已部署的 commit hash
```

## 部署（push 即部署）

```bash
cd ~/nimbus-news
git push origin main          # 唯一部署动作
```

VPS cron 每 5 分钟跑 `deploy/vps-build-deploy.sh`：

1. `git fetch` 对比 `.deployed-commit`，无新提交直接退出
2. 有更新 → `git pull` → `go build`（`GOTMPDIR=/opt/news/tmp`，避免 /tmp tmpfs 不足）
3. 原子替换 `bin/platform.new → platform` + `systemctl restart`
4. 轮询 `/api/health` 最多 60s；失败自动回滚旧二进制
5. 成功则更新 `.deployed-commit`

构建环境：VPS 装 Go 1.26.3（/usr/local/go）；私有仓认证用两个 GitHub
deploy key（nimbus-news / nimbus-os 各一），经 `~/.ssh/config` 的
`github-news` / `github-os` alias 区分。

## 配置文件同步（本地为真源）

`config.platform.vps.yaml` 与 `.env` 含密钥且 gitignored，本地只有
`config.platform.vps.yaml`（`.env` 真源在 VPS）：

```bash
./deploy/sync-vps.sh            # 同步 config + 安装 cron（幂等）
./deploy/sync-vps.sh --via=scp  # 应急: 二进制直传（GitHub 网络差时）
```

> **严禁提交** config/*.yaml 与 .env。

## VPS 运维 cron

| 任务 | 触发 | 内容 |
|---|---|---|
| 自动构建部署 | 每 5 分钟 | `vps-build-deploy.sh`：新 commit → 构建 → 替换 → 重启 → 健康检查（失败回滚） |
| 数据备份 | 每日 03:15 | `tar czf backups/platform.$(date +%Y%m%d).db.tgz data/platform.db`，保留 7 天自动删除 |
| 外部探活 | 每 5 分钟 | `health-probe.sh`：curl `/api/health`，**连续 3 次**失败 → 企业微信 webhook 告警（独立于平台进程） |

手动操作：

```bash
ssh root@45.77.26.44
systemctl status news-platform           # 状态
tail -f /opt/news/logs/platform.out.log  # 平台日志
tail -f /opt/news/logs/build-deploy.log  # 构建部署日志
journalctl -u news-platform -e           # systemd 侧日志
crontab -l                               # 三条 cron（构建/备份/探活）
```

## 配置

- 运行配置唯一入口：`config.platform.vps.yaml`（渠道、源调度、脚本源参数）
- 密钥在 `.env`，`launch-vps.sh` 启动时 source 进环境；脚本源 subprocess 同样继承
- 模板：`config.platform.vps.yaml.example`（渠道默认关闭）
- 常用密钥：`WECHAT_WEBHOOK_URL`（企业微信，探活告警同用）、`DISCORD_PUSH_WEBHOOK`、
  `FINNHUB_API_KEY`（earnings-calendar）、`FRED_API_KEY`（宏观报告）、LLM key

数据源全览见 nimbus-os 仓 `docs/references/news-sources.md`（news 仓源码为准，文档随源变更在 nimbus-os 更新）。

## 本地开发

```bash
cd ~/nimbus-news
make python-deps          # venv 装 scripts 依赖（akshare/ecocal 等，见 scripts/requirements.txt）
make build-native         # 本地 go build ./cmd/platform
go run ./cmd/platform -config config.platform.yaml   # 本地起服务（8081）
```

本地 `config.platform.yaml` 同样 gitignored，自建一份（复制 example + vps 配置的渠道/密钥）。

## 数据与生成物（不提交）

- `data/*.db`（BoltDB 平台库、market.db）
- `bin/`、`venv/`、`logs/`、`backups/`、`scripts/__pycache__/`

备份/恢复：

```bash
# VPS 上
ls /opt/news/backups/                        # 每日 03:15 的 platform.*.db.tgz
# 恢复：停服务 → 解压覆盖 data/platform.db → 启动
systemctl stop news-platform
tar xzf backups/platform.20260816.db.tgz -C /opt/news   # 恢复备份当天数据
systemctl start news-platform
```

## Digest topics v4 显式迁移

该迁移不会随平台启动自动执行。默认命令是只读 dry-run，输出仅含计数、
守恒 hash、marker 状态和预计更新数，不包含新闻正文。

先停止平台后复制数据库，或使用平台 owner-mediated snapshot 生成已验证副本，再演练：

```bash
cp data/platform.db data/platform.topics-v4.rehearsal.db
go run ./cmd/digest-migrate \
  --db data/platform.topics-v4.rehearsal.db \
  --config config.platform.vps.yaml > /tmp/topics-v4-dry-run.json

BEFORE_HASH=$(jq -r .before_hash /tmp/topics-v4-dry-run.json)
go run ./cmd/digest-migrate \
  --db data/platform.topics-v4.rehearsal.db \
  --config config.platform.vps.yaml \
  --mode apply \
  --expected-before-hash "$BEFORE_HASH"
```

确认 `before_count == after_count`、`before_hash == after_hash`、
`applied == true` 和 `marker_present == true`。再次 apply 应返回
`already_marked == true` 且不改数据。

生产执行必须先停止平台并保留原库备份；不要对仍由平台持有的 BoltDB 执行：

```bash
cp data/platform.db data/platform.pre-topics-v4.db
go run ./cmd/digest-migrate --db data/platform.db --config config.platform.vps.yaml \
  > /tmp/topics-v4-production-dry-run.json
BEFORE_HASH=$(jq -r .before_hash /tmp/topics-v4-production-dry-run.json)
go run ./cmd/digest-migrate --db data/platform.db --config config.platform.vps.yaml \
  --mode apply --expected-before-hash "$BEFORE_HASH"
```

hash 不匹配时 apply 会在写入前失败。需要回滚时，保持平台停止并用
`data/platform.pre-topics-v4.db` 替换目标库，再启动和验收；不要尝试手工删除 marker。
