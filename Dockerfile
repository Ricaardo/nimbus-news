# ============================================
# 阶段1: 构建阶段
# ============================================
FROM golang:1.26-alpine AS builder

WORKDIR /build

# 安装必要的构建工具
RUN apk add --no-cache git ca-certificates tzdata

# 设置 Go 模块代理
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=off
ENV CGO_ENABLED=0

# 先复制依赖文件，利用 Docker 缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建可执行文件
RUN go build -ldflags="-s -w" -o news-platform ./cmd/platform

# ============================================
# 阶段2: 运行阶段
# ============================================
FROM alpine:3.19

WORKDIR /app

# 安装运行时依赖
RUN apk add --no-cache ca-certificates tzdata

# 设置时区
ENV TZ=Asia/Shanghai

# 从构建阶段复制二进制文件
COPY --from=builder /build/news-platform .

# 创建数据目录
RUN mkdir -p /app/data

# 暴露端口: 8080 webhook/callback, 8081 admin API, 8082 wxofficial callback
EXPOSE 8080 8081 8082

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8081/api/health || exit 1

# 启动命令
CMD ["./news-platform", "-config", "/app/config.yaml"]
