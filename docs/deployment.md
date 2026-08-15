# 部署说明（当前架构：VPS systemd，2026-08 起）

## 拓扑

- **代码仓**：本地 `~/nimbus-news`（唯一 git 仓库，两仓制：nimbus-os 持 datasources/guanfu 模块，经 go.mod replace 引用）
- **运行机**：VPS（`root@45.77.26.44`），目录 `/opt/news`，**非 git 仓库**
- **服务**：systemd `news-platform.service`（`Type=simple`，ExecStart=`deploy/launch-vps.sh`，TZ=Asia/Shanghai）
- **进程**：`./bin/platform -config config.platform.vps.yaml`，API 仅监听 127.0.0.1:8081

VPS 需要的可执行环境：`bin/platform`（本地交叉编译的 Linux amd64 ELF）、
`venv/`（python3 + akshare/ecocal 等，PATH 由 launch-vps.sh 前置）、
`scripts/*.py`（script 源）、`config.platform.vps.yaml`、`.env`（密钥）。

## 部署（一条命令）

```bash
cd ~/nimbus-news
./deploy/sync-vps.sh          # 编译 + 同步 + 安装 cron + 重启 + 健康检查
./deploy/sync-vps.sh --skip-build    # 只同步+重启（代码没动时）
./deploy/sync-vps.sh --skip-restart # 只同步不重启
```

脚本幂等地做四件事：

1. `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 交叉编译 `bin/platform-linux-amd64`
2. scp 同步：二进制（原子替换）、`scripts/*.py`、`deploy/`、`config.platform.vps.yaml`、`.env`
3. 安装/刷新 VPS cron（见下）
4. `systemctl restart news-platform` + `curl /api/health` 验证

> `config.platform.vps.yaml` 与 `.env` 含密钥且 gitignored，只存在于本地与 VPS；
> **严禁提交**。VPS 上的这两个文件被 scp 覆盖，本地即为真源。

## VPS 运维（sync-vps.sh 自动安装，幂等）

| 任务 | 触发 | 内容 |
|---|---|---|
| 数据备份 | 每日 03:15 | `tar czf backups/platform.$(date +%Y%m%d).db.tgz data/platform.db`，保留 7 天自动删除 |
| 外部探活 | 每 5 分钟 | `deploy/health-probe.sh`：curl `/api/health`，**连续 3 次**失败 → 企业微信 webhook 告警（独立于平台进程，平台挂了它照样跑） |

手动操作：

```bash
ssh root@45.77.26.44
systemctl status news-platform          # 状态
tail -f /opt/news/logs/platform.out.log # 平台日志
journalctl -u news-platform -e          # systemd 侧日志
crontab -l                              # 查看备份/探活 cron
```

## 配置

- 运行配置唯一入口：`config.platform.vps.yaml`（渠道、源调度、脚本源参数）
- 密钥在 `.env`，`launch-vps.sh` 启动时 source 进环境；脚本源 subprocess 同样继承
- 模板：`config.platform.vps.yaml.example`（渠道默认关闭）
- 常用密钥：`WECHAT_WEBHOOK_URL`（企业微信，探活告警同用）、`DISCORD_PUSH_WEBHOOK`、
  `FINNHUB_API_KEY`（earnings-calendar）、`FRED_API_KEY`（宏观报告）、LLM key

数据源全览见 [SOURCES.md](SOURCES.md)。

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
