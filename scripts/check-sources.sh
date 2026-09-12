#!/usr/bin/env bash
set -uo pipefail
URLS="https://goproxy.cn/ https://registry.npmmirror.com/ https://dl-cdn.alpinelinux.org/alpine/v3.20/main/x86_64/APKINDEX.tar.gz https://proxy.golang.org/"
for u in $URLS; do
  d=$(curl -s -o /dev/null -w '%{http_code}' -m 15 "$u" 2>/dev/null)
  p=$(curl -s -o /dev/null -w '%{http_code}' -m 20 -x http://127.0.0.1:8118 "$u" 2>/dev/null)
  printf '%-72s direct=%-5s proxy=%s\n' "$u" "$d" "$p"
done
