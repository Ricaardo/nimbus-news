#!/bin/bash
# ============================================================
# VPS 端自动构建部署（cron 每 5 分钟）
#   本地 push 源码 → VPS 检测到新 commit → 构建 → 原子替换 → 重启
#   失败自动回滚到上一个二进制。
# 部署凭据: 两个 GitHub deploy key（nimbus-news / nimbus-os），
#   经 ~/.ssh/config 的 github-news / github-os alias 区分。
# ============================================================
set -euo pipefail

export PATH=/usr/local/go/bin:$PATH
export GOTMPDIR=/opt/news/tmp
SRC=/opt/news/src/nimbus-news
BIN=/opt/news/bin
STATE=/opt/news/.deployed-commit
LOG=/opt/news/logs/build-deploy.log

log() { echo "$(date '+%F %T') $*" >> "$LOG"; }

cd "$SRC"

# 0. scripts/ 与 deploy/ 软链到源码目录（git pull 后自动生效，无需 scp）
ln -sfn "$SRC/scripts" /opt/news/scripts
ln -sfn "$SRC/deploy" /opt/news/deploy

# 1. 拉取远程 main，无新提交则退出
git fetch -q origin main
HEAD="$(git rev-parse origin/main)"
DEPLOYED="$(cat "$STATE" 2>/dev/null || echo none)"
[ "$DEPLOYED" = "$HEAD" ] && exit 0

log ">>> 检测到新 commit ${HEAD:0:12} (deployed=${DEPLOYED:0:12})"

# 2. 构建
git pull --ff-only -q origin main
if ! CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$BIN/platform.new" ./cmd/platform 2>>"$LOG"; then
  log "!!! 构建失败，保持现状"
  exit 1
fi
log "构建完成 $(ls -la "$BIN/platform.new" | awk '{print $5}') bytes"

# 3. 原子替换 + 重启
mv -f "$BIN/platform" "$BIN/platform.prev" 2>/dev/null || true
mv -f "$BIN/platform.new" "$BIN/platform"
systemctl restart news-platform

# 4. 健康检查（轮询等待 API 就绪，最多 60s），失败回滚
healthy=0
for _ in $(seq 1 30); do
  if systemctl is-active news-platform >/dev/null 2>&1 \
     && curl -sf -m 3 http://127.0.0.1:8081/api/health >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 2
done
if [ "$healthy" -eq 1 ]; then
  echo "$HEAD" > "$STATE"
  rm -f "$BIN/platform.prev"
  log ">>> 部署成功 ${HEAD:0:12}"
else
  mv -f "$BIN/platform.prev" "$BIN/platform"
  systemctl restart news-platform
  log "!!! 健康检查失败，已回滚 ${HEAD:0:12}"
  exit 1
fi
