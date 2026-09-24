#!/usr/bin/env bash
# 实时推送回归：WebSocket 能不能连上、统计快照有没有内容、
# 新请求产生后日志有没有被推到第一行。
#
# 判据是「发一次真实请求后确实收到了一条 logs」，而不是「连接建立了」——
# 后者证明不了推送链路是通的。
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup


API=${API:-http://127.0.0.1:8888/api/admin}
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PG="docker exec -i llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c"
MODEL=$($PG "SELECT public_name FROM channel_models WHERE enabled = true ORDER BY id LIMIT 1")
KEYNAME=__live_probe__

pass=0; fail=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; pass=$((pass + 1));
  else echo "  [失败] $1 期望 $2 实际 $3"; fail=$((fail + 1)); fi
}
chk_has() {
  case "$3" in *"$2"*) echo "  [通过] $1"; pass=$((pass + 1));;
    *) echo "  [失败] $1：期望包含「$2」，实际「$3」"; fail=$((fail + 1));; esac
}

cleanup() {
  for id in $($PG "SELECT id FROM api_keys WHERE name = '$KEYNAME'"); do
    curl -s -o /dev/null -X DELETE "$API/keys/$id"
  done
}
trap cleanup EXIT
cleanup

echo "=== 准备：一把探针密钥（模型 $MODEL）==="
KEY=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' -d "{\"name\":\"$KEYNAME\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["key"])')

echo
echo "=== 1. 握手 + 初始快照 ==="
python3 "$ROOT/scripts/ws-listen.py" 3 > /tmp/ws1.txt 2>&1 &
WSPID=$!
sleep 1
wait $WSPID
cat /tmp/ws1.txt | sed 's/^/  /'
chk_has "握手成功" "HANDSHAKE_OK" "$(cat /tmp/ws1.txt)"
chk_has "连上就收到统计快照" '"stats"' "$(cat /tmp/ws1.txt)"
chk "统计快照字段数不为 0" "true" "$(python3 -c "
import re
t = open('/tmp/ws1.txt').read()
m = re.search(r'STATS_FIELDS (\d+)', t)
print('true' if m and int(m.group(1)) > 0 else 'false')")"

echo
echo "=== 2. 发一次真实请求，日志应当被推过来 ==="
# 监听窗口必须罩住「请求真正跑完」的那一刻，而不是拍一个固定秒数：
# 上游慢的时候请求可能要十几秒才返回，日志推送就落到窗口之外了 ——
# 实测在 verify-all 里偶发失败，单独跑却总是通过，就是这种时序问题。
# 现在的做法是「请求完成后最多再等 12 秒」：日志落库后最迟 1 秒内就会被推。
# -u 是关键：python 输出重定向到文件时是块缓冲，
# 进程被杀掉时缓冲区里的 TYPES 行会整个丢掉。
python3 -u "$ROOT/scripts/ws-listen.py" 40 logs > /tmp/ws2.txt 2>&1 &
WSPID=$!
sleep 2
BEFORE=$($PG "SELECT COALESCE(MAX(id),0) FROM request_logs")
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:8888/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":8}")
chk "探针请求成功" "200" "$CODE"
# 监听端收到 logs 会自己退出；这里等它（最多 40 秒，够覆盖一次慢上游）
wait $WSPID
cat /tmp/ws2.txt | sed 's/^/  /'
chk_has "收到了 logs 推送" '"logs"' "$(cat /tmp/ws2.txt)"
AFTER=$($PG "SELECT COALESCE(MAX(id),0) FROM request_logs")
NEW=$($PG "SELECT id FROM request_logs WHERE id > $BEFORE ORDER BY id LIMIT 1")
chk_has "推来的就是刚产生的那条日志" "$NEW" "$(cat /tmp/ws2.txt)"
chk_has "同时推了更新后的统计" '"stats"' "$(cat /tmp/ws2.txt)"

echo
if [ "$fail" -eq 0 ]; then echo "通过 $pass 项，失败 0 项"; echo ALL_PASS
else echo "通过 $pass 项，失败 $fail 项"; echo HAS_FAILURE; exit 1; fi
