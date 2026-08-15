#!/bin/bash
# news-feed 桥接拉取器(VPS 版): 每分钟把 VPS 上 news 平台写的 feed 文件拉回 Mac
# 原路径,让 nimbus 投顾 / mcp-gateway 等本地消费者无感继续读。
# 部署于 launchd: com.news.feed-pull.plist (StartInterval 60)
set -u
# macOS 无 flock,用 mkdir 原子锁;上一轮还在跑则跳过
LOCK=/tmp/news-feed-pull.lockdir
if ! mkdir "$LOCK" 2>/dev/null; then exit 0; fi
trap 'rmdir "$LOCK"' EXIT

VPS=root@45.77.26.44
SRC=/opt/news/feed/
DST="$HOME/nimbus-os/nimbus/workspace/feed"

# 不用 --delete: 保留本地 13f-latest.json 等 VPS 不产出的历史文件
rsync -az --timeout 15 "$VPS:$SRC" "$DST" 2>>"$HOME/nimbus-news/logs/feed-pull.err.log" \
  || echo "$(date +%FT%T) rsync failed: $?" >>"$HOME/nimbus-news/logs/feed-pull.err.log"
