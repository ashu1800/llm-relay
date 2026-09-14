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
# 固定到 3.24 而不是原来的 3.20：3.20 的支持期已在 2026-04-01 结束，
# 之后不再有安全更新，而基础镜像里的漏洞是直接进产物的。
# 查证来源 https://endoflife.date/alpine —— 3.24 支持到 2028-06-01。
# 升级时同步改这一行即可（Alpine 每个小版本大约支持两年）。
FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata curl \
 && addgroup -S relay && adduser -S -G relay relay
WORKDIR /app
COPY --from=backend-build /out/llm-relay /app/llm-relay
RUN mkdir -p /data && chown -R relay:relay /data /app
USER relay
EXPOSE 8888
ENV CONFIG_PATH=/app/config.yaml \
    DATA_DIR=/data
# 探 /readyz 而不是 /healthz。
#
# healthz 的语义是「进程还活着」，它不碰数据库 —— 数据库连不上时它照样
# 返回 200，而这个容器此刻对任何请求都只会回 500。用它当健康检查，
# 编排层会认为一切正常，故障因此不会被自动发现或重启。
# readyz 会实际探一次数据库，那才是「能对外服务」的定义。
#
# start-period 给 40s：启动时要跑 schema 迁移，迁移期间 readyz 会失败，
# 那属于正常启动过程而不是故障。
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8888/readyz || exit 1
ENTRYPOINT ["/app/llm-relay"]
