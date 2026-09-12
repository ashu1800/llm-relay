#!/usr/bin/env bash
# 确保「Anthropic 原生协议」的上游 mock（proto-upstream）在跑。
# 与 ensure-mock-upstream.sh 同样的理由：容器会随 docker 网络重建被带走。
set -uo pipefail
NAME=proto-upstream

if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" = "true" ]; then
  exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
docker rm -f "$NAME" >/dev/null 2>&1
# 端口映射是给用例读 /stats 用的（要断言「上游收到的到底是哪种协议」）
if ! docker run -d --name "$NAME" --network llm-relay_relay \
     -p 127.0.0.1:9998:9998 \
     -v "$ROOT/scripts/proto-upstream.js:/app/server.js:ro" \
     node:22-alpine node /app/server.js >/dev/null; then
  echo "启动 proto-upstream 失败：确认 node:22-alpine 镜像已就绪" >&2
  exit 1
fi
sleep 2
exit 0
