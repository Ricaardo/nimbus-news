#!/bin/bash
# ============================================================
# VPS 部署同步脚本（本地 Mac 运行）
#   1. 交叉编译 Linux amd64 platform 二进制
#   2. 同步二进制 + scripts/ + deploy/ + config 到 VPS
#   3. 安装 VPS 运维 cron（数据备份 7 天、外部探活→企业微信）
#   4. systemctl restart news-platform + 健康检查
#
# 用法:
#   ./deploy/sync-vps.sh          # 全量部署
#   ./deploy/sync-vps.sh --skip-build   # 跳过编译（只同步+重启）
#   ./deploy/sync-vps.sh --skip-restart # 同步但重启服务
#
# 注意: 直接运行本脚本，不要用管道包裹（`sync-vps.sh | tail` 会吞退出码，
#   失败会误报成功——2026-08-16 部署踩过：二进制传坏导致 SEGV 崩溃）。
# ============================================================
set -euo pipefail

VPS_HOST="${VPS_HOST:-root@45.77.26.44}"
REMOTE_DIR="/opt/news"
SSH="ssh -o ConnectTimeout=10 $VPS_HOST"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SKIP_BUILD=0
SKIP_RESTART=0
for arg in "$@"; do
  case "$arg" in
    --skip-build) SKIP_BUILD=1 ;;
    --skip-restart) SKIP_RESTART=1 ;;
  esac
done

# ---------- 1. 交叉编译 ----------
if [ "$SKIP_BUILD" -eq 0 ]; then
  echo ">>> 交叉编译 Linux amd64 platform..."
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/platform-linux-amd64 ./cmd/platform
else
  echo ">>> 跳过编译（使用现有 bin/platform-linux-amd64）"
fi
test -f bin/platform-linux-amd64 || { echo "ERROR: bin/platform-linux-amd64 不存在"; exit 1; }

# ---------- 2. 同步文件 ----------
echo ">>> 同步到 $VPS_HOST:$REMOTE_DIR ..."
$SSH "mkdir -p $REMOTE_DIR/{bin,scripts,deploy,data,logs,venv}"

# 二进制走 gzip 传输（101MB→37MB，VPS 线路不稳时更抗中断），
# 传完后在 VPS 上解压并校验 SHA256，防止坏文件上线（SEGV 事故教训）
gzip -kf bin/platform-linux-amd64
scp -q -o ConnectTimeout=15 bin/platform-linux-amd64.gz "$VPS_HOST:$REMOTE_DIR/bin/platform.new.gz"
LOCAL_SHA="$(shasum -a 256 bin/platform-linux-amd64 | awk '{print $1}')"
$SSH "cd $REMOTE_DIR/bin && gunzip -f platform.new.gz && \
  test \"\$(sha256sum platform.new | awk '{print \$1}')\" = \"$LOCAL_SHA\" \
  && echo 'binary sha256 OK' || { echo 'binary sha256 MISMATCH'; exit 1; }"

scp -q -r scripts/*.py "$VPS_HOST:$REMOTE_DIR/scripts/"
scp -q deploy/launch-vps.sh deploy/news-platform.service "$VPS_HOST:$REMOTE_DIR/deploy/"

# config 含密钥，仅本地持有；同步失败（如文件缺失）即中止，不留半套
scp -q config.platform.vps.yaml "$VPS_HOST:$REMOTE_DIR/config.platform.vps.yaml"
scp -q deploy/health-probe.sh "$VPS_HOST:$REMOTE_DIR/deploy/health-probe.sh"

# .env 真源在 VPS（本地不保存密钥）；本地有同名文件时才覆盖
if [ -f .env ]; then
  scp -q .env "$VPS_HOST:$REMOTE_DIR/.env"
else
  echo ">>> 本地无 .env，保留 VPS 现有 $REMOTE_DIR/.env（密钥真源）"
fi

$SSH "chmod +x $REMOTE_DIR/deploy/launch-vps.sh $REMOTE_DIR/deploy/health-probe.sh"

# ---------- 3. 安装运维 cron（幂等） ----------
echo ">>> 安装 VPS 运维 cron..."
$SSH "bash -s" <<'REMOTE'
set -e
REMOTE_DIR=/opt/news
CRON_BACKUP='15 3 * * * cd /opt/news && tar czf backups/platform.$(date +\%Y\%m\%d).db.tgz data/platform.db && find backups -name "platform.*.db.tgz" -mtime +7 -delete'
CRON_PROBE='*/5 * * * * /opt/news/deploy/health-probe.sh >> /opt/news/logs/health-probe.log 2>&1'
mkdir -p "$REMOTE_DIR/backups"
# 去重后写回（保留既有自定义条目）
(crontab -l 2>/dev/null | grep -v -e 'news/platform.db' -e 'health-probe.sh' || true
 echo "$CRON_BACKUP"
 echo "$CRON_PROBE") | crontab -
echo "crontab installed:"
crontab -l | grep -E 'platform.db|health-probe'
REMOTE

# ---------- 4. 原子替换二进制 + 重启 ----------
if [ "$SKIP_RESTART" -eq 0 ]; then
  echo ">>> 重启 news-platform..."
  $SSH "mv -f $REMOTE_DIR/bin/platform.new $REMOTE_DIR/bin/platform && systemctl restart news-platform && sleep 3 && systemctl is-active news-platform"
  echo ">>> 健康检查..."
  sleep 2
  $SSH "curl -sf -m 5 http://127.0.0.1:8081/api/health && echo && echo HEALTH-OK || echo HEALTH-FAILED"
else
  echo ">>> 跳过重启（--skip-restart）"
fi

echo ">>> 完成。平台日志: $VPS_HOST:$REMOTE_DIR/logs/platform.out.log"
