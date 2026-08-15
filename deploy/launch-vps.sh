#!/bin/bash
# VPS 启动脚本：加载 .env，前置 venv python，exec platform（让 systemd 直接接管 PID）
set -e
# 项目根目录 = 本脚本所在 deploy/ 的上一级
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 前置 venv python，让所有 script 源 subprocess 调用的 python3 解析到带依赖的解释器
export PATH="$ROOT/venv/bin:$PATH"

# 加载 .env 中的所有环境变量
if [ -f .env ]; then
    set -a
    source .env
    set +a
fi

# 时区：调度器用系统本地时区计算推送时间，必须固定为北京时间
export TZ=Asia/Shanghai

exec ./bin/platform -config config.platform.vps.yaml
