#!/usr/bin/env bash
# 探测各代理对容器镜像仓库的可达性
# 代理地址从环境变量读取；未设置对应项时跳过该项探测
HTTP_PROXY_URL="${UPSTREAM_HTTP:-}"
SOCKS_PROXY_URL="${UPSTREAM_SOCKS_S5:-}"

probe() {
  local label="$1"; shift
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' -m 25 "$@" 2>/dev/null)
  local rc=$?
  if [ "$code" = "000" ]; then
    printf '%-46s FAIL(rc=%s)\n' "$label" "$rc"
  else
    printf '%-46s HTTP %s\n' "$label" "$code"
  fi
}

echo "===== 直连 ====="
probe "direct registry-1"        https://registry-1.docker.io/v2/
probe "direct auth.docker.io"    https://auth.docker.io/token
probe "direct ghcr.io"           https://ghcr.io/v2/
probe "direct quay.io"           https://quay.io/v2/
probe "direct cloudflare CDN"    https://production.cloudflare.docker.com/

echo
echo "===== HTTP 代理 8088 ====="
probe "http8088 registry-1"      -x "$HTTP_PROXY_URL" https://registry-1.docker.io/v2/
probe "http8088 auth.docker.io"  -x "$HTTP_PROXY_URL" https://auth.docker.io/token
probe "http8088 ghcr.io"         -x "$HTTP_PROXY_URL" https://ghcr.io/v2/
probe "http8088 quay.io"         -x "$HTTP_PROXY_URL" https://quay.io/v2/
probe "http8088 cloudflare CDN"  -x "$HTTP_PROXY_URL" https://production.cloudflare.docker.com/

echo
echo "===== SOCKS5 代理 1080 ====="
probe "socks5 registry-1"        -x "$SOCKS_PROXY_URL" https://registry-1.docker.io/v2/
probe "socks5 auth.docker.io"    -x "$SOCKS_PROXY_URL" https://auth.docker.io/token
probe "socks5 cloudflare CDN"    -x "$SOCKS_PROXY_URL" https://production.cloudflare.docker.com/

echo
echo "===== 代理详细错误（HTTP 8088 -> registry-1）====="
curl -s -o /dev/null -v -m 20 -x "$HTTP_PROXY_URL" https://registry-1.docker.io/v2/ 2>&1 | grep -Ei 'connect|proxy|HTTP/|error|refused|denied|407|403' | head -12

echo
echo "===== DNS 解析 ====="
getent hosts registry-1.docker.io || echo "registry-1 DNS 解析失败"
getent hosts auth.docker.io || echo "auth DNS 解析失败"
