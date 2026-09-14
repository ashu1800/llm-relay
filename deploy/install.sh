#!/usr/bin/env bash
# ============================================================
#  LLM Relay 一键部署脚本（WSL2 / Ubuntu 24.04）
#
#  用法（二选一）：
#    sudo bash install.sh                 # 常规：用 sudo
#    wsl -u root -- bash install.sh       # WSL 专用：免密直接以 root 运行
#
#  脚本做的事：
#    1. 清理 Docker Desktop 卸载后的失效软链
#    2. 安装 Docker Engine（download.docker.com，直连可用）
#    3. 探测可用的上游代理，必要时用 privoxy 做 socks5 -> http 转换
#       （Docker daemon 不原生支持 socks5，必须转换）
#    4. 部署源码到 /opt/llm-relay，生成带随机密码的 .env
#    5. docker compose 构建并启动（端口 8888）
#    6. 注册 llm-relay.service 实现开机自启
#
#  可用环境变量覆盖：
#    INSTALL_DIR        安装目录，默认 /opt/llm-relay
#    USE_PROXY          auto | yes | no，默认 auto
#    UPSTREAM_SOCKS     上游 socks5，默认见下
#    UPSTREAM_HTTP      上游 http 代理，可选
#  （端口固定 8888，改绑地址请设 deploy/.env 的 BIND_ADDR，见其注释）
# ============================================================
set -uo pipefail

# 默认 umask 022 会让脚本创建的所有文件都是 0644（同机其他用户可读），
# 而这个脚本会写出 .env（含数据库密码与主密钥）、代理凭据与数据库备份 ——
# 这几类内容都不该被同机其他用户读到。改成 077 后：
#   · 显式 chmod 600 的文件仍是 600（下面那些调用不变）
#   · 忘了 chmod 的新文件默认就是 600/700，不再需要每处都记住
# 已有安装不受影响（.env 早就是 600），但新建的备份与新写的凭据会立刻受益。
umask 077

APP_NAME="llm-relay"
INSTALL_DIR="${INSTALL_DIR:-/opt/llm-relay}"
# PORT 只用于最后打印的访问地址与端口。真正生效的是 compose 里的
# 宿主端口映射 —— 容器内固定 8888，宿主侧由 deploy/.env 的 BIND_ADDR
# 决定绑定地址，端口号本身在 docker-compose.yml 里写死为 8888:8888。
# 所以这里不再提供 PORT 覆盖：允许它和实际映射不一致时，脚本会打印一个
# 根本连不上的地址（例如 PORT=9000 而实际发布的是 8888），
# 那种「装好了但打不开、且提示里的地址是错的」最难排查。
PORT=8888
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# 代理凭据不再内置。可先 export UPSTREAM_SOCKS / UPSTREAM_HTTP，
# 或写入 deploy/.env（已被 .gitignore 排除），安装脚本会自动读取。
if [[ -f "$SRC_DIR/deploy/.env" ]]; then
  # shellcheck disable=SC1091
  set -a; . "$SRC_DIR/deploy/.env"; set +a
fi
UPSTREAM_SOCKS="${UPSTREAM_SOCKS:-}"
UPSTREAM_HTTP="${UPSTREAM_HTTP:-}"
HTTP_PROXY_PORT="${HTTP_PROXY_PORT:-8118}"
USE_PROXY="${USE_PROXY:-auto}"

log()  { printf '\033[1;36m[%s]\033[0m %s\n' "$APP_NAME" "$*"; }
ok()   { printf '\033[1;32m[%s]\033[0m %s\n' "$APP_NAME" "$*"; }
warn() { printf '\033[1;33m[%s]\033[0m %s\n' "$APP_NAME" "$*" >&2; }
die()  { printf '\033[1;31m[%s]\033[0m %s\n' "$APP_NAME" "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "需要 root 权限。请用：sudo bash install.sh  或  wsl -u root -- bash install.sh"

# ---------- 1. 清理失效软链 ----------
log "清理 Docker Desktop 残留软链"
for p in /usr/bin/docker /usr/bin/docker-compose /usr/local/bin/docker /usr/bin/docker-credential-desktop.exe; do
  if [[ -L "$p" && ! -e "$p" ]]; then warn "移除失效软链 $p"; rm -f "$p"; fi
done

# ---------- 2. 安装 Docker Engine ----------
if ! command -v docker >/dev/null 2>&1; then
  log "安装 Docker Engine"
  install -m 0755 -d /etc/apt/keyrings
  if ! curl -fsSL --connect-timeout 12 -m 90 https://download.docker.com/linux/ubuntu/gpg \
       -o /etc/apt/keyrings/docker.asc; then
    warn "直连下载 GPG 失败，改用 http 代理"
    curl -fsSL --connect-timeout 12 -m 90 -x "$UPSTREAM_HTTP" \
      https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
      || die "GPG 密钥下载失败"
  fi
  chmod a+r /etc/apt/keyrings/docker.asc
  CODENAME="$(. /etc/os-release && echo "$VERSION_CODENAME")"
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $CODENAME stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin privoxy
  ok "Docker $(docker --version 2>/dev/null | awk '{print $3}' | tr -d ,)"
else
  ok "Docker 已安装：$(docker --version)"
fi

# ---------- 3. 代理配置 ----------
# 判定顺序：
#   a) Docker Hub 直连可用 -> 不配代理
#   b) socks5 可用          -> 装 privoxy 转 http，Docker daemon 走 127.0.0.1:8118
#   c) http 代理可用        -> Docker daemon 直接用
#   d) 都不可用             -> 警告并继续（可能已配置镜像加速）
setup_proxy() {
  log "探测 Docker Hub 连通性"

  if curl -s -o /dev/null -m 15 https://registry-1.docker.io/v2/; then
    ok "Docker Hub 直连可用，跳过代理配置"
    return 0
  fi
  warn "Docker Hub 直连不可用"

  # 优先 socks5（实测：HTTP 代理对 Docker Hub 会被重置，socks5 可通）
  if [[ -n "$UPSTREAM_SOCKS" ]] && \
     curl -s -o /dev/null -m 25 -x "socks5h://$UPSTREAM_SOCKS" https://registry-1.docker.io/v2/; then
    ok "socks5 上游可用，配置 privoxy 转换为 http"

    if ! command -v privoxy >/dev/null 2>&1; then
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq privoxy || { warn "privoxy 安装失败"; return 1; }
    fi

    if ! grep -q 'llm-relay-forward' /etc/privoxy/config 2>/dev/null; then
      cp -n /etc/privoxy/config /etc/privoxy/config.orig 2>/dev/null || true
      printf '\n# llm-relay-forward\nforward-socks5t / %s .\n' "$UPSTREAM_SOCKS" >> /etc/privoxy/config
    fi

    # listen-address 必须唯一，否则 privoxy 启动失败
    python3 - <<'PY'
p = '/etc/privoxy/config'
try:
    lines = open(p, encoding='utf-8', errors='replace').read().splitlines()
except FileNotFoundError:
    raise SystemExit(0)
out, seen = [], False
for ln in lines:
    if ln.startswith('listen-address'):
        if seen:
            continue
        out.append('listen-address  127.0.0.1:8118')
        seen = True
    else:
        out.append(ln)
open(p, 'w', encoding='utf-8').write('\n'.join(out) + '\n')
PY

    systemctl enable privoxy >/dev/null 2>&1 || true
    systemctl restart privoxy
    sleep 2

    if ! systemctl is-active --quiet privoxy; then
      warn "privoxy 启动失败，代理未生效"
      return 1
    fi
    if ! curl -s -o /dev/null -m 25 -x "http://127.0.0.1:$HTTP_PROXY_PORT" https://registry-1.docker.io/v2/; then
      warn "privoxy 转发不通，代理可能无效"
      return 1
    fi
    ok "privoxy 就绪：127.0.0.1:$HTTP_PROXY_PORT"

    mkdir -p /etc/systemd/system/docker.service.d
    cat > /etc/systemd/system/docker.service.d/http-proxy.conf <<EOF
[Service]
Environment="HTTP_PROXY=http://127.0.0.1:$HTTP_PROXY_PORT"
Environment="HTTPS_PROXY=http://127.0.0.1:$HTTP_PROXY_PORT"
Environment="NO_PROXY=localhost,127.0.0.1,::1,postgres,llm-relay"
EOF
    return 0
  fi

  # 退回 http 代理
  if [[ -n "$UPSTREAM_HTTP" ]] && \
     curl -s -o /dev/null -m 25 -x "$UPSTREAM_HTTP" https://registry-1.docker.io/v2/; then
    ok "http 代理上游可用"
    mkdir -p /etc/systemd/system/docker.service.d
    cat > /etc/systemd/system/docker.service.d/http-proxy.conf <<EOF
[Service]
Environment="HTTP_PROXY=$UPSTREAM_HTTP"
Environment="HTTPS_PROXY=$UPSTREAM_HTTP"
Environment="NO_PROXY=localhost,127.0.0.1,::1,postgres,llm-relay"
EOF
    return 0
  fi

  warn "未找到可用的上游代理；若已配置 registry-mirrors 可继续"
  return 1
}

case "$USE_PROXY" in
  no|false|0) log "USE_PROXY=$USE_PROXY，跳过代理配置" ;;
  *)          setup_proxy || true ;;
esac

systemctl enable docker >/dev/null 2>&1 || true
systemctl restart docker
sleep 5
docker info >/dev/null 2>&1 || die "Docker daemon 未就绪，请检查 systemctl status docker"
ok "Docker daemon 运行中"

# ---------- 4. 部署源码 ----------
log "部署源码到 $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
if [[ "$SRC_DIR" != "$INSTALL_DIR" ]]; then
  # 先清掉旧源码目录：tar 只覆盖不删除，残留的已删除文件会导致编译报重复声明。
  # 注意保留 deploy/data（数据库卷）与 deploy/.env（密钥）。
  # deploy/.env 必须在下面的 exclude 里：它是运行期配置，覆盖掉会让数据库密码丢失。
  rm -rf "$INSTALL_DIR/backend" "$INSTALL_DIR/frontend" \
         "$INSTALL_DIR/scripts" "$INSTALL_DIR/docs"
  tar -C "$SRC_DIR" \
      --exclude='./frontend/node_modules' \
      --exclude='./frontend/dist' \
      --exclude='./backend/internal/web/dist' \
      --exclude='./deploy/data' \
      --exclude='./deploy/.env' \
      --exclude='./.git' \
      --exclude='./.chrome-profile' \
      --exclude='./docs/shots' \
      --exclude='./.shots' \
      -cf - . | tar -C "$INSTALL_DIR" -xf -
fi
mkdir -p "$INSTALL_DIR/deploy/data"/{postgres,relay}

# ---------- 5. 生成 .env ----------
ENV_FILE="$INSTALL_DIR/deploy/.env"
if [[ ! -f "$ENV_FILE" ]]; then
  log "生成 $ENV_FILE"
  DB_PASSWORD="$(openssl rand -hex 24)"
  RELAY_SECRET="$(openssl rand -hex 24)"
  sed -e "s|^DB_PASSWORD=.*|DB_PASSWORD=$DB_PASSWORD|" \
      -e "s|^RELAY_SECRET=.*|RELAY_SECRET=$RELAY_SECRET|" \
      "$INSTALL_DIR/deploy/.env.example" > "$ENV_FILE"
  chmod 600 "$ENV_FILE"
elif ! grep -q '^DB_PASSWORD=.\+' "$ENV_FILE"; then
  # 走到这里说明 .env 被覆盖或写坏了。直接生成新密码会让已有数据库连不上，
  # 所以只补默认值并明确告警，由用户决定是否回填原密码。
  warn "$ENV_FILE 缺少 DB_PASSWORD，可能被源码目录的同名文件覆盖过"
  warn "已从 .env.example 补齐缺失项并生成新密码"
  warn "若数据库此前已初始化，请把 DB_PASSWORD 改回原值，否则会连不上："
  warn "  docker inspect llm-relay-postgres --format '{{range .Config.Env}}{{println .}}{{end}}' | grep POSTGRES_PASSWORD"
  DB_PASSWORD="$(openssl rand -hex 24)"
  # 保留用户已改的键，只补齐缺失的
  TMP_ENV="$(mktemp)"
  cp "$ENV_FILE" "$TMP_ENV"
  # 主密钥只补缺失，绝不为已有配置重新生成——换掉它会让全部渠道密钥解不开
  if grep -q '^RELAY_SECRET=.\+' "$TMP_ENV"; then
    RELAY_SECRET="$(grep '^RELAY_SECRET=' "$TMP_ENV" | head -1 | cut -d= -f2-)"
  else
    RELAY_SECRET="$(openssl rand -hex 24)"
    warn "补齐了缺失的 RELAY_SECRET（原配置里没有）"
    warn "若渠道已用内置默认密钥加密过，需要运行轮换工具迁移，否则渠道会认证失败"
  fi
  sed -e "s|^DB_PASSWORD=.*|DB_PASSWORD=$DB_PASSWORD|" \
      -e "s|^RELAY_SECRET=.*|RELAY_SECRET=$RELAY_SECRET|" \
      "$INSTALL_DIR/deploy/.env.example" > "$ENV_FILE"
  while IFS= read -r line; do
    key="${line%%=*}"
    [[ -z "$key" || "$key" == \#* ]] && continue
    grep -q "^${key}=" "$ENV_FILE" || printf '%s\n' "$line" >> "$ENV_FILE"
  done < "$TMP_ENV"
  rm -f "$TMP_ENV"
  chmod 600 "$ENV_FILE"
else
  log "复用已存在的 $ENV_FILE"
  # 老版本安装的 .env 没有 RELAY_SECRET，缺了会退回公开的内置默认值
  if ! grep -q '^RELAY_SECRET=.\+' "$ENV_FILE"; then
    printf 'RELAY_SECRET=%s\n' "$(openssl rand -hex 24)" >> "$ENV_FILE"
    warn "已为 $ENV_FILE 补上 RELAY_SECRET（此前缺失，渠道密钥用的是公开默认密钥）"
    warn "已有渠道需要用 scripts/rotate-secret 迁移，否则会认证失败"
  fi
fi

# ---------- 5.5 升级前自动备份数据库 ----------
# 启动时后端会自动执行迁移（含改列、删表）。迁移一旦执行就是单向的，
# 出问题时没有备份只能靠重新录配置。所以只要旧库还在，就先把整库导出来。
log "备份现有数据库（迁移前的安全网）"
if docker ps --format '{{.Names}}' | grep -qx "${APP_NAME}-postgres"; then
  BACKUP_DIR="${INSTALL_DIR}/backups"
  mkdir -p "$BACKUP_DIR"
  # 备份里有全部渠道的上游密钥（密文）、渠道配置与请求日志，
  # 目录与文件都收窄到属主可读可进：原来的默认权限（0755 目录 + 0644 文件）
  # 让同机任何用户都能把整库读走。
  chmod 700 "$BACKUP_DIR"
  BACKUP_FILE="$BACKUP_DIR/pre-migrate-$(date +%Y%m%d-%H%M%S).sql"
  # 用 pg_dump 做逻辑备份：与数据库版本无关，恢复时不必先建同名容器
  if docker exec "${APP_NAME}-postgres" pg_dump -U "${DB_USER:-llmrelay}" -d "${DB_NAME:-llm_relay}" > "$BACKUP_FILE" 2>/dev/null; then
    gzip -f "$BACKUP_FILE"
    chmod 600 "${BACKUP_FILE}.gz"
    log "已备份到 ${BACKUP_FILE}.gz（$(du -h "${BACKUP_FILE}.gz" | cut -f1)）"
    log "恢复方式：见 deploy/restore.sh（备份在未压缩前也可直接用 psql 导入）"
    # 只留最近 10 份，避免长期升级把磁盘占满
    ls -1t "$BACKUP_DIR"/pre-migrate-*.sql.gz 2>/dev/null | tail -n +11 | xargs -r rm -f
  else
    rm -f "$BACKUP_FILE"
    warn "数据库备份失败，继续安装（升级风险自担）"
  fi
else
  log "没有正在运行的数据库容器，跳过备份（首次安装）"
fi

# ---------- 6. 构建镜像 ----------
# 只构建，不在这里启动 —— 启动交给第 7 步注册的 systemd 单元，
# 让「谁负责守护」只有一个答案。原来这里先 up -d 起容器、第 7 步只 enable 单元，
# 结果是单元从未被启动过：`systemctl is-active llm-relay` 报 inactive，
# 而容器明明在跑，任何人用 systemctl 判断服务状态都会得到错误结论。
log "构建镜像（首次构建较慢，请耐心等待）"
cd "$INSTALL_DIR/deploy"
docker compose --env-file "$ENV_FILE" build || die "docker compose 构建失败，请查看上面的日志"

# ---------- 7. 注册并启动 systemd 服务 ----------
log "注册 llm-relay.service（开机自启 + 进程守护）"
cat > /etc/systemd/system/llm-relay.service <<EOF
[Unit]
Description=LLM Relay - 本地大模型中转服务
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
# 用前台 up（不加 -d）而不是 oneshot + up -d。
#
# 两者都能把容器跑起来，区别在 systemd 眼里的「active」是什么意思：
#   oneshot + up -d  → 进程立刻退出，靠 RemainAfterExit 假装常驻。
#                      用户在命令行执行 docker compose down 之后，
#                      systemctl status 依旧显示 active，不报错也不重启 ——
#                      单元状态与真实状态完全脱钩。
#   前台 up          → compose 进程一直挂着（已实测：它 attach 到容器并常驻），
#                      容器没了它就退出，systemd 的 active/inactive 才是真的。
#
# 实测行为（在本机验证过）：前台 up 收到 SIGTERM 会优雅停掉容器；
# 容器正常停完后 compose 以 0 退出，单元转 inactive —— 此时
# systemctl status 说「没在跑」是对的。意外退出（非 0）才触发 Restart。
ExecStart=/usr/bin/docker compose --env-file $ENV_FILE up --no-color
ExecStop=/usr/bin/docker compose --env-file $ENV_FILE down
ExecReload=/usr/bin/docker compose --env-file $ENV_FILE restart
WorkingDirectory=$INSTALL_DIR/deploy
TimeoutStartSec=0
TimeoutStopSec=120
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
# enable --now 一步到位：既开机自启，也立刻用 systemd 拉起。
# 只 enable 不 start 是前面那个「inactive 却在跑」的直接原因。
systemctl enable --now llm-relay.service >/dev/null 2>&1 || true

# ---------- 8. 健康检查 ----------
log "等待服务就绪"
READY=no
for i in $(seq 1 45); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then READY=yes; break; fi
  sleep 2
done

WSL_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
echo
echo "============================================================"
if [[ "$READY" == "yes" ]]; then
  echo " LLM Relay 部署完成"
else
  echo " LLM Relay 已启动，但健康检查未通过"
  echo " 排查：cd $INSTALL_DIR/deploy && docker compose logs -f app"
fi
echo "------------------------------------------------------------"
echo " 访问地址   : http://localhost:$PORT"
# 不手工解析 .env 来推断绑定（env 优先级/引号/重复键与 compose 语义处处分歧），
# 直接回读 Docker 实际发布的地址 —— 容器没起来时回退到回环提示，宁可不报
PUB_BIND="$(cd "$INSTALL_DIR/deploy" && docker compose port app 8888 2>/dev/null | head -1 | cut -d: -f1 || true)"
case "$PUB_BIND" in
  ""|127.0.0.1|"::1"|localhost)
    echo " 局域网访问 : 在 deploy/.env 设 BIND_ADDR=0.0.0.0 后重启（管理接口无鉴权，自行加防火墙）" ;;
  0.0.0.0|"::")
    [[ -n "$WSL_IP" ]] && echo " 局域网地址 : http://$WSL_IP:$PORT" ;;
  *)
    echo " 局域网地址 : http://$PUB_BIND:$PORT" ;;
esac
echo " 安装目录   : $INSTALL_DIR"
echo " 端口       : $PORT"
echo
echo " 常用命令:"
echo "   systemctl status llm-relay"
echo "   systemctl restart llm-relay"
echo "   cd $INSTALL_DIR/deploy && docker compose logs -f app"
echo "   cd $INSTALL_DIR/deploy && docker compose down"
echo "============================================================"
