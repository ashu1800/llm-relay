#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# LLM Relay —— 一键安装（裸二进制 + systemd）
#
# 本仓库唯一的部署方式：从 GitHub Releases 下载预编译二进制（前端已
# embed 进二进制），自动安装并配置 PostgreSQL，注册 systemd 服务。
# 不需要 Docker，也不需要在服务器上装 Go/Node 现场编译。
#
# 用法：
#   curl -sSL https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.sh | sudo bash
#
#   # 自定义参数：跟在 bash -s -- 后面写成 KEY=VALUE（也支持 export 后 sudo -E）
#   curl -sSL https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.sh \
#     | sudo bash -s -- PORT=9000 BIND_ADDR=0.0.0.0
#
# 可用变量（环境变量或 KEY=VALUE 参数均可）：
#   INSTALL_DIR     安装目录，默认 /opt/llm-relay
#   PORT            监听端口，默认 8888（写入 .env 的 SERVER_PORT）
#   BIND_ADDR       监听地址，默认 127.0.0.1（写入 .env 的 SERVER_HOST；
#                   公网部署改 0.0.0.0，并务必保管好 RELAY_ADMIN_KEY）
#   DB_NAME/DB_USER 数据库名 / 用户，默认 llm_relay / llmrelay
#   VERSION         要安装的版本号（不带 v 前缀），默认取最新 Release
#   RELAY_GH_PROXY  GitHub 下载加速前缀（形如 https://gh-proxy.com），
#                   github.com 直连可达时不用填
#
# 重装安全性：重复运行本脚本就是升级。数据库密码、RELAY_SECRET、
# RELAY_ADMIN_KEY 从已有 .env 原样沿用（沿用不了会随机新生成并打印），
# 升级前自动把数据库备份到 $INSTALL_DIR/backups/。
# ---------------------------------------------------------------------------
set -euo pipefail

REPO="ashu1800/llm-relay"
SERVICE="llm-relay"

log()  { printf "\n\033[36m==> %s\033[0m\n" "$*"; }
ok()   { printf "\033[32m  %s\033[0m\n" "$*"; }
warn() { printf "\033[33m[注意] %s\033[0m\n" "$*"; }
die()  { printf "\033[31m[失败] %s\033[0m\n" "$*" >&2; exit 1; }

# ---------------- 参数解析（环境变量为主，KEY=VALUE 参数覆盖） -------------
INSTALL_DIR="${INSTALL_DIR:-/opt/llm-relay}"
PORT="${PORT:-8888}"
BIND_ADDR="${BIND_ADDR:-127.0.0.1}"
DB_NAME="${DB_NAME:-llm_relay}"
DB_USER="${DB_USER:-llmrelay}"
VERSION="${VERSION:-}"
RELAY_GH_PROXY="${RELAY_GH_PROXY:-}"

for a in "$@"; do
  case "$a" in
    *=*)
      key="${a%%=*}" val="${a#*=}"
      case "$key" in
        INSTALL_DIR|PORT|BIND_ADDR|DB_NAME|DB_USER|VERSION|RELAY_GH_PROXY) printf -v "$key" '%s' "$val" ;;
        *) warn "未知参数 $a（可用：INSTALL_DIR PORT BIND_ADDR DB_NAME DB_USER VERSION RELAY_GH_PROXY）" ;;
      esac
      ;;
    *) warn "忽略参数 $a（参数需写成 KEY=VALUE）" ;;
  esac
done

# ---------------- 前置检查 -----------------------------------------------
[ "$(id -u)" = "0" ] || die "请用 root 运行：curl -sSL ... | sudo bash"

command -v apt-get >/dev/null || die "本脚本只支持 Debian/Ubuntu（需要 apt-get）"
command -v systemctl >/dev/null || die "未检测到 systemctl，本脚本以 systemd 托管服务"
[ -d /run/systemd/system ] || die "systemd 未在运行。WSL 下请在 /etc/wsl.conf 加入 [boot] systemd=true，然后 Windows 侧执行 wsl --shutdown 后重进"

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64)          GOARCH=amd64 ;;
  aarch64|arm64)   GOARCH=arm64 ;;
  *) die "不支持的架构：$ARCH_RAW（发布矩阵只有 amd64 / arm64）" ;;
esac

# ---------------- 依赖安装 ------------------------------------------------
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

# ---------------- 确定版本与下载 ------------------------------------------
resolve_latest() {
  curl -fsSL --connect-timeout 15 -m 30 "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -nE 's/.*"tag_name":[[:space:]]*"v?([^"]+)".*/\1/p' | head -1
}

if [ -z "$VERSION" ]; then
  log "查询最新版本"
  VERSION="$(resolve_latest || true)"
  [ -n "$VERSION" ] || die "GitHub API 查询失败。国内网络可设置 RELAY_GH_PROXY=<加速前缀> 重试；或手动指定 VERSION=<版本号>"
fi
VERSION="${VERSION#v}"

if [ -n "$RELAY_GH_PROXY" ]; then
  DL_BASE="${RELAY_GH_PROXY%/}/https://github.com"
else
  DL_BASE="https://github.com"
fi
REL_BASE="$DL_BASE/$REPO/releases/download/v$VERSION"
ARCHIVE="llm-relay_${VERSION}_linux_${GOARCH}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

log "下载 $ARCHIVE"
curl -fSL --retry 3 --retry-delay 2 --connect-timeout 15 \
  -o "$TMP/$ARCHIVE"      "$REL_BASE/$ARCHIVE" \
  || die "下载失败：$REL_BASE/$ARCHIVE（GitHub 直连不稳定时，设置 RELAY_GH_PROXY=<加速前缀> 后重试）"
curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 15 \
  -o "$TMP/checksums.txt" "$REL_BASE/checksums.txt" \
  || die "下载失败：$REL_BASE/checksums.txt（GitHub 直连不稳定时，设置 RELAY_GH_PROXY=<加速前缀> 后重试）"

EXPECTED="$(sed -n "s/^\([0-9a-f]\{64\}\)[[:space:]]\{1,\}${ARCHIVE}\$/\1/p" "$TMP/checksums.txt")"
[ -n "$EXPECTED" ] || die "checksums.txt 里没有 $ARCHIVE（版本与架构不匹配？）"
ACTUAL="$(sha256sum "$TMP/$ARCHIVE" | cut -d' ' -f1)"
[ "$ACTUAL" = "$EXPECTED" ] || die "sha256 校验失败：期望 $EXPECTED，实际 $ACTUAL"
ok "sha256 校验通过"

tar -xzf "$TMP/$ARCHIVE" -C "$TMP"
[ -f "$TMP/llm-relay" ] || die "归档里没有 llm-relay 可执行文件，归档可能已损坏"

# ---------------- 数据库 --------------------------------------------------
log "配置 PostgreSQL"
systemctl enable --now postgresql >/dev/null 2>&1 || service postgresql start || true
for _ in $(seq 1 30); do
  su - postgres -c "psql -tAc 'SELECT 1'" >/dev/null 2>&1 && break
  sleep 1
done

# ---------------- 密钥生成 / 沿用 ----------------------------------------
# 数据库密码。首次安装随机生成，重装时从已有 .env 读回 —— 否则会把数据库
# 里的密码改掉、服务再也连不上。
mkdir -p "$INSTALL_DIR/deploy" "$INSTALL_DIR/data" "$INSTALL_DIR/backups"
ENV_FILE="$INSTALL_DIR/deploy/.env"
if [ -f "$ENV_FILE" ] && grep -q "^DB_PASSWORD=" "$ENV_FILE"; then
  # 剥引号与 CRLF/空白：Windows 手编 .env 常带入，混进 postgres 密码会造成
  # ALTER USER 与 systemd EnvironmentFile 两个口径永久错位、服务 crashloop
  DB_PASS="$(grep '^DB_PASSWORD=' "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]")"
  log "复用已有配置中的数据库密码"
else
  DB_PASS="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  log "已生成新的数据库密码"
fi

# 主密钥同理：重装必须沿用原值，否则已存库的渠道密钥再也解不开
if [ -f "$ENV_FILE" ] && grep -q "^RELAY_SECRET=.\+" "$ENV_FILE"; then
  RELAY_SECRET="$(grep '^RELAY_SECRET=' "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]")"
  log "复用已有配置中的加密主密钥"
else
  RELAY_SECRET="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  log "已生成新的加密主密钥"
fi

# 管理台登录密钥（无账号模型）：重装沿用原值，首次安装随机生成。
# 与加密主密钥相互独立 —— 登录密钥出现在每个登录请求里，泄露面更大，
# 两处绝不能共用同一个值
ADMIN_KEY_GENERATED=no
if [ -f "$ENV_FILE" ] && grep -q "^RELAY_ADMIN_KEY=.\+" "$ENV_FILE"; then
  RELAY_ADMIN_KEY="$(grep '^RELAY_ADMIN_KEY=' "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]")"
  log "复用已有配置中的管理台登录密钥"
else
  RELAY_ADMIN_KEY="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  ADMIN_KEY_GENERATED=yes
  log "已生成新的管理台登录密钥"
fi

# ---------------- 数据库建库建用户 ----------------------------------------
su - postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='${DB_USER}'\"" | grep -q 1 \
  || su - postgres -c "psql -c \"CREATE USER ${DB_USER} WITH PASSWORD '${DB_PASS}'\"" >/dev/null
# 已存在的用户也要同步密码，否则改过密码后服务连不上
su - postgres -c "psql -c \"ALTER USER ${DB_USER} WITH PASSWORD '${DB_PASS}'\"" >/dev/null
su - postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'\"" | grep -q 1 \
  || su - postgres -c "createdb -O ${DB_USER} ${DB_NAME}"
su - postgres -c "psql -c \"GRANT ALL PRIVILEGES ON DATABASE ${DB_NAME} TO ${DB_USER}\"" >/dev/null
# PostgreSQL 15+ 起 public schema 默认不再给普通用户建表权限，必须显式授予
su - postgres -c "psql -d ${DB_NAME} -c \"GRANT ALL ON SCHEMA public TO ${DB_USER}\"" >/dev/null

# ---------------- 升级前备份 ----------------------------------------------
if [ -x "$INSTALL_DIR/llm-relay" ]; then
  log "检测到已有安装，先备份数据库"
  SAFETY="$INSTALL_DIR/backups/pre-upgrade-$(date +%Y%m%d-%H%M%S).sql.gz"
  if su - postgres -c "pg_dump '$DB_NAME'" | gzip > "$SAFETY"; then
    chmod 600 "$SAFETY"
    ok "已备份到 $SAFETY（恢复入口：deploy/restore.sh）"
  else
    rm -f "$SAFETY"
    warn "数据库备份失败，继续安装（旧二进制仍在原位，可直接重试）"
  fi
fi

# ---------------- 安装 ----------------------------------------------------
log "安装到 $INSTALL_DIR"
install -m 0755 "$TMP/llm-relay" "$INSTALL_DIR/llm-relay"
if [ -f "$TMP/rotate-secret" ]; then
  install -m 0755 "$TMP/rotate-secret" "$INSTALL_DIR/rotate-secret"
else
  # 旧版本归档里没有这个工具；它只在「从内置默认主密钥迁移」时用到，
  # 缺了不阻塞安装，scripts/rotate-secret.sh 会给出对应提示
  warn "归档里没有 rotate-secret 工具（旧版本归档），跳过"
fi
# 备份恢复入口随部署走：归档里带了就装上（脚本不依赖仓库源码在服务器上）
if [ -f "$TMP/deploy/restore.sh" ]; then
  install -m 0755 "$TMP/deploy/restore.sh" "$INSTALL_DIR/deploy/restore.sh"
fi

cat > "$ENV_FILE" <<EOF
# 由 install.sh 生成于 $(date -Iseconds)
SERVER_PORT=${PORT}
SERVER_HOST=${BIND_ADDR}

# 渠道密钥的加密主密钥。缺失会退回程序内置的公开默认值，等于没有加密
RELAY_SECRET=${RELAY_SECRET}

# 管理台登录密钥：浏览器打开管理台时输入它登录（无账号模型）
RELAY_ADMIN_KEY=${RELAY_ADMIN_KEY}

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
RELAY_RETRY_SAME_UPSTREAM_DELAY=500ms

# 进程本地时区：可用时段与时段倍率按它判断。systemd 服务默认没有时区，
# 不写这行会按 UTC 跑，时段类配置会整体错位
TZ=Asia/Shanghai
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
  if curl -fsS -m 3 "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then OK=1; break; fi
  sleep 1
done

echo
if [ "$OK" = "1" ]; then
  printf "\033[32m部署完成（llm-relay ${VERSION} linux/${GOARCH}）\033[0m\n"
  echo "  管理后台: http://localhost:${PORT}"
  echo "  监听地址: ${BIND_ADDR}:${PORT}"
  echo "  配置文件: ${ENV_FILE}"
  echo "  管理密钥: ${RELAY_ADMIN_KEY}（打开管理台时输入它登录）"
  [ "$ADMIN_KEY_GENERATED" = "no" ] || echo "            （本次新生成，请妥善保存；泄露后在 ${ENV_FILE} 轮换并重启）"
  echo
  echo "  查看日志: journalctl -u ${SERVICE} -f"
  echo "  重启服务: systemctl restart ${SERVICE}"
  echo "  停止服务: systemctl stop ${SERVICE}"
  echo "  一键更新: 管理台「设置 → 版本与更新」里点一下即可（二进制自替换）"
  if [ "${BIND_ADDR}" != "127.0.0.1" ] && [ "${BIND_ADDR}" != "localhost" ]; then
    echo
    warn "当前绑定在 ${BIND_ADDR}，管理台已由 RELAY_ADMIN_KEY 保护；公网部署建议再加反向代理 + HTTPS。"
  fi
else
  systemctl status "$SERVICE" --no-pager -l | head -20 || true
  die "健康检查未通过，请查看上面的日志"
fi
