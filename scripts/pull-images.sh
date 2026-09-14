#!/usr/bin/env bash
# 带重试地拉取镜像（代理偶发瞬断）
set -uo pipefail
# 这一行必须与 Dockerfile / docker-compose.yml 里的版本保持一致。
# 原来是 alpine:3.20，而 Dockerfile 已经升到 3.24 —— 两边不一致的后果是
# 这个「预热镜像」脚本拉的是一个构建时不会用到的版本：
# 真到离线构建时才发现要用的那个还是没缓存。
IMAGES="node:22-alpine alpine:3.24 postgres:16-alpine golang:1.25-alpine"

failed=""
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
  if [ "$ok" != yes ]; then
    echo "  !! 最终失败：$img"
    failed="$failed $img"
  fi
done

echo
echo "=== 本地镜像清单 ==="
docker images --format '{{.Repository}}:{{.Tag}}  {{.Size}}'

# 有镜像没拉下来时不能报成功。
# 原来无论成败都打印 IMAGES_DONE 且退出码为 0，于是「预热失败」
# 和「预热成功」在调用方看来一模一样 —— 而失败的那次会在稍后的
# 离线构建里才暴露，那时网络条件可能更差。
if [ -n "$failed" ]; then
  echo "IMAGES_FAILED:$failed"
  exit 1
fi
echo "IMAGES_DONE"
