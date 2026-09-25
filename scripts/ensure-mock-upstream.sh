#!/usr/bin/env bash
# 确保慢速 mock 上游（slow-upstream）在跑。
#
# 依赖它的用例（分组路由、密钥白名单、流式截断……）通过宿主回环
# 127.0.0.1:9997 连它 —— 用例里渠道的 base_url 一律写 127.0.0.1:9997
# （与 verify-all.sh 里那个启动参数保持同一份，别一边有一边没有）。
# 它原先只写在用例的注释里「跑之前先手动起一次」，但进程不在就是全批 502，
# 看起来像产品坏了。让每个用例自己确保它在跑，比写在使用说明里可靠。
#
# 以**宿主 node 进程**运行（原先跑在 node:22-alpine 容器里），端口不变；
# 用 pidfile 记生命周期，探到 /stats 能应答才算就绪。
set -uo pipefail
NAME=slow-upstream
PORT=9997
PIDFILE="/tmp/llm-relay-test-$NAME.pid"

# 已有进程在监听目标端口就视为就绪（pidfile 丢了也没关系）
if curl -fsS -m 2 "http://127.0.0.1:$PORT/stats" >/dev/null 2>&1; then
  exit 0
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 清掉可能残留的旧进程（上次异常退出留下的），避免拿着旧代码继续跑
if [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  kill "$(cat "$PIDFILE")" 2>/dev/null || true
  sleep 1
fi
rm -f "$PIDFILE"

nohup env PORT=$PORT node "$ROOT/scripts/slow-upstream.js" >"/tmp/$NAME.log" 2>&1 &
echo $! > "$PIDFILE"

# node 起来后还要一点时间才开始监听 —— 探到端口真的能应答才算就绪。
# 原来只 sleep 2：机器慢时 node 还没 bind，紧跟着的第一个用例就 502，
# 失败又会让渠道进入冷却（连续失败 3 次），把「上游慢半拍」放大成
# 「接下来 60 秒全挂」
for i in $(seq 1 20); do
  if curl -fsS -m 2 "http://127.0.0.1:$PORT/stats" >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done
echo "mock 上游进程已起，但 20 秒内端口未就绪（日志：/tmp/$NAME.log）" >&2
exit 1
