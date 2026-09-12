#!/usr/bin/env bash
# 确保慢速 mock 上游（slow-upstream）在跑。
#
# 依赖它的用例（分组路由、密钥白名单、流式截断……）都要求这个容器在
# llm-relay_relay 网络里、容器名恰好是 slow-upstream。
# 它原先只写在用例的注释里「跑之前先手动起一次」，但容器会随着
# docker compose 重建网络被带走（Exited 137），于是整批用例突然全变 502，
# 看起来像产品坏了。让每个用例自己确保它在跑，比写在使用说明里可靠。
set -uo pipefail
NAME=slow-upstream

if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = "true" ]; then
  exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
docker rm -f "$NAME" >/dev/null 2>&1
if ! docker run -d --name "$NAME" --network llm-relay_relay \
     -v "$ROOT/scripts/slow-upstream.js:/app/server.js:ro" \
     node:22-alpine node /app/server.js >/dev/null; then
  echo "启动 mock 上游失败：确认 node:22-alpine 镜像已就绪" >&2
  exit 1
fi
# 容器起来后 node 还要一点时间才开始监听
sleep 2
exit 0
