#!/usr/bin/env bash
# 确保「Anthropic 原生协议」的上游 mock（proto-upstream）在跑。
# 与 ensure-mock-upstream.sh 同样的理由：进程不在场时整批用例会失败。
#
# 以**宿主 node 进程**运行（原先跑在 node:22-alpine 容器里），端口不变；
# 用 pidfile 记生命周期。9998 的 /stats 是给用例读的
# （要断言「上游收到的到底是哪种协议」）。
set -uo pipefail
NAME=proto-upstream
PORT=9998
PIDFILE="/tmp/llm-relay-test-$NAME.pid"

if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
rm -f "$PIDFILE"

nohup env PORT=$PORT node "$ROOT/scripts/proto-upstream.js" >"/tmp/$NAME.log" 2>&1 &
echo $! > "$PIDFILE"

# 探到端口真的能应答才算就绪
for i in $(seq 1 20); do
  if curl -fsS -m 2 "http://127.0.0.1:$PORT/stats" >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done
echo "proto-upstream 进程已起，但 20 秒内端口未就绪（日志：/tmp/$NAME.log）" >&2
exit 1
