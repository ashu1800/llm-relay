#!/usr/bin/env bash
# 计费口径：按渠道白名单上的四个单价，用真实请求的 usage 手算金额，与日志里的
# estimated_cost 对齐。金额只做成本感知，所以这里验的就是「算得对不对」。
#
# 为什么用 proto-upstream（Anthropic 口径的 mock）而不是 OpenAI 口径的 mock：
# 只有它的 usage 里缓存读/缓存写都是非零（3 / 4）。用 OpenAI 口径的 mock 时
# 「缓存读 0.5 / 缓存写 1」这两个单价在算式里恒乘以 0，等于压根没验到。
#
# 金额列是 numeric(18,8)，且接口返回的十进制字符串不一定带满 8 位小数
# （实测同一个值可能是 0.0000315 也可能是 0.00003150），所以统一用 Decimal
# 归一化后比较，而不是比字符串。
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
MOCK="http://127.0.0.1:9998"
P() { docker exec -i llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}
# Python 侧算出的一批断言，按「说明<TAB>期望<TAB>实际」回灌给 chk，保证 ok/bad 只有一处统计。
# 必须用重定向喂进来而不是管道：管道会让 while 跑在子 shell 里，
# ok/bad 的累加全部丢失，脚本会在「一项都没通过」的情况下打印 ALL_PASS。
run_checks() {
  while IFS=$'\t' read -r desc exp act; do
    [ -z "$desc" ] && continue
    chk "$desc" "$exp" "$act"
  done
}

bash "$(dirname "$0")/ensure-proto-upstream.sh" || exit 1

GNAME="__cost-probe-group"
CNAME="__cost-probe-ch"
KNAME="__cost-probe-key"
MODEL="__cost-probe-model"

purge() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do
    curl -s -X DELETE "$API/channels/$cid" >/dev/null
  done
  for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME'"); do
    curl -s -X DELETE "$API/keys/$kid" >/dev/null
  done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
  P "DELETE FROM request_payloads WHERE log_id IN (SELECT id FROM request_logs WHERE api_key_name='$KNAME' OR model_requested='$MODEL')" >/dev/null
  P "DELETE FROM request_logs WHERE api_key_name='$KNAME' OR model_requested='$MODEL'" >/dev/null
}

purge

echo "=== 1. 建临时渠道并配价：输入 1 / 输出 2 / 缓存读 0.5 / 缓存写 1 ==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
CID=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\", \"group_id\": $GID, \"protocol\": \"anthropic-messages\",
  \"base_url\": \"http://proto-upstream:9998\", \"api_key\": \"mock-anthropic-key\", \"weight\": 1,
  \"models\": [{
    \"public_name\": \"$MODEL\", \"upstream_name\": \"claude-mock\",
    \"input_per_1m\": \"1\", \"output_per_1m\": \"2\",
    \"cache_read_per_1m\": \"0.5\", \"cache_write_per_1m\": \"1\"
  }]}" | jqg "d['id']")
chk "渠道已创建（拿到数字 id）" "yes" "$(case "$CID" in ''|*[!0-9]*) echo no ;; *) echo yes ;; esac)"
chk "四个单价都落库" "1|2|0.5|1" "$(curl -s "$API/channels/$CID/models" | python3 -c "
import sys, json
from decimal import Decimal
r = json.load(sys.stdin)['items'][0]
d = lambda v: str(Decimal(str(v)).normalize())
print('|'.join(d(r[k]) for k in ('input_per_1m','output_per_1m','cache_read_per_1m','cache_write_per_1m')))
")"
SK=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json'   -d "{\"name\":\"$KNAME\",\"allowed_groups\":[\"$GNAME\"]}" | jqg "d['key']")
echo "  渠道 id=$CID 分组=$GID 密钥长度=${#SK}"

echo
echo "=== 2. 发一次真实请求，把上游回报的 usage 存下来 ==="
RESP=$(curl -s -m 60 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json'   -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"只回复:OK\"}],\"max_tokens\":32}")
echo "$RESP" > /tmp/__cost-resp.json
echo "$RESP" | python3 -c "
import sys, json
d = json.load(sys.stdin)
u = d.get('usage') or {}
print('  上游回报 usage: 输入 %s / 输出 %s / 缓存读 %s / 缓存写 %s' % (
    u.get('prompt_tokens'), u.get('completion_tokens'),
    (u.get('prompt_tokens_details') or {}).get('cached_tokens'), u.get('cache_creation_input_tokens')))
"
chk "请求成功（不是 401/502/429）" "200" "$(echo "$RESP" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(200 if d.get('choices') else ('错误:' + str(d.get('error', {}).get('message'))[:60]))
")"

echo
echo "=== 3. 等日志落库（异步写入，轮询而不是死等）==="
LOG=""
for i in $(seq 1 20); do
  LOG=$(curl -s "$API/logs?model=$MODEL&page_size=1")
  [ "$(echo "$LOG" | jqg "d['total']")" != "0" ] && break
  sleep 1
done
echo "$LOG" | python3 -c "
import sys, json
d = json.load(sys.stdin)
it = (d.get('items') or [None])[0]
if it is None:
    print('  日志还没写进来'); raise SystemExit
print('  落库用量: 输入 %s / 输出 %s / 缓存读 %s / 缓存写 %s' % (
    it['prompt_tokens'], it['completion_tokens'], it['cached_tokens'], it['cache_creation_tokens']))
print('  预估费用: \$%s' % it['estimated_cost'])
"
echo "$LOG" > /tmp/__cost-log.json

echo
echo "=== 4. 手算金额并逐项对比 ==="
# 期望值：(输入价×输入 + 输出价×输出 + 缓存读价×缓存读 + 缓存写价×缓存写) / 1e6
# 这里的四个 token 数取自**日志**（也就是引擎实际用的那组归一化后的数），
# 所以「金额 = 单价 × 用量」这条链路上任何一环错了都会在这里现形
DBCOST=$(P "SELECT estimated_cost::text FROM request_logs WHERE model_requested='$MODEL' ORDER BY id DESC LIMIT 1")
python3 - "$MODEL" "$CID" "$DBCOST" > /tmp/__cost-checks.txt <<'PY'
import json, sys
from decimal import Decimal

model, cid, dbcost = sys.argv[1], sys.argv[2], sys.argv[3]

def emit(desc, exp, act):
    print('%s\t%s\t%s' % (desc, exp, act))
log = json.load(open('/tmp/__cost-log.json'))['items'][0]
resp = json.load(open('/tmp/__cost-resp.json'))
snap = log.get('pricing_snapshot') or {}

# 归一化到最短十进制形式：numeric(18,8) 读回来的字符串位数不固定
def d(v):
    return str(Decimal(str(v)).normalize()) if v not in (None, '') else '0'

pr, co = log['prompt_tokens'], log['completion_tokens']
cr, cw = log['cached_tokens'], log['cache_creation_tokens']
got = d(log['estimated_cost'])
want = d((Decimal(1) * pr + Decimal(2) * co + Decimal('0.5') * cr + Decimal(1) * cw) / Decimal(1000000))
emit('手算金额 = (1×输入 + 2×输出 + 0.5×缓存读 + 1×缓存写)/1e6', want, got)
emit('数据库里的金额与接口一致', d(dbcost), got)
emit('快照里的四个单价', '1|2|0.5|1',
     '|'.join(d(snap.get(k)) for k in ('input_per_1m', 'output_per_1m', 'cache_read_per_1m', 'cache_write_per_1m')))
# 归属：同一个模型名在不同渠道成本不同，快照必须记下这次真正走的渠道
emit('快照记的是这次走的渠道', cid, str(snap.get('channel_id')))
emit('快照记的是对外模型名', model, snap.get('model_key'))
emit('没命中时段时倍率来源是 none', 'none', snap.get('multiplier_source'))
emit('生效倍率是 1（没配固定倍率）', '1', d(snap.get('multiplier')))

# 归一化口径：上游（Anthropic）的 input_tokens 与 cache_read 是并列口径，
# 转成 OpenAI 形状时 prompt = input + cached，落库前再减回来 —— 落库的
# prompt 就是未命中缓存的输入，缓存命中单独一列，两处不能重复计费
u = resp.get('usage') or {}
emit('归一化：响应 prompt = 落库 prompt + 缓存读（不重复计费）', u.get('prompt_tokens'), pr + cr)
emit('缓存写入独立成列（不计入输入）', u.get('cache_creation_input_tokens'), cw)
emit('落库用量（输入/输出/缓存读/缓存写）', '12|7|3|4', '%d|%d|%d|%d' % (pr, co, cr, cw))
PY
run_checks < /tmp/__cost-checks.txt

echo
echo "=== 5. 清理探测数据（含请求日志：它会污染看板的今日统计）==="
for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME'"); do
  curl -s -X DELETE "$API/keys/$kid" >/dev/null
done
curl -s -X DELETE "$API/channels/$CID" >/dev/null
purge
rm -f /tmp/__cost-resp.json /tmp/__cost-log.json
chk "探测密钥已删除" "0" "$(P "SELECT count(*) FROM api_keys WHERE name='$KNAME'")"
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探测日志已删除" "0" "$(P "SELECT count(*) FROM request_logs WHERE api_key_name='$KNAME' OR model_requested='$MODEL'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || { echo "HAS_FAILURE"; exit 1; }
