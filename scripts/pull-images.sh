#!/usr/bin/env bash
# 带重试地拉取镜像（代理偶发瞬断）
set -uo pipefail
IMAGES="node:22-alpine alpine:3.20 postgres:16-alpine golang:1.25-alpine"

for img in $IMAGES; do
  printf '\n=== %s ===\n' "$img"
  if docker image inspect "$img" >/dev/null 2>&1; then
    echo "  已存在，跳过"
    continue
  fi
  ok=no
  for i in 1 2 3 4 5; do
    if docker pull "$img" >/tmp/pull.log 2>&1; then
      echo "  第 $i 次成功"
      ok=yes
      break
    fi
    echo "  第 $i 次失败：$(tail -1 /tmp/pull.log | cut -c1-140)"
    sleep 5
  done
  [ "$ok" = yes ] || echo "  !! 最终失败：$img"
done

echo
echo "=== 本地镜像清单 ==="
docker images --format '{{.Repository}}:{{.Tag}}  {{.Size}}'
echo "IMAGES_DONE"
