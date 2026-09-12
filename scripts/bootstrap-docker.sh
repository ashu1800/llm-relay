#!/usr/bin/env bash
# Docker Engine 引导安装（以 root 运行）
# 网络策略：先直连，失败自动改走 HTTP 代理；apt 仅对 download.docker.com 走代理。
set -uo pipefail

# 代理凭据从环境变量注入，避免明文入库。未设置时直接走直连。
PROXY_HTTP="${UPSTREAM_HTTP:-}"

log()  { printf '\n\033[1;36m[BOOT]\033[0m %s\n' "$*"; }
ok()   { printf '  \033[1;32mOK\033[0m %s\n' "$*"; }
warn() { printf '  \033[1;33m!!\033[0m %s\n' "$*"; }

# 直连优先，失败再走代理，各重试若干次
fetch() {
  local url="$1" outf="$2" i
  for i in 1 2 3; do
    if curl -fsSL --connect-timeout 12 -m 90 "$url" -o "$outf"; then ok "直连成功 $url"; return 0; fi
    if curl -fsSL --connect-timeout 12 -m 90 -x "$PROXY_HTTP" "$url" -o "$outf"; then ok "代理成功 $url"; return 0; fi
    warn "第 $i 次尝试失败，重试中"
    sleep 3
  done
  return 1
}

log "1/5 清理失效软链（Docker Desktop 残留）"
for p in /usr/bin/docker /usr/bin/docker-compose /usr/local/bin/docker /usr/bin/docker-credential-desktop.exe; do
  if [ -L "$p" ] && [ ! -e "$p" ]; then echo "  移除 $p"; rm -f "$p"; fi
done

log "2/5 配置 Docker apt 源"
install -m 0755 -d /etc/apt/keyrings
if ! fetch "https://download.docker.com/linux/ubuntu/gpg" /etc/apt/keyrings/docker.asc; then
  echo "GPG 密钥下载失败（网络不可达），终止"; exit 1
fi
chmod a+r /etc/apt/keyrings/docker.asc
CODENAME="$(. /etc/os-release && echo "$VERSION_CODENAME")"
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $CODENAME stable" > /etc/apt/sources.list.d/docker.list
ok "发行版代号 $CODENAME"

log "3/5 apt-get update"
APT_PROXY_OPT="-o Acquire::https::Proxy::download.docker.com=$PROXY_HTTP"
if ! apt-get update -qq $APT_PROXY_OPT 2>&1 | tail -4; then
  warn "带代理的 update 失败，尝试直连"
  apt-get update -qq 2>&1 | tail -4
fi

log "4/5 安装 docker-ce / compose 插件 / privoxy"
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $APT_PROXY_OPT \
  docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin privoxy 2>&1 | tail -6

log "5/5 版本确认"
docker --version || echo "docker 未安装成功"
docker compose version 2>/dev/null || echo "compose 插件缺失"
privoxy --version 2>/dev/null | head -1 || echo "privoxy 缺失"
echo "BOOT_DONE"
