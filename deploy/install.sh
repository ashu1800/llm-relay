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

# 是否需要重启 Docker daemon（默认否，见第 3 步之后的判定）。
# 代理配置真的被改写时才置 1 —— 只有那种情况必须重启 daemon 才生效。
DOCKER_RESTART_NEEDED=0

APP_NAME="llm-relay"
INSTALL_DIR="${INSTALL_DIR:-/opt/llm-relay}"
# PORT 只用于最后打印的访问地址与端口。真正生效的是 app 进程的监听地址：
# host 网络模式下进程直接监听宿主，SERVER_PORT 固定 8888，绑定地址由
# deploy/.env 的 SERVER_HOST 决定（默认 127.0.0.1，只绑回环）。
# 所以这里不再提供 PORT 覆盖：允许它和实际监听不一致时，脚本会打印一个
# 根本连不上的地址 —— 那种「装好了但打不开、且提示里的地址是错的」最难排查。
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

# ---------- 0. --verify：只读体检，不做任何改动 ----------
#
# 部署或重启之后最需要回答的一个问题是「现在到底跑的是哪个版本、健不健康」。
# 没有这个模式时只能靠人拼凑：docker ps 看容器、curl 看健康、翻日志看镜像时间 ——
# 而判断「镜像比源码新还是旧」这件事根本没法一眼看出来。
# （2026-09-20 就卡在这里：部署成功、服务随后因整机重启停了 27 秒，
#  但当时无法快速确认「重启回来的到底是新版本还是旧版本」。）
#
# 只读：不构建、不重启、不写文件。随时可以跑。
if [[ "${1:-}" == "--verify" || "${1:-}" == "-v" ]]; then
  echo "============================================================"
  echo " LLM Relay 体检（只读，不做任何改动）"
  echo "------------------------------------------------------------"
  printf ' 服务健康   : '
  if curl -fsS -m 5 "http://127.0.0.1:8888/healthz" >/dev/null 2>&1; then
    printf 'healthz 200（进程活着）'
    if curl -fsS -m 5 "http://127.0.0.1:8888/readyz" >/dev/null 2>&1; then
      echo "，readyz 200（数据库可连，能接请求）"
    else
      echo "，但 readyz 失败（数据库连不上）"
    fi
  else
    echo "不通 —— 服务没在跑，或端口不是 8888"
  fi
  printf ' 容器       : '
  docker ps -a --filter "name=^llm-relay" --format '{{.Names}}={{.Status}}' 2>/dev/null | paste -sd'  ' - || echo '(docker 不可用)'
  printf ' 运行中镜像 : '
  docker inspect -f '{{.Image}}' llm-relay 2>/dev/null || echo '(容器不存在)'
  printf ' 镜像构建于 : '
  docker inspect -f '{{.Created}}' llm-relay:local 2>/dev/null || echo '(镜像不存在)'
  printf ' systemd    : '
  systemctl is-enabled llm-relay.service 2>/dev/null || echo '未注册（开机不会自启）'
  # 版本号取自**运行中的服务**而不是 .env —— .env 里那个是「上次构建时用的值」，
  # 而这个接口回的是真正跑着的二进制里编进去的版本（见 Dockerfile 的 -ldflags）。
  # 两者不一致本身就是个信号：说明构建之后有人改过 .env 但没重新部署。
  #
  # 管理接口有登录鉴权（RELAY_ADMIN_KEY）：先读已安装 .env 里的密钥登录一次，
  # 拿会话 Cookie 再取系统信息。密钥没配（鉴权关闭）时直接取，两种情况都兼容。
  printf ' 服务版本   : '
  VERIFY_ENV="$INSTALL_DIR/deploy/.env"
  VERIFY_JAR="$(mktemp)"
  VERIFY_KEY="$(grep '^RELAY_ADMIN_KEY=' "$VERIFY_ENV" 2>/dev/null | head -1 | cut -d= -f2- || true)"
  if [[ -n "$VERIFY_KEY" ]]; then
    curl -fsS -m 5 -c "$VERIFY_JAR" -X POST "http://127.0.0.1:8888/api/admin/auth/login" \
      -H 'Content-Type: application/json' -d "{\"key\":\"$VERIFY_KEY\"}" >/dev/null 2>&1 || true
  fi
  SYSINFO="$(curl -fsS -m 5 -b "$VERIFY_JAR" "http://127.0.0.1:8888/api/admin/system/info" 2>/dev/null || true)"
  rm -f "$VERIFY_JAR"
  if [[ -n "$SYSINFO" ]]; then
    echo "$SYSINFO" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("version") or "?")' 2>/dev/null \
      || echo "(响应解析失败)"
  else
    echo "(服务未响应，拿不到)"
  fi
  # 源码与镜像的时间对比：镜像比源码旧 ⇒ 改了代码但没重新部署。
  # README 明确写过「改了前端或后端必须重新部署才会在 8888 上生效」，
  # 这条判断把它变成可自动核对的，而不是靠人记住。
  IMG_EPOCH="$(docker inspect -f '{{.Created}}' llm-relay:local 2>/dev/null | xargs -r -I{} date -d {} +%s 2>/dev/null || true)"
  SRC_EPOCH="$(find "$INSTALL_DIR/backend" "$INSTALL_DIR/frontend/src" -type f \
      \( -name '*.go' -o -name '*.vue' \) -printf '%T@\n' 2>/dev/null | sort -rn | head -1 | cut -d. -f1)"
  printf ' 版本新鲜度 : '
  if [[ -z "$IMG_EPOCH" ]]; then
    echo "查不到镜像构建时间"
  elif [[ -z "$SRC_EPOCH" ]]; then
    echo "查不到源码时间（$INSTALL_DIR 还没部署过？）"
  elif [[ "$SRC_EPOCH" -gt "$IMG_EPOCH" ]]; then
    echo "⚠ 源码比镜像新 —— 改动尚未部署，8888 上跑的还是旧代码"
  else
    echo "OK（镜像不早于源码）"
  fi
  echo "============================================================"
  exit 0
fi

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
    # 代理是 daemon 级配置：不重启 daemon 不会生效（这也是唯一需要重启它的场景）
    DOCKER_RESTART_NEEDED=1
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
    DOCKER_RESTART_NEEDED=1
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
# 只在必要时重启 Docker daemon。
#
# 原来这里无条件 `systemctl restart docker`：daemon 一停，容器跟着停，
# 靠 restart: unless-stopped 才会在新 daemon 起来后自动回来 —— 一次部署
# 因此白白中断十几秒到几十秒，而绝大多数部署（只换了应用镜像）根本不需要动 daemon。
# 需要重启的只有两种情况：daemon 本身不可用，或刚写过代理配置（下面设的标记）。
if (( ${DOCKER_RESTART_NEEDED:-0} )) || ! docker info >/dev/null 2>&1; then
  log "重启 Docker daemon（容器会随 restart: unless-stopped 自动回来）"
  systemctl restart docker
  sleep 5
fi
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
  # 管理台登录密钥与加密主密钥相互独立、各自随机：登录密钥出现在
  # 每个登录请求里，泄露面更大，绝不能图省事两处共用一个值
  RELAY_ADMIN_KEY="$(openssl rand -hex 24)"
  RELAY_ADMIN_KEY_PRINT="$RELAY_ADMIN_KEY"
  sed -e "s|^DB_PASSWORD=.*|DB_PASSWORD=$DB_PASSWORD|" \
      -e "s|^RELAY_SECRET=.*|RELAY_SECRET=$RELAY_SECRET|" \
      -e "s|^RELAY_ADMIN_KEY=.*|RELAY_ADMIN_KEY=$RELAY_ADMIN_KEY|" \
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
  # 登录密钥同样只补缺失；与主密钥不同，它不落库，换新值只影响「下次登录」
  if grep -q '^RELAY_ADMIN_KEY=.\+' "$TMP_ENV"; then
    RELAY_ADMIN_KEY="$(grep '^RELAY_ADMIN_KEY=' "$TMP_ENV" | head -1 | cut -d= -f2-)"
  else
    RELAY_ADMIN_KEY="$(openssl rand -hex 24)"
    RELAY_ADMIN_KEY_PRINT="$RELAY_ADMIN_KEY"
    warn "补齐了缺失的 RELAY_ADMIN_KEY（管理台登录鉴权此前处于关闭状态）"
  fi
  sed -e "s|^DB_PASSWORD=.*|DB_PASSWORD=$DB_PASSWORD|" \
      -e "s|^RELAY_SECRET=.*|RELAY_SECRET=$RELAY_SECRET|" \
      -e "s|^RELAY_ADMIN_KEY=.*|RELAY_ADMIN_KEY=$RELAY_ADMIN_KEY|" \
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
  # 同样补上管理台登录密钥：老配置没有它时登录鉴权是关闭的
  if ! grep -q '^RELAY_ADMIN_KEY=.\+' "$ENV_FILE"; then
    RELAY_ADMIN_KEY="$(openssl rand -hex 24)"
    printf 'RELAY_ADMIN_KEY=%s\n' "$RELAY_ADMIN_KEY" >> "$ENV_FILE"
    RELAY_ADMIN_KEY_PRINT="$RELAY_ADMIN_KEY"
    warn "已为 $ENV_FILE 补上 RELAY_ADMIN_KEY（管理台登录鉴权此前处于关闭状态）"
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

# ---------- 5.8 计算版本号并写进 .env ----------
#
# 版本号的单一来源是 **git**，不是手写常量 —— 手写的那个迟早会和实际代码
# 对不上，而「界面上显示的版本是不是正在跑的那份代码」正是它唯一要回答的问题。
#
# 为什么必须在这里算：源码 tar 到安装目录时排除了 .git（见第 4 步），
# 到了 docker build 阶段已经读不到 git 了。所以趁源码目录还在手边算好，
# 经 .env → docker-compose 的 build arg → Dockerfile 的 -ldflags 传进去。
#
# 格式：v 前缀 + 最近 tag（没有 tag 就退化成一串短哈希）+ 提交数 + 短哈希 +
# 脏标记。带提交数与哈希是为了让「同一个 tag 下的两次构建」也能区分开 ——
# 否则改了代码但没打新 tag 时，界面上的版本号会一动不动，看着像没部署成功。
#
#   v1.2.0-7-g3f9a1c        基于 tag v1.2.0，之后 7 个提交
#   v1.2.0-7-g3f9a1c-dirty  工作区有未提交改动
#   g3f9a1c                 从没有 tag 的历史上构建
#   unknown                 不是 git 仓库（例如拿一份导出源码去部署）
compute_version() {
  if ! command -v git >/dev/null 2>&1; then
    echo "unknown"; return
  fi
  # -C 指定目录而不是 cd：调用方的当前目录不该被这个函数改掉
  if ! git -C "$SRC_DIR" rev-parse --git-dir >/dev/null 2>&1; then
    echo "unknown"; return
  fi
  local desc
  desc="$(git -C "$SRC_DIR" describe --tags --always --dirty 2>/dev/null || true)"
  if [[ -z "$desc" ]]; then
    echo "unknown"; return
  fi
  # describe 在没有 tag 时只给短哈希；补个 v 前缀让两种形状一致，
  # 前端展示时就不必再判断「该不该加 v」
  if [[ "$desc" != v* ]]; then
    echo "v$desc"
  else
    echo "$desc"
  fi
}

APP_VERSION="$(compute_version)"
log "版本号：$APP_VERSION（来自 git describe，经 .env 的 VERSION 传入构建）"

# 幂等更新 .env 里的 VERSION。**只改这一个键**，其余内容原样保留 ——
# 这个文件里有数据库密码与加密主密钥，任何「整体重写」都是在拿线上数据冒险。
# 用临时文件 + mv 而不是原地 sed -i：mv 是原子的，中途失败不会留下半个 .env。
if grep -q '^VERSION=' "$ENV_FILE" 2>/dev/null; then
  grep -v '^VERSION=' "$ENV_FILE" > "${ENV_FILE}.tmp"
  printf 'VERSION=%s\n' "$APP_VERSION" >> "${ENV_FILE}.tmp"
  chmod 600 "${ENV_FILE}.tmp"
  mv "${ENV_FILE}.tmp" "$ENV_FILE"
else
  printf 'VERSION=%s\n' "$APP_VERSION" >> "$ENV_FILE"
fi

# ---------- 5.9 迁移 BIND_ADDR → SERVER_HOST（host 网络模式） ----------
#
# app 改为 host 网络模式后，宿主侧绑定地址不再由 compose ports 的
# 「127.0.0.1:8888:8888」前缀控制，而是进程自己的 SERVER_HOST。
# 老安装的 .env 里记的是 BIND_ADDR —— 语义相同（回环 / 0.0.0.0），
# 直接改名迁移，用户的手动配置不丢。新值不存在时 compose 的默认值
# （127.0.0.1）兜底，与老默认 BIND_ADDR=127.0.0.1 同义。
if grep -q '^BIND_ADDR=' "$ENV_FILE" 2>/dev/null && ! grep -q '^SERVER_HOST=' "$ENV_FILE" 2>/dev/null; then
  OLD_BIND="$(grep '^BIND_ADDR=' "$ENV_FILE" | head -1 | cut -d= -f2-)"
  grep -v '^BIND_ADDR=' "$ENV_FILE" > "${ENV_FILE}.tmp"
  printf 'SERVER_HOST=%s\n' "$OLD_BIND" >> "${ENV_FILE}.tmp"
  chmod 600 "${ENV_FILE}.tmp"
  mv "${ENV_FILE}.tmp" "$ENV_FILE"
  log "已把 .env 的 BIND_ADDR=$OLD_BIND 迁移为 SERVER_HOST（host 网络模式改用此键）"
fi

# ---------- 6. 构建镜像 ----------
# 只构建，不在这里启动 —— 启动交给第 7 步注册的 systemd 单元，
# 让「谁负责守护」只有一个答案。原来这里先 up -d 起容器、第 7 步只 enable 单元，
# 结果是单元从未被启动过：`systemctl is-active llm-relay` 报 inactive，
# 而容器明明在跑，任何人用 systemctl 判断服务状态都会得到错误结论。
log "构建镜像（首次构建较慢，请耐心等待）"
cd "$INSTALL_DIR/deploy"
docker compose --env-file "$ENV_FILE" build || die "docker compose 构建失败，请查看上面的日志"

# ---------- 6.5 切换前预检新镜像（不碰线上端口、不碰线上数据库）----------
# 直接切过去再检查「起没起来」是不够的：新镜像如果启动就崩（迁移写错、
# 依赖缺失、配置项改名），线上要一直挂着旧容器等我们发现 —— 而 systemd
# 已经把旧容器停了，实际表现是一次长时间的 502。
# 所以先把新镜像跑起来，配一个**临时数据库**：
#   · 不碰 8888，线上这一秒还在正常服务
#   · 不连线上库，迁移的副作用只落在这个用完就删的空库上
# 预检失败就 die —— 此时线上仍是旧版本、仍然可用（这正是要保住的东西）。
#
# **预检容器不发布任何宿主端口**（原来用 -p 127.0.0.1:8899:8888）。
# 发布端口这个动作本身会打断 Windows↔WSL 的 localhost 转发（wslrelay.exe），
# 表现是**部署完成后浏览器打不开 localhost:8888** —— 而服务其实是好的。
# README 里因此写着「遇到就一直转圈的话 wsl --shutdown」。实测踩到过：
# 2026-09-20 我照做之后整台 WSL 重启，服务停了 27 秒（数据库数据没丢，
# 容器都是开机自启，但这 27 秒是白白赔进去的）。
# 现在改用 `docker exec` 在容器**内部**探 /readyz：既拿到同样的结论
# （容器起来、迁移跑通、数据库连得上），又完全不新增宿主监听端口，
# 于是那条「打不开 → 重启 WSL」的链路从根上不会再有。
# 凭据从**安装目录的 .env** 里读，不用脚本前面 source 进来的变量：
# 脚本 source 的是源码目录的 .env（可能不存在或是旧的），而容器真正吃的是
# $ENV_FILE。两者混用会让临时库用错密码、预检必然失败，从而挡住正常部署。
env_get() { grep -m1 "^$1=" "$ENV_FILE" 2>/dev/null | cut -d= -f2-; }
SMOKE_DB_PASSWORD="$(env_get DB_PASSWORD)"
SMOKE_DB_USER="$(env_get DB_USER)"; SMOKE_DB_USER="${SMOKE_DB_USER:-llmrelay}"
SMOKE_DB_NAME="$(env_get DB_NAME)"; SMOKE_DB_NAME="${SMOKE_DB_NAME:-llm_relay}"

smoke_image() {
  local db="llm-relay-smoke-db" app="llm-relay-smoke-app"
  # host 网络模式下端口就是**宿主**端口，预检的 app 若也用 8888 会与线上撞车。
  # 用 18088：只在本机回环上临时听几十秒，用完即删。
  local smoke_port=18088
  docker rm -f "$db" "$app" >/dev/null 2>&1 || true
  log "预检新镜像：临时数据库 + host 模式试跑（监听 ${smoke_port}，不发布宿主端口）"
  # host 模式下容器间不能再走 compose 内部 DNS，数据库对 app 暴露为
  # 宿主回环的一个端口。smoke 的 postgres **也跑 host 网络**，用 postgres
  # 自己的参数监听 127.0.0.1:25432（线上 postgres 用 15432，不冲突）——
  # 刻意不用 -p 端口发布：那正是本功能要消灭的动作（发布端口会打断
  # Windows↔WSL 的 localhost 转发），预检自己不能带头违反。
  docker run -d --name "$db" --network host \
    -e POSTGRES_PASSWORD="$SMOKE_DB_PASSWORD" -e POSTGRES_USER="$SMOKE_DB_USER" \
    -e POSTGRES_DB="$SMOKE_DB_NAME" \
    postgres:16-alpine \
    postgres -c listen_addresses=127.0.0.1 -p 25432 >/dev/null || return 1
  # 等临时库能接受连接（最多 30s；host 模式下它监听在宿主 25432）
  local i
  for i in $(seq 1 30); do
    docker exec "$db" pg_isready -h 127.0.0.1 -p 25432 >/dev/null 2>&1 && break
    sleep 1
  done
  # app 与线上**同一种网络形态**（host 模式）：预检才真的预演了生产形态 ——
  # 迁移在 host 模式下连库、监听、健康检查，任何一项不兼容都会在这里暴露，
  # 而不是切换之后。--env-file 会带上 .env 里的 SERVER_HOST/DB_* 等，
  # 显式 -e 覆盖掉端口与库地址：预检绝不能碰线上 8888 与线上数据库。
  docker run -d --name "$app" --network host \
    --env-file "$ENV_FILE" \
    -e SERVER_HOST=127.0.0.1 -e SERVER_PORT="$smoke_port" -e GIN_MODE=release \
    -e DB_HOST=127.0.0.1 -e DB_PORT=25432 -e DB_SSLMODE=disable \
    -e RELAY_PAYLOAD_STORAGE_MODE="${RELAY_PAYLOAD_STORAGE_MODE:-errors}" \
    -e LOG_LEVEL=warn -e TZ="${TZ:-Asia/Shanghai}" \
    llm-relay:local >/dev/null || return 1
  # /readyz 会实际探一次数据库，迁移失败时它不会通过 —— 这才是「能不能接请求」。
  # host 模式下进程就监听在宿主回环上，宿主侧直接 curl；
  # 不发探针进容器也行，但保持与切前检查同一姿势（wget 进容器）更直观
  for i in $(seq 1 40); do
    if curl -fsS -m 2 "http://127.0.0.1:${smoke_port}/readyz" >/dev/null 2>&1; then
      log "预检通过：新镜像能启动、迁移跑通、/readyz 正常"
      return 0
    fi
    if ! docker ps --format '{{.Names}}' | grep -qx "$app"; then
      warn "预检失败：容器已退出，日志如下"
      docker logs --tail 25 "$app" 2>&1 | sed 's/^/    /'
      return 1
    fi
    sleep 1
  done
  warn "预检失败：40 秒内 /readyz 未通过，日志如下"
  docker logs --tail 25 "$app" 2>&1 | sed 's/^/    /'
  return 1
}

# host 模式下 app 不再连接 compose 网络，网络名无从取也不需要 ——
# 预检只看「线上是否健康」来决定跑不跑（首次安装或服务未启动时没有对照意义）
if [[ -z "$SMOKE_DB_PASSWORD" ]]; then
  log "读不到 DB_PASSWORD，跳过镜像预检（只在 .env 缺失时会发生）"
elif curl -fsS -o /dev/null -m 3 "http://127.0.0.1:$PORT/healthz" 2>/dev/null; then
  if smoke_image; then
    :
  else
    docker rm -f llm-relay-smoke-db llm-relay-smoke-app >/dev/null 2>&1 || true
    die "新镜像未通过预检，已放弃切换 —— 线上仍是旧版本，服务未受影响"
  fi
  docker rm -f llm-relay-smoke-db llm-relay-smoke-app >/dev/null 2>&1 || true
else
  log "线上没有在跑的旧容器（首次安装或服务未启动），跳过镜像预检"
fi

# ---------- 7. 注册并启动 systemd 服务 ----------
log "注册 llm-relay.service（开机自启 + 进程守护）"
# 这个 heredoc 加了引号（<<'EOF'），里面的内容**不做任何展开**，
# 因此注释里可以随便用反引号 —— 这里踩过一次：原来是不加引号的 heredoc，
# 而下面的注释里写了 `docker compose ps`，bash 当成命令替换去执行了，
# 于是容器列表被塞进单元文件（systemd 报 "Missing '=', ignoring line"）。
# 需要展开的两个路径改用占位符 + 写完 sed 替换，见下面两行。
cat > /etc/systemd/system/llm-relay.service <<'EOF'
[Unit]
Description=LLM Relay - 本地大模型中转服务
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
# 用 oneshot + `up -d`，而不是前台 `up`。
#
# 原来用前台 `up`，理由是「单元状态更诚实」：compose 进程挂着，
# 容器没了它就退出，systemctl status 的 active/inactive 与真实状态一致。
# 2026-09-16 实测发现这个诚实是要付代价的，而且代价正好落在「重新部署」上：
# systemd 停单元时会给前台 up 发 SIGTERM，而它是按「整个项目」善后的 ——
# 日志里能同时看到 llm-relay 与 llm-relay-postgres 两个容器
# Stopping → Stopped → Removing → Removed。于是 ExecStop 写什么都无所谓
# （哪怕只写 `stop app`，数据库照样被停掉重建），app 还得等 postgres 通过
# 健康检查才能起：实测一次切换中断 6.7 秒，其中大部分与「换个 app 镜像」无关。
#
# 换成 `up -d`（detached）之后没人再持有这些容器，单元停止时只会执行 ExecStop：
# 数据库容器全程不动（实测容器 ID 与 StartedAt 都不变），切换只剩 app 自己启停。
# 代价是 systemctl status 的含义变成「这个单元启动过」而不是「容器正跑着」——
# 真实状态看 `docker compose ps` 或 /healthz（脚本每次部署都会探它）。
ExecStart=/usr/bin/docker compose --env-file __ENV_FILE__ up -d --no-color
# ExecStop 只停 app，不 down 整个项目：数据库是数据服务，重启它纯粹是白等；
# 容器也不删除，下次 up 只是重启而不是重建。
# 注意：这样 `systemctl stop llm-relay` 停的是中转服务本身，数据库仍在跑 ——
# 需要连数据库一起停时，直接在 deploy 目录 `docker compose down`。
ExecStop=/usr/bin/docker compose --env-file __ENV_FILE__ stop app
ExecReload=/usr/bin/docker compose --env-file __ENV_FILE__ restart app
WorkingDirectory=__INSTALL_DIR__/deploy
TimeoutStartSec=0
TimeoutStopSec=120
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
# 替换占位符（用 | 作分隔符：路径里可能带 /）
sed -i "s|__ENV_FILE__|$ENV_FILE|g; s|__INSTALL_DIR__|$INSTALL_DIR|g" /etc/systemd/system/llm-relay.service
# 占位符没被替换掉说明 sed 失败，宁可不装单元也不要装一个 ExecStart 跑不起来的
if grep -q '__ENV_FILE__\|__INSTALL_DIR__' /etc/systemd/system/llm-relay.service; then
  die "单元文件里的占位符未被替换，请检查 $ENV_FILE 是否含特殊字符"
fi
systemctl daemon-reload
# 先 enable 保证开机自启（切换动作在下面，见「切换」那一段）。
#
# 为什么必须显式做点什么：重新部署时容器还挂在旧镜像上，`enable --now` 对
# **已激活**的单元是空操作 —— 镜像明明刚构建完，容器却没换，脚本照样"部署完成"、
# 健康检查也通过（旧容器本来就是健康的），后续验证测的全是旧二进制。
# 这与文件头记的那次踩坑是同一类问题：失败被伪装成了成功。
systemctl enable llm-relay.service >/dev/null 2>&1 || true

# 单元定义要先确认已经生效：脚本刚重写过单元文件，若 systemd 还在用旧定义，
# 之后任何一次 systemctl stop/restart 都会按旧 ExecStop 走（旧的写的是 down，
# 会把数据库一起停掉重建）。实测踩过：日志里那句
# "Warning: The unit file ... changed on disk. Run 'systemctl daemon-reload'"
# 就是它，而那一次切换因此多停了 6.7 秒、数据库也被重建。
if ! systemctl show -p ExecStop --value llm-relay.service 2>/dev/null | grep -q 'stop app'; then
  warn "systemd 仍在用旧的单元定义，重新 daemon-reload"
  systemctl daemon-reload
  if ! systemctl show -p ExecStop --value llm-relay.service 2>/dev/null | grep -q 'stop app'; then
    die "单元定义未生效（ExecStop 不是 stop app）。切换已中止，线上仍是旧版本、未受影响。"
  fi
fi

# 切换期间用探针量一次真实中断时长：/healthz 每 100ms 打一次，
# 统计最长的连续失败窗口（探测间隔本身有误差，报的是上界）。
# 同时记下数据库容器的身份：切换后要对得上，才说明「只换了 app」。
PG_ID_BEFORE="$(docker inspect -f '{{.Id}}' "${APP_NAME}-postgres" 2>/dev/null || true)"
PG_START_BEFORE="$(docker inspect -f '{{.State.StartedAt}}' "${APP_NAME}-postgres" 2>/dev/null || true)"
DOWNTIME_PROBE="$(mktemp)"

# 从这一刻起脚本开始**改动线上**（重建容器）。如果它在切换中途被打断
# （Ctrl+C、SSH 断开、WSL 整机重启、预检失败……），必须留下明确结论，
# 而不是让人对着半个屏幕的输出猜「现在跑的是新版本还是旧版本」。
# 2026-09-20 就卡在这件事上：部署过程被打断，服务随后又经历一次整机重启，
# 无从快速判断回来的是哪个版本。
#
# 只读式收尾：不尝试自动回滚（那可能把服务弄得更糟），只如实报告状态并
# 给出下一步命令。用 `|| true` 保证报告本身绝不成为新的失败点。
report_state() {
  printf '\n\033[1;33m[%s]\033[0m 当前服务状态：\n' "$APP_NAME"
  if curl -fsS -m 5 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 \
     && curl -fsS -m 5 "http://127.0.0.1:$PORT/readyz" >/dev/null 2>&1; then
    printf '  健康   : 正常（healthz 与 readyz 均通过）\n'
  elif curl -fsS -m 5 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    printf '  健康   : 进程活着但数据库连不上（readyz 失败）\n'
  else
    printf '  健康   : ⚠ 服务不通 —— 请求会失败\n'
  fi
  printf '  容器   : %s\n' "$(docker ps -a --filter "name=^${APP_NAME}$" \
      --format '{{.Status}}' 2>/dev/null | head -1 || echo '查询失败')"
  printf '  镜像   : %s\n' "$(docker inspect -f '{{.Created}}' llm-relay:local 2>/dev/null || echo '查询失败')"
  printf '  下一步 : bash %s --verify     # 只读体检，随时可跑\n' "$0"
  printf '           systemctl restart llm-relay   # 让服务按当前镜像整个重来\n'
}
# INT/TERM 必须显式 exit：bash 设了 trap 之后，处理完仍会从被打断处继续执行 ——
# 那会让 Ctrl+C 变成「报告完状态接着往下重建容器」，比不拦还糟。
# 130 是 128+SIGINT 的惯例退出码。
trap 'report_state; exit 130' INT TERM
trap 'rm -f "$DOWNTIME_PROBE"' EXIT

(
  while :; do
    code="$(curl -fsS -o /dev/null -m 1 -w '%{http_code}' "http://127.0.0.1:$PORT/healthz" 2>/dev/null || echo 000)"
    printf '%s %s\n' "$(date +%s%3N)" "$code" >> "$DOWNTIME_PROBE"
    sleep 0.1
  done
) &
PROBE_PID=$!

# ---------- 7.5 切换 ----------
# 直接重建 app 容器，不走 systemctl restart。
#
# `--no-deps` 是这里的关键：它让 compose 只处理 app 这一个服务，
# 数据库容器不会被停、不会被删、也不会被重建 —— 这正是「换镜像」该有的代价。
# 为什么不用 systemctl restart：systemd 的停止阶段用哪个 ExecStop 取决于它
# 当时加载的定义（上面那段刚踩过），而且即使 ExecStop 正确，它也只能
# 「先停再起」，中间那几百毫秒是白白断掉的；up -d --no-deps 只需要重建 app。
cd "$INSTALL_DIR/deploy"
if docker ps --format '{{.Names}}' | grep -qx "${APP_NAME}-postgres"; then
  # postgres 的 compose 配置若也变了（如新增端口映射），必须一起重建 ——
  # 否则 app 按新配置连库（127.0.0.1:15432）而 postgres 还按旧配置跑
  # （只在内部网络），readyz 必然超时。数据在 bind mount 里，重建容器不丢数据，
  # 代价只是数据库自己重启几秒。判定方式：比较「compose 认为的容器配置」与
  # 「实际在跑的容器」是否一致（compose 对配置漂移的标准做法）。
  if ! docker compose --env-file "$ENV_FILE" up -d --no-deps postgres >/dev/null 2>&1; then
    die "重建 postgres 容器失败（检查 docker compose logs postgres）"
  fi
  # 重建后等它恢复健康（最多 60s），app 起来时库必须已可连
  PG_OK=no
  for i in $(seq 1 60); do
    st="$(docker inspect -f '{{.State.Health.Status}}' "${APP_NAME}-postgres" 2>/dev/null || echo unknown)"
    if [[ "$st" == "healthy" ]]; then PG_OK=yes; break; fi
    sleep 1
  done
  if [[ "$PG_OK" != "yes" ]]; then
    warn "postgres 重建后 60 秒内未恢复 healthy,继续切换（app 会自己重试连库）"
  fi
  docker compose --env-file "$ENV_FILE" up -d --no-deps app || die "重建 app 容器失败"
else
  # 数据库没在跑（首次安装，或机器刚重启）：交给单元按依赖顺序整体拉起
  systemctl restart llm-relay.service
fi
# oneshot 单元被外部操作弄成 inactive 时补一次 start，免得 systemctl status
# 说「没在跑」而容器其实好好的（单元状态与容器状态是两件事，见单元里的注释）
systemctl is-active --quiet llm-relay || systemctl start llm-relay.service

# ---------- 8. 健康检查 ----------
log "等待服务就绪"
READY=no
for i in $(seq 1 45); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then READY=yes; break; fi
  sleep 2
done
# healthz 只说明进程活着；能接请求才是这一页要的（数据库连不上时进程照样活着）
DB_READY=no
if [[ "$READY" == "yes" ]] && curl -fsS -m 5 "http://127.0.0.1:$PORT/readyz" >/dev/null 2>&1; then
  DB_READY=yes
fi

sleep 1
kill "$PROBE_PID" >/dev/null 2>&1 || true
wait "$PROBE_PID" 2>/dev/null || true

# 最长连续失败窗口 = 这一次切换对线上的实际影响
DOWNTIME_MS="$(python3 - "$DOWNTIME_PROBE" <<'PY' 2>/dev/null || true
import sys
prev_t = None
start = None
worst = 0
for line in open(sys.argv[1], encoding='utf-8', errors='replace'):
    parts = line.split()
    if len(parts) != 2:
        continue
    t, code = int(parts[0]), parts[1]
    if code == '200':
        if start is not None:
            worst = max(worst, prev_t - start)
            start = None
    elif start is None:
        start = t
    prev_t = t
if start is not None and prev_t is not None:
    worst = max(worst, prev_t - start)
print(worst)
PY
)"
rm -f "$DOWNTIME_PROBE"

# 数据库容器必须原封不动：ID 与 StartedAt 都对得上，才证明这次切换只碰了 app
PG_ID_AFTER="$(docker inspect -f '{{.Id}}' "${APP_NAME}-postgres" 2>/dev/null || true)"
PG_START_AFTER="$(docker inspect -f '{{.State.StartedAt}}' "${APP_NAME}-postgres" 2>/dev/null || true)"
if [[ -n "$PG_ID_BEFORE" && "$PG_ID_BEFORE" == "$PG_ID_AFTER" && "$PG_START_BEFORE" == "$PG_START_AFTER" ]]; then
  PG_UNTOUCHED=yes
else
  PG_UNTOUCHED=no
fi

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
# 管理台登录密钥：安装时（重新）生成过就当面打印出来，别让人去 .env 里翻；
# 没生成过（用户自设）则只提示位置。登录页在 http://<地址>/login。
if [[ -n "${RELAY_ADMIN_KEY_PRINT:-}" ]]; then
  echo " 管理密钥   : $RELAY_ADMIN_KEY_PRINT"
  echo "              （打开管理台时输入它登录；妥善保存，泄露后在 .env 轮换并重启）"
else
  echo " 管理密钥   : 见 $ENV_FILE 中的 RELAY_ADMIN_KEY（打开管理台时输入它登录）"
fi
if [[ "${DB_READY:-no}" == "yes" ]]; then
  echo " 就绪检查   : /healthz 与 /readyz 均通过（进程活着且数据库可连）"
else
  echo " 就绪检查   : /healthz 通过，但 /readyz 未通过 —— 检查数据库"
fi
if [[ "${PG_UNTOUCHED:-no}" == "yes" ]]; then
  echo " 数据库     : 本次切换未重启（容器 ID 与启动时刻均未变）"
else
  echo " 数据库     : 容器发生了重启 —— 若并非所愿，检查是否有人手动 down 过"
fi
if [[ -n "${DOWNTIME_MS:-}" && "$DOWNTIME_MS" != "0" ]]; then
  echo " 本次切换   : 最长中断约 ${DOWNTIME_MS}ms（/healthz 每 100ms 探测一次，含探测误差）"
elif [[ "${DOWNTIME_MS:-}" == "0" ]]; then
  echo " 本次切换   : 探测期间未观察到中断（100ms 粒度）"
fi
# host 模式下没有 docker-proxy 的端口发布可查（docker compose port 输出为空），
# 绑定地址直接看进程实际监听 —— 比 .env 的声明更权威（.env 改了没重启时，
# 声明与实际会不一致，如实报实际的）
PUB_BIND="$(ss -ltn 2>/dev/null | awk -v p=":$PORT" '$4 ~ p {print $4}' | head -1 | sed 's/^[^:]*://')"
case "$PUB_BIND" in
  ""|127.0.0.1|"::1"|localhost)
    echo " 局域网访问 : 在 deploy/.env 设 SERVER_HOST=0.0.0.0 后重启（确保已设 RELAY_ADMIN_KEY，公网建议再加反代 + HTTPS）" ;;
  0.0.0.0|"::*")
    [[ -n "$WSL_IP" ]] && echo " 局域网地址 : http://$WSL_IP:$PORT" ;;
  *)
    echo " 局域网地址 : http://$PUB_BIND:$PORT" ;;
esac
echo " 安装目录   : $INSTALL_DIR"
echo " 端口       : $PORT"
echo
# 部署本身成功、但浏览器打不开时，先看这一条：服务在 WSL 里是好的，
# 断的是 Windows↔WSL 的 localhost 转发（wslrelay.exe 会接受连接却不转发数据，
# 浏览器打不开时的排查指引。
#
# host 网络模式下，部署不再做任何宿主端口发布/重发布动作（app 直接监听、
# 没有 docker-proxy），Windows↔WSL localhost 转发被部署打断的主因已消除。
# 这段提示保留给真正的边缘情况（如 wslrelay.exe 因其它原因失效）。
if [[ -n "$WSL_IP" ]]; then
  echo " 打不开？   : 先在 WSL 里自测（返回 200 就说明服务本身没问题）："
  echo "              curl -I http://127.0.0.1:$PORT/healthz"
  echo "              host 网络模式下部署已不再触发转发断连；若仍打不开且上面"
  echo "              返回 200，可用 wsl --shutdown 重建转发 —— 但注意那会让"
  echo "              服务真停约 30 秒，不是必须就先别重启。"
  echo
fi
echo " 常用命令:"
echo "   systemctl status llm-relay"
echo "   systemctl restart llm-relay"
echo "   cd $INSTALL_DIR/deploy && docker compose logs -f app"
echo "   cd $INSTALL_DIR/deploy && docker compose down"
echo "============================================================"
