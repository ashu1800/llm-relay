#!/usr/bin/env bash
# 确保「会失败的上游」（flaky-upstream）在跑，供 test-retry-delay.sh 使用。
#
# 契约与 ensure-mock-upstream.sh 完全一致，理由也一样：进程不在场时
# 整批用例突然全 502，看起来像产品坏了。让用例自己确保它在跑，
# 比写在说明里可靠。
#
# 以**宿主 node 进程**运行（原先跑在 node:22-alpine 容器里），端口不变；
# 用 pidfile 记生命周期，探到 /stats 能应答才算就绪。
set -uo pipefail
NAME=flaky-upstream
PORT=9996
PIDFILE="/tmp/llm-relay-test-$NAME.pid"

# 已有进程在监听目标端口就视为就绪（pidfile 丢了也没关系）
if curl -fsS -m 2 "http://127.0.0.1:$PORT/stats" >/dev/null 2>&1; then
  exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  kill "$(cat "$PIDFILE")" 2>/dev/null || true
  sleep 1
fi
rm -f "$PIDFILE"

# 用例里渠道的 base_url 一律写 127.0.0.1:9996
nohup env PORT=$PORT node "$ROOT/scripts/flaky-upstream.js" >"/tmp/$NAME.log" 2>&1 &
echo $! > "$PIDFILE"

# 探到端口真的能应答才算就绪 —— 只 sleep 的话机器慢时 node 还没 bind，
# 紧跟的第一个用例就会因为连接失败而失败，还顺带把渠道打进冷却
for i in $(seq 1 20); do
  if curl -fsS -m 2 "http://127.0.0.1:$PORT/stats" >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done
echo "flaky 上游进程已起，但 20 秒内端口未就绪（日志：/tmp/$NAME.log）" >&2
exit 1
