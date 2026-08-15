# ============================================
# 投资信息平台 - 一键部署 Makefile
# ============================================
# 使用方式:
#   make help      - 查看所有可用命令
#   make deploy    - 一键部署（推荐）
#   make dev       - 本地开发模式
# ============================================

.PHONY: help init deploy build up down logs logs-backend logs-frontend restart clean clean-all \
        dev dev-backend dev-frontend status ps health backup restore update symbols python-deps \

# 默认目标
.DEFAULT_GOAL := help

# 颜色定义
GREEN  := \033[0;32m
YELLOW := \033[0;33m
BLUE   := \033[0;34m
RED    := \033[0;31m
NC     := \033[0m # No Color

CONFIG ?= config.yaml
ENV_FILE ?= .env
BACKEND_API_URL ?= http://localhost:8081
FRONTEND_URL ?= http://localhost:$${FRONTEND_PORT:-80}

# ==========================================
# 帮助信息
# ==========================================
help:
	@echo ""
	@echo "$(BLUE)============================================$(NC)"
	@echo "$(BLUE)    投资信息平台 - 部署命令$(NC)"
	@echo "$(BLUE)============================================$(NC)"
	@echo ""
	@echo "$(GREEN)一键部署:$(NC)"
	@echo "  make init          - 创建本地配置和环境变量文件"
	@echo "  make deploy        - 完整部署（构建+启动）"
	@echo "  make update        - 更新部署（拉取代码+重新构建）"
	@echo ""
	@echo "$(GREEN)容器管理:$(NC)"
	@echo "  make build         - 构建镜像"
	@echo "  make up            - 启动服务"
	@echo "  make down          - 停止服务"
	@echo "  make restart       - 重启服务"
	@echo "  make logs          - 查看日志"
	@echo "  make status        - 查看服务状态"
	@echo "  make health        - 健康检查"
	@echo ""
	@echo "$(GREEN)本地开发:$(NC)"
	@echo "  make dev           - 启动本地开发环境"
	@echo "  make dev-backend   - 仅启动后端"
	@echo "  make dev-frontend  - 仅启动前端"
	@echo ""
	@echo "$(GREEN)数据管理:$(NC)"
	@echo "  make backup        - 备份数据"
	@echo "  make restore       - 恢复数据"
	@echo "  make symbols       - 重新生成 data/index 标的索引"
	@echo "  make clean         - 清理（保留数据）"
	@echo "  make clean-all     - 完全清理（包括数据）"
	@echo ""

# ==========================================
# 一键部署
# ==========================================
init:
	@if [ ! -f "$(CONFIG)" ]; then cp config.yaml.example "$(CONFIG)"; echo "$(GREEN)✓ 已创建 $(CONFIG)$(NC)"; else echo "$(YELLOW)$(CONFIG) 已存在，跳过$(NC)"; fi
	@if [ ! -f "$(ENV_FILE)" ]; then cp .env.example "$(ENV_FILE)"; echo "$(GREEN)✓ 已创建 $(ENV_FILE)$(NC)"; else echo "$(YELLOW)$(ENV_FILE) 已存在，跳过$(NC)"; fi
	@mkdir -p data/cache data/index data/reports

deploy: init build up
	@echo "$(GREEN)✓ 部署完成!$(NC)"
	@echo ""
	@echo "访问地址:"
	@echo "  - 前端: $(FRONTEND_URL)"
	@echo "  - API:  $(BACKEND_API_URL)/api"
	@echo ""
	@echo "查看日志: make logs"

update:
	@echo "$(BLUE)>>> 更新部署...$(NC)"
	git pull
	@$(MAKE) deploy

# ==========================================
# 容器管理
# ==========================================
build:
	@echo "$(BLUE)>>> 构建镜像...$(NC)"
	docker compose build

# 原生二进制（现网 platform）。
build-native:
	@echo "$(BLUE)>>> go build platform...$(NC)"
	go build -o bin/platform ./cmd/platform

up:
	@echo "$(BLUE)>>> 启动服务...$(NC)"
	docker compose up -d
	@echo "$(GREEN)✓ 服务已启动$(NC)"

down:
	@echo "$(BLUE)>>> 停止服务...$(NC)"
	docker compose down
	@echo "$(GREEN)✓ 服务已停止$(NC)"

restart:
	@echo "$(BLUE)>>> 重启服务...$(NC)"
	docker compose restart
	@echo "$(GREEN)✓ 服务已重启$(NC)"

logs:
	docker compose logs -f

logs-backend:
	docker compose logs -f backend

logs-frontend:
	docker compose logs -f frontend

status:
	@echo "$(BLUE)>>> 服务状态$(NC)"
	docker compose ps

ps: status

health:
	@echo "$(BLUE)>>> 健康检查$(NC)"
	@echo ""
	@echo "后端服务:"
	@curl -s $(BACKEND_API_URL)/api/health && echo " $(GREEN)✓ 正常$(NC)" || echo " $(RED)✗ 异常$(NC)"
	@echo ""
	@echo "前端服务:"
	@curl -s $(FRONTEND_URL)/health && echo " $(GREEN)✓ 正常$(NC)" || echo " $(RED)✗ 异常$(NC)"

smoke-longbridge:
	@echo "$(BLUE)>>> Longbridge 行情链路 smoke 检查$(NC)"
	@bash scripts/longbridge_smoke.sh

# ==========================================
# 本地开发
# ==========================================
dev: dev-backend dev-frontend
	@echo "$(GREEN)✓ 开发环境已启动$(NC)"
	@echo "  - 后端: http://localhost:8081"
	@echo "  - 前端: http://localhost:3000"

dev-backend:
	@echo "$(BLUE)>>> 启动后端开发服务...$(NC)"
	@cd . && go run ./cmd/platform -config "$(CONFIG)" &

dev-frontend:
	@echo "$(BLUE)>>> 启动前端开发服务...$(NC)"
	@cd web && npm run dev &

# ==========================================
# 数据管理
# ==========================================
BACKUP_DIR := ./backups
TIMESTAMP := $(shell date +%Y%m%d_%H%M%S)

backup:
	@echo "$(BLUE)>>> 备份数据...$(NC)"
	@mkdir -p $(BACKUP_DIR)
	@tar -czvf $(BACKUP_DIR)/data_$(TIMESTAMP).tar.gz data/
	@echo "$(GREEN)✓ 备份完成: $(BACKUP_DIR)/data_$(TIMESTAMP).tar.gz$(NC)"

restore:
	@echo "$(BLUE)>>> 恢复数据...$(NC)"
	@if [ -z "$(FILE)" ]; then \
		echo "$(RED)请指定备份文件: make restore FILE=backups/data_xxx.tar.gz$(NC)"; \
		exit 1; \
	fi
	@tar -xzvf $(FILE)
	@echo "$(GREEN)✓ 数据已恢复$(NC)"

symbols:
	@echo "$(BLUE)>>> 生成标的索引...$(NC)"
	@mkdir -p data/index
	python3 scripts/fetch_symbols.py

python-deps:
	pip install -r scripts/requirements.txt

clean:
	@echo "$(BLUE)>>> 清理容器和镜像...$(NC)"
	docker compose down --rmi local
	@echo "$(GREEN)✓ 清理完成（数据已保留）$(NC)"

clean-all:
	@echo "$(YELLOW)警告: 这将删除所有数据!$(NC)"
	@read -p "确认删除? [y/N] " confirm && [ "$$confirm" = "y" ] || exit 1
	docker compose down -v --rmi local
	rm -rf data
	@echo "$(GREEN)✓ 完全清理完成$(NC)"
