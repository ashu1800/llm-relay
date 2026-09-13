#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# LLM Relay —— 无 Docker 环境安装脚本
#
# 与 install.sh 的区别：不依赖 Docker，直接在本机装 Go / Node / PostgreSQL，
# 编译出二进制后交给 systemd 托管。适合不能跑容器或不想为一个小服务
# 常驻三个容器的场景。
#
# 用法：
#   sudo bash deploy/install-bare.sh
#   PORT=9000 sudo -E bash deploy/install-bare.sh
# ---------------------------------------------------------------------------
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DIR="${INSTALL_DIR:-/opt/llm-relay}"
# 应用读的是 SERVER_HOST / SERVER_PORT。写成 APP_PORT / BIND_ADDR 不会报错，
# 只在非默认端口时静默失效，所以这里用变量名对齐，避免踩这个坑。
APP_PORT="${PORT:-8888}"
BIND_ADDR="${BIND_ADDR:-127.0.0.1}"
DB_NAME="${DB_NAME:-llm_relay}"
DB_USER="${DB_USER:-llmrelay}"
GOPROXY_VAL="${GOPROXY:-https://goproxy.cn,direct}"
NPM_REGISTRY="${NPM_REGISTRY:-https://registry.npmmirror.com}"
SERVICE="llm-relay"

log()  { printf "\n\033[36m==> %s\033[0m\n" "$*"; }
warn() { printf "\033[33m[注意] %s\033[0m\n" "$*"; }
die()  { printf "\033[31m[失败] %s\033[0m\n" "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "请用 root 运行：sudo bash deploy/install-bare.sh"
[ -f "$REPO_DIR/backend/go.mod" ] || die "没找到 backend/go.mod，请从仓库根目录运行本脚本"

# 生成数据库密码。首次安装时随机生成，重装时从已有配置读回，
# 否则会把数据库里的密码改掉、服务再也连不上。
mkdir -p "$INSTALL_DIR/deploy"
ENV_FILE="$INSTALL_DIR/deploy/.env"
if [ -f "$ENV_FILE" ] && grep -q "^DB_PASSWORD=" "$ENV_FILE"; then
  DB_PASS="$(grep '^DB_PASSWORD=' "$ENV_FILE" | head -1 | cut -d= -f2-)"
  log "复用已有配置中的数据库密码"
else
  DB_PASS="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  log "已生成新的数据库密码"
fi

# 主密钥同理：重装必须沿用原值，否则已存库的渠道密钥再也解不开
if [ -f "$ENV_FILE" ] && grep -q "^RELAY_SECRET=.\+" "$ENV_FILE"; then
  RELAY_SECRET="$(grep '^RELAY_SECRET=' "$ENV_FILE" | head -1 | cut -d= -f2-)"
  log "复用已有配置中的加密主密钥"
else
  RELAY_SECRET="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  log "已生成新的加密主密钥"
fi

# ---------------------------------------------------------------- 依赖安装
log "检查系统依赖"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq

need_pkg() { dpkg -s "$1" >/dev/null 2>&1; }

PKGS=()
need_pkg curl || PKGS+=(curl)
need_pkg ca-certificates || PKGS+=(ca-certificates)
need_pkg postgresql || PKGS+=(postgresql)
need_pkg postgresql-contrib || PKGS+=(postgresql-contrib)
if [ "${#PKGS[@]}" -gt 0 ]; then
  log "安装：${PKGS[*]}"
  apt-get install -y -qq "${PKGS[@]}"
fi

# Go：优先用系统包，太旧则下载官方 tarball
GO_BIN="$(command -v go || true)"
GO_OK=0
if [ -n "$GO_BIN" ]; then
  GOVER="$("$GO_BIN" version | sed -E 's/.*go([0-9]+)\.([0-9]+).*/\1\2/')"
  [ "$GOVER" -ge 125 ] 2>/dev/null && GO_OK=1
fi
if [ "$GO_OK" != "1" ]; then
  log "安装 Go 1.25（系统版本缺失或过旧）"
  GOVERSION="1.25.3"
  curl -fsSL "https://golang.google.cn/dl/go${GOVERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz \
    || die "Go 下载失败，请检查网络或手动安装 Go >= 1.25"
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && rm -f /tmp/go.tgz
  export PATH="/usr/local/go/bin:$PATH"
fi
go version >/dev/null || die "go 不可用"

# Node：仅编译前端需要，已有可用版本就跳过
NODE_OK=0
if command -v node >/dev/null 2>&1; then
  NMAJOR="$(node -v | sed -E 's/v([0-9]+).*/\1/')"
  [ "$NMAJOR" -ge 18 ] 2>/dev/null && NODE_OK=1
fi
if [ "$NODE_OK" != "1" ]; then
  log "安装 Node.js 22"
  curl -fsSL https://deb.nodesource.com/setup_22.x | bash - >/dev/null 2>&1 \
    || die "NodeSource 配置失败"
  apt-get install -y -qq nodejs
fi
node -v >/dev/null || die "node 不可用"

# ---------------------------------------------------------------- 数据库
log "配置 PostgreSQL"
systemctl enable --now postgresql >/dev/null 2>&1 || service postgresql start || true
for _ in $(seq 1 30); do
  su - postgres -c "psql -tAc 'SELECT 1'" >/dev/null 2>&1 && break
  sleep 1
done

su - postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='${DB_USER}'\"" | grep -q 1 \
  || su - postgres -c "psql -c \"CREATE USER ${DB_USER} WITH PASSWORD '${DB_PASS}'\"" >/dev/null
# 已存在的用户也要同步密码，否则改过密码后服务连不上
su - postgres -c "psql -c \"ALTER USER ${DB_USER} WITH PASSWORD '${DB_PASS}'\"" >/dev/null
su - postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'\"" | grep -q 1 \
  || su - postgres -c "createdb -O ${DB_USER} ${DB_NAME}"
su - postgres -c "psql -c \"GRANT ALL PRIVILEGES ON DATABASE ${DB_NAME} TO ${DB_USER}\" >/dev/null"
# PostgreSQL 15+ 起 public schema 默认不再给普通用户建表权限，必须显式授予
su - postgres -c "psql -d ${DB_NAME} -c \"GRANT ALL ON SCHEMA public TO ${DB_USER}\" >/dev/null"

# ---------------------------------------------------------------- 编译
log "编译前端"
cd "$REPO_DIR/frontend"
npm ci --registry="$NPM_REGISTRY" --no-audit --no-fund 2>&1 | tail -3
npm run build 2>&1 | tail -5

log "编译后端（前端产物会被 embed 进二进制，必须先构建前端）"
rm -rf "$REPO_DIR/backend/internal/web/dist"
cp -r "$REPO_DIR/frontend/dist" "$REPO_DIR/backend/internal/web/dist"
cd "$REPO_DIR/backend"
export GOPROXY="$GOPROXY_VAL" GOSUMDB=off CGO_ENABLED=0
go build -trimpath -ldflags="-s -w" -o /tmp/llm-relay ./cmd/server

# ---------------------------------------------------------------- 安装
log "安装到 $INSTALL_DIR"
install -m 0755 /tmp/llm-relay "$INSTALL_DIR/llm-relay"
rm -f /tmp/llm-relay
mkdir -p "$INSTALL_DIR/data"

cat > "$ENV_FILE" <<EOF
# 由 install-bare.sh 生成于 $(date -Iseconds)
SERVER_PORT=${APP_PORT}
SERVER_HOST=${BIND_ADDR}

# 渠道密钥的加密主密钥。缺失会退回程序内置的公开默认值，等于没有加密
RELAY_SECRET=${RELAY_SECRET}

DB_HOST=127.0.0.1
DB_PORT=5432
DB_NAME=${DB_NAME}
DB_USER=${DB_USER}
DB_PASSWORD=${DB_PASS}
DB_SSLMODE=disable

LOG_LEVEL=info
RELAY_PAYLOAD_STORAGE_MODE=errors
RELAY_PAYLOAD_MAX_KB=256
RELAY_MAX_CONCURRENCY=64
RELAY_DEFAULT_RPM=0
EOF
chmod 600 "$ENV_FILE"

log "写入 systemd 服务"
cat > "/etc/systemd/system/${SERVICE}.service" <<EOF
[Unit]
Description=LLM Relay - 本地大模型中转服务
After=network-online.target postgresql.service
Wants=network-online.target
Requires=postgresql.service

[Service]
Type=simple
WorkingDirectory=${INSTALL_DIR}
# 配置以环境变量注入，.env 里可能含数据库密码，因此不让 systemd 读取整个文件，
# 而是由 EnvironmentFile 解析后逐条传入（systemd 原生支持这种写法）
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_DIR}/llm-relay
Restart=always
RestartSec=3
# 日志进 journald，用 journalctl -u llm-relay 查看
StandardOutput=journal
StandardError=journal
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE" >/dev/null 2>&1 || true
systemctl restart "$SERVICE"

log "等待健康检查"
OK=0
for _ in $(seq 1 40); do
  if curl -fsS -m 3 "http://127.0.0.1:${APP_PORT}/healthz" >/dev/null 2>&1; then OK=1; break; fi
  sleep 1
done

echo
if [ "$OK" = "1" ]; then
  printf "\033[32m部署完成\033[0m\n"
  echo "  管理后台: http://localhost:${APP_PORT}"
  echo "  监听地址: ${BIND_ADDR}:${APP_PORT}"
  echo "  配置文件: ${ENV_FILE}"
  echo
  echo "  查看日志: journalctl -u ${SERVICE} -f"
  echo "  重启服务: systemctl restart ${SERVICE}"
  echo "  停止服务: systemctl stop ${SERVICE}"
  if [ "${BIND_ADDR}" != "127.0.0.1" ] && [ "${BIND_ADDR}" != "localhost" ]; then
    echo
    warn "当前绑定在 ${BIND_ADDR}，管理接口没有鉴权，同网段可读取日志与密钥。"
    warn "若只需本机访问，建议改成 BIND_ADDR=127.0.0.1 后重启。"
  fi
else
  systemctl status "$SERVICE" --no-pager -l | head -20 || true
  die "健康检查未通过，请查看上面的日志"
fi
