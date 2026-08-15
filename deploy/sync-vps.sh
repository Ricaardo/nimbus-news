#!/bin/bash
# ============================================================
# VPS 部署同步脚本（本地 Mac 运行）
#
# 【主部署流程已改为 VPS 自构建】: 本地 push 源码 → VPS cron 每 5 分钟
#   检测新 commit → vps-build-deploy.sh 构建 + 原子替换 + 重启 + 健康检查
#   （失败自动回滚）。scripts/ deploy/ 走 git 软链，无需同步。
#   本脚本仅剩两个职责:
#   1. 同步 config.platform.vps.yaml（gitignored 含密钥，本地为真源）
#   2. 可选 .env 覆盖 + 应急通道（--via=scp 直传二进制 / 手动触发远程构建）
#
# 用法:
#   ./deploy/sync-vps.sh                  # 同步 config + .env + cron 安装
#   ./deploy/sync-vps.sh --via=scp        # 应急: 二进制直传 VPS（GitHub 网络差时）
#   ./deploy/sync-vps.sh --skip-restart   # 只同步不重启
#
# 注意: 直接运行本脚本，不要用管道包裹（`sync-vps.sh | tail` 会吞退出码，
#   失败会误报成功——2026-08-16 部署踩过：二进制传坏导致 SEGV 崩溃）。
# ============================================================
set -euo pipefail

VPS_HOST="${VPS_HOST:-root@45.77.26.44}"
REMOTE_DIR="/opt/news"
REPO="Ricaardo/nimbus-news"
RELEASE_TAG="deploy"
ASSET="platform-linux-amd64.gz"
SSH="ssh -o ConnectTimeout=10 $VPS_HOST"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SKIP_BUILD=0
SKIP_RESTART=0
VIA="release"   # release=GitHub Release 下载 | scp=直传 VPS
for arg in "$@"; do
  case "$arg" in
    --skip-build) SKIP_BUILD=1 ;;
    --skip-restart) SKIP_RESTART=1 ;;
    --via=scp) VIA="scp" ;;
    --via=release) VIA="release" ;;
  esac
done

# ---------- 1. 交叉编译 + 发布到 GitHub Release ----------
if [ "$SKIP_BUILD" -eq 0 ]; then
  echo ">>> 交叉编译 Linux amd64 platform..."
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/platform-linux-amd64 ./cmd/platform
else
  echo ">>> 跳过编译（使用现有 bin/platform-linux-amd64）"
fi
test -f bin/platform-linux-amd64 || { echo "ERROR: bin/platform-linux-amd64 不存在"; exit 1; }

gzip -kf bin/platform-linux-amd64
LOCAL_SHA="$(shasum -a 256 bin/platform-linux-amd64 | awk '{print $1}')"

# ---------- 2. 二进制通道 ----------
if [ "$VIA" = "release" ]; then
  echo ">>> 发布 $ASSET 到 GitHub Release $RELEASE_TAG ..."
  gh release create "$RELEASE_TAG" --repo "$REPO" --title "deploy" \
    --notes "deploy build $(date '+%F %T')" --target main 2>/dev/null || true
  gh release upload "$RELEASE_TAG" --repo "$REPO" "bin/$ASSET" --clobber
else
  echo ">>> 二进制走 scp（--via=scp）..."
  scp -q -o ConnectTimeout=15 "bin/$ASSET" "$VPS_HOST:$REMOTE_DIR/bin/$ASSET"
  $SSH "cd $REMOTE_DIR/bin && gunzip -f $ASSET && mv -f ${ASSET%.gz} platform.new && \
    test \"\$(sha256sum platform.new | awk '{print \$1}')\" = \"$LOCAL_SHA\" \
    && echo 'binary sha256 OK' || { echo 'binary sha256 MISMATCH'; exit 1; }"
fi

# ---------- 3. 同步小文件 ----------
echo ">>> 同步到 $VPS_HOST:$REMOTE_DIR ..."
$SSH "mkdir -p $REMOTE_DIR/{bin,scripts,deploy,data,logs,venv}"

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

# ---------- 4. 安装运维 cron（幂等） ----------
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

# ---------- 5. VPS 获取二进制 + 校验 + 重启 ----------
if [ "$VIA" = "release" ]; then
  if [ "$SKIP_RESTART" -eq 0 ]; then
    echo ">>> VPS 从 GitHub CDN 下载 $ASSET 并校验 sha256..."
    $SSH "bash -s" "$REPO" "$RELEASE_TAG" "$ASSET" "$LOCAL_SHA" "$REMOTE_DIR" <<'REMOTE'
set -euo pipefail
REPO="$1"; RELEASE_TAG="$2"; ASSET="$3"; LOCAL_SHA="$4"; REMOTE_DIR="$5"
set -a; source "$REMOTE_DIR/.env"; set +a
: "${GH_DEPLOY_TOKEN:?GH_DEPLOY_TOKEN 未设置（VPS .env）}"
echo ">>> 下载 https://github.com/$REPO/releases/download/$RELEASE_TAG/$ASSET ..."
curl -sfL -m 600 -H "Authorization: Bearer $GH_DEPLOY_TOKEN" \
  -o "$REMOTE_DIR/bin/$ASSET" \
  "https://github.com/$REPO/releases/download/$RELEASE_TAG/$ASSET"
cd "$REMOTE_DIR/bin" && gunzip -f "$ASSET" && mv -f "${ASSET%.gz}" platform.new
VPS_SHA="$(sha256sum platform.new | awk '{print $1}')"
echo "sha256 vps=$VPS_SHA local=$LOCAL_SHA"
[ "$VPS_SHA" = "$LOCAL_SHA" ] || { echo "ERROR: sha256 MISMATCH"; exit 1; }
REMOTE
  fi
fi

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
