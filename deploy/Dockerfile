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
# 版本信息注入。这四个变量在 backend/internal/version 包里（不是 main 包）——
# 原来的 -X main.version 打进的是一个**即将不存在**的变量，静默失效，
# 结果就是界面上的版本号永远是 VERSION 文件里那个值。
#
# 变量全名 = Go 包导入路径 + 变量名，所以是 llm-relay/internal/version.Version。
# -X 打错目标不会报错，只会让注入悄无声息地不生效，改这行时务必逐字对照。
ARG VERSION=
ARG COMMIT=docker
ARG DATE
ARG BUILD_TYPE=docker
# resolve-version.sh 由上面那行 `COPY backend/ ./` 带进来，落在 /src/scripts/ 下，
# 所以这里是 ./scripts/… 而不是 backend/scripts/…（WORKDIR 就是 /src，且
# COPY 在 RUN 之前执行，脚本此刻已经在镜像里）。用 `sh` 显式执行，不依赖
# 可执行位 —— Windows 上检出/提交的文件带不带 +x 不确定，这样最稳。
# VERSION 为空时（docker-compose 未传 VERSION）由脚本解析：构建上下文里
# 排除了 .git（见 .dockerignore），git 拿不到 tag，脚本会回落到
# backend/internal/version/VERSION 文件的内容，正好是我们要的兜底。
RUN VERSION_VALUE="${VERSION}" && \
    if [ -z "${VERSION_VALUE}" ]; then VERSION_VALUE="$(./scripts/resolve-version.sh 2>/dev/null || echo dev)"; fi && \
    DATE_VALUE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" && \
    go build -trimpath \
      -ldflags "-s -w \
        -X llm-relay/internal/version.Version=${VERSION_VALUE} \
        -X llm-relay/internal/version.Commit=${COMMIT} \
        -X llm-relay/internal/version.Date=${DATE_VALUE} \
        -X llm-relay/internal/version.BuildType=${BUILD_TYPE}" \
      -o /out/llm-relay ./cmd/server && \
    printf '%s' "${VERSION_VALUE}" > /out/version.txt

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
# 版本号另存一份纯文本，并打成一个 OCI 镜像标签。
#
# 这是给**宿主侧更新器**用的（backend/cmd/updater）：
#
#   · 它不能读 /api/admin/system/version —— 那个接口要登录，
#     而更新器没有会话（实测返回 401）。
#   · 它也不该靠 HTTP 问版本：服务起不来的时候 HTTP 就是不通的，
#     而那恰恰是最需要知道「当前是哪个版本」的时刻。
#   · 读镜像是**离线**的：只要镜像还在本地，就能问出它的版本。
#
# 标签给 `docker image inspect` 用（更新器确认新镜像建对了），
# 文件给容器内排查用（docker compose exec app cat /app/version.txt）。
#
# 这三个 ARG 必须**在本 stage 里重新声明一次**。
#
# ARG 的作用域只到它所在的那个 stage 为止：stage 2 里声明过的 ARG，
# 到了 stage 3 就是空的（Docker 不会报错，只是静默为空）。实测踩过：
# 标签打出来是空串、version.txt 里的内容也是空的 ——
# 而这两样正是宿主侧更新器用来判断版本的依据。
ARG VERSION=
ARG COMMIT=docker
ARG DATE
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"
COPY --from=backend-build /out/version.txt /app/version.txt
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
