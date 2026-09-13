#!/usr/bin/env bash
# 配置 Docker daemon 经 privoxy(socks5->http) 访问外网，并启动 Docker
set -uo pipefail
SOCKS="${UPSTREAM_SOCKS:-}"
[[ -n "$SOCKS" ]] || { echo "请先 export UPSTREAM_SOCKS=user:pass@host:port"; exit 1; }
HTTP_PORT=8118
log() { printf '\n\033[1;36m[CFG]\033[0m %s\n' "$*"; }

log "1/5 配置 privoxy 转发规则"
if ! grep -q 'llm-relay-forward' /etc/privoxy/config 2>/dev/null; then
  cp -n /etc/privoxy/config /etc/privoxy/config.orig 2>/dev/null || true
  printf '\n# llm-relay-forward\nforward-socks5t / %s .\n' "$SOCKS" >> /etc/privoxy/config
fi
grep -n 'llm-relay-forward' -A1 /etc/privoxy/config | tail -2

# 确保监听地址正确
sed -i 's/^listen-address.*/listen-address  127.0.0.1:8118/' /etc/privoxy/config
grep -n '^listen-address' /etc/privoxy/config | head -2

log "2/5 启动 privoxy"
systemctl enable privoxy >/dev/null 2>&1 || true
systemctl restart privoxy
sleep 2
systemctl is-active privoxy

log "3/5 通过 privoxy 验证 Docker Hub 可达性"
curl -s -o /dev/null -w "  privoxy -> registry-1: HTTP %{http_code}\n" -m 30 -x http://127.0.0.1:8118 https://registry-1.docker.io/v2/ || echo "  探测失败"
curl -s -o /dev/null -w "  privoxy -> auth.docker: HTTP %{http_code}\n" -m 30 -x http://127.0.0.1:8118 https://auth.docker.io/token || true

log "4/5 配置 Docker daemon 代理"
mkdir -p /etc/systemd/system/docker.service.d
cat > /etc/systemd/system/docker.service.d/http-proxy.conf <<'EOF'
[Service]
Environment="HTTP_PROXY=http://127.0.0.1:8118"
Environment="HTTPS_PROXY=http://127.0.0.1:8118"
Environment="NO_PROXY=localhost,127.0.0.1,::1,postgres,llm-relay"
EOF
systemctl daemon-reload

log "5/5 启动 Docker"
systemctl enable docker >/dev/null 2>&1 || true
systemctl restart docker
sleep 6
systemctl is-active docker
docker info --format '{{.ServerVersion}} / storage={{.Driver}}' 2>&1 | head -2
echo "CFG_DONE"
