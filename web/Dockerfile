# ============================================
# 阶段1: 构建阶段
# ============================================
FROM node:20-alpine AS builder

WORKDIR /build

# 安装 pnpm
RUN npm install -g pnpm

# 先复制依赖文件，利用 Docker 缓存
COPY package.json pnpm-lock.yaml* package-lock.json* ./

# 安装依赖
RUN if [ -f pnpm-lock.yaml ]; then pnpm install --frozen-lockfile; \
    elif [ -f package-lock.json ]; then npm ci; \
    else npm install; fi

# 复制源代码
COPY . .

# 构建生产版本
RUN npm run build

# ============================================
# 阶段2: 运行阶段
# ============================================
FROM nginx:alpine

# 复制构建产物
COPY --from=builder /build/dist /usr/share/nginx/html

# 复制 nginx 配置
COPY nginx.conf /etc/nginx/conf.d/default.conf

# 暴露端口
EXPOSE 80

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost/health || exit 1

CMD ["nginx", "-g", "daemon off;"]
