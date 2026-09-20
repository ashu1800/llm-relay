#!/usr/bin/env bash
# 确保慢速 mock 上游（slow-upstream）在跑。
#
# 依赖它的用例（分组路由、密钥白名单、流式截断……）通过宿主回环
# 127.0.0.1:9997 连它 —— app 容器是 host 网络模式，走 Docker 容器名
# 的旧写法（slow-upstream:9999）在那次改造后就解析不了了。
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
# 端口发布到宿主回环：app 容器是 host 网络模式，解析不了 Docker 网络里的
# 容器名 —— 用例里渠道的 base_url 一律写 127.0.0.1:9997（与 verify-all.sh
# 里那个启动参数保持同一份，别一边有一边没有）
if ! docker run -d --name "$NAME" --network llm-relay_relay \
     -p 127.0.0.1:9997:9999 \
     -v "$ROOT/scripts/slow-upstream.js:/app/server.js:ro" \
     node:22-alpine node /app/server.js >/dev/null; then
  echo "启动 mock 上游失败：确认 node:22-alpine 镜像已就绪" >&2
  exit 1
fi
# 容器起来后 node 还要一点时间才开始监听 —— 探到端口真的能应答才算就绪。
# 原来只 sleep 2：机器慢时 node 还没 bind，紧跟着的第一个用例就 502，
# 失败又会让渠道进入冷却（连续失败 3 次），把「上游慢半拍」放大成
# 「接下来 60 秒全挂」
for i in $(seq 1 20); do
  if curl -fsS -m 2 http://127.0.0.1:9997/stats >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done
echo "mock 上游容器已起，但 20 秒内端口未就绪" >&2
exit 1
