# 注意：不要加 `# syntax=docker/dockerfile:1`。
# 该指令会让 BuildKit 去 Docker Hub 拉取外部前端镜像，本机 Docker Hub 不可达会直接失败。
# 使用内置前端已覆盖本项目所需语法。

# ---------- Stage 1: 构建前端 ----------
FROM node:22-alpine AS frontend-build
WORKDIR /src
# 使用国内镜像源；npm ci 在有 lockfile 时比 npm install 快一个数量级且可复现
ENV npm_config_registry=https://registry.npmmirror.com \
    npm_config_fund=false \
    npm_config_audit=false \
    npm_config_fetch_retries=5 \
    npm_config_fetch_timeout=180000
COPY frontend/package.json frontend/package-lock.json* ./
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY frontend/ ./
RUN npm run build

# ---------- Stage 2: 构建后端 ----------
# 版本需与 backend/go.mod 的 go 指令保持一致，否则 go mod download 会直接失败
FROM golang:1.25-alpine AS backend-build
ENV GOPROXY=https://goproxy.cn,direct \
    GOSUMDB=off \
    CGO_ENABLED=0
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend-build /src/dist ./internal/web/dist
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/llm-relay ./cmd/server

# ---------- Stage 3: 运行时 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata curl \
 && addgroup -S relay && adduser -S -G relay relay
WORKDIR /app
COPY --from=backend-build /out/llm-relay /app/llm-relay
RUN mkdir -p /data && chown -R relay:relay /data /app
USER relay
EXPOSE 8888
ENV CONFIG_PATH=/app/config.yaml \
    DATA_DIR=/data
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8888/healthz || exit 1
ENTRYPOINT ["/app/llm-relay"]
