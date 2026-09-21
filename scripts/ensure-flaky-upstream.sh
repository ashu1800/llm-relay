#!/usr/bin/env bash
# 确保「会失败的上游」（flaky-upstream）在跑，供 test-retry-delay.sh 使用。
#
# 契约与 ensure-mock-upstream.sh 完全一致，理由也一样：容器会随
# compose 重建网络被带走（Exited 137），届时整批用例突然全 502，
# 看起来像产品坏了。让用例自己确保它在跑，比写在说明里可靠。
set -uo pipefail
NAME=flaky-upstream
PORT=9996

if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = "true" ]; then
  exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
docker rm -f "$NAME" >/dev/null 2>&1
# app 容器是 host 网络模式，解析不了 Docker 网络里的容器名，
# 所以用例里渠道的 base_url 一律写 127.0.0.1:9996
if ! docker run -d --name "$NAME" --network llm-relay_relay \
     -p 127.0.0.1:$PORT:$PORT \
     -e PORT=$PORT \
     -v "$ROOT/scripts/flaky-upstream.js:/app/server.js:ro" \
     node:22-alpine node /app/server.js >/dev/null; then
  echo "启动 flaky 上游失败：确认 node:22-alpine 镜像已就绪" >&2
  exit 1
fi

# 探到端口真的能应答才算就绪 —— 只 sleep 的话机器慢时 node 还没 bind，
# 紧跟的第一个用例就会因为连接失败而失败，还顺带把渠道打进冷却
for i in $(seq 1 20); do
  if curl -fsS -m 2 "http://127.0.0.1:$PORT/stats" >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done
echo "flaky 上游容器已起，但 20 秒内端口未就绪" >&2
exit 1
