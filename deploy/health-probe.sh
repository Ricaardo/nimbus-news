#!/bin/bash
# ============================================================
# VPS 外部探活（cron 每 5 分钟，独立于平台进程）
#   curl 平台 /api/health；连续 FAIL_THRESHOLD 次失败 → 企业微信告警。
#   告警按"连续失败"去重：健康一次即清零计数，避免半夜刷屏。
# ============================================================
set -u
HEALTH_URL="http://127.0.0.1:8081/api/health"
FAIL_THRESHOLD=3
STATE_FILE="/opt/news/logs/health-probe.state"
PROBE_LOG="/opt/news/logs/health-probe.log"

# 企业微信 webhook 从 .env 读取（VPS 上 key 为 WECHAT_WEBHOOK）
if [ -f /opt/news/.env ]; then
  set -a; source /opt/news/.env; set +a
fi
WEBHOOK_URL="${WECHAT_WEBHOOK_URL:-${WECHAT_WEBHOOK:-}}"

probe() {
  local body
  body="$(curl -sf -m 5 "$HEALTH_URL" 2>/dev/null)" || return 1
  # 健康端点应返回 200 JSON；再兜底检查关键字段，防止 502 网关页误判
  echo "$body" | grep -q '"status"' || return 1
  return 0
}

consecutive_failures=0
if [ -f "$STATE_FILE" ]; then
  consecutive_failures="$(cat "$STATE_FILE" 2>/dev/null || echo 0)"
fi

if probe; then
  if [ "$consecutive_failures" -gt 0 ]; then
    echo "0" > "$STATE_FILE"
    echo "$(date '+%F %T') recovered (was $consecutive_failures consecutive failures)" >> "$PROBE_LOG"
  fi
  exit 0
fi

consecutive_failures=$((consecutive_failures + 1))
echo "$consecutive_failures" > "$STATE_FILE"
echo "$(date '+%F %T') probe failed ($consecutive_failures consecutive)" >> "$PROBE_LOG"

if [ "$consecutive_failures" -ge "$FAIL_THRESHOLD" ] && [ -n "$WEBHOOK_URL" ]; then
  curl -sf -m 10 -X POST "$WEBHOOK_URL" \
    -H 'Content-Type: application/json' \
    -d "{\"msgtype\":\"text\",\"text\":{\"content\":\"⚠️ news-platform 探活失败(连续 ${consecutive_failures} 次): http://127.0.0.1:8081/api/health 无响应 — ssh $HOSTNAME 检查 systemctl status news-platform\"}}" \
    >/dev/null 2>&1 && echo "$(date '+%F %T') alert sent" >> "$PROBE_LOG" \
    || echo "$(date '+%F %T') alert send failed" >> "$PROBE_LOG"
fi
