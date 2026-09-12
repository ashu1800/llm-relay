#!/usr/bin/env bash
# 定价倍率的端到端验证：固定倍率、时段倍率、折扣、停用、规则校验。
#
# 倍率必须用**真实请求**验证，而不是看接口返回：倍率是在定价引擎里作用于
# 「那一刻的单价」的，只有在日志的 estimated_cost 上才能看到最终结果。
# 时刻全部从应用容器里取（docker exec llm-relay date），
# 因为时段是按服务器本地时区判断的，脚本所在环境的时区不一定是它。
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }
# 应用容器里的本地时间/星期（时段规则就是按它判断的）
APP() { docker exec llm-relay date "$@"; }
# 容器里的 date 是精简版，不认 -d '-1 hour' 这类相对时间（实测报 invalid date），
# 所以偏移在脚本里自己做：只动小时，分钟原样保留
shift_time() {
  local hm="$1" delta="$2"
  local h="${hm%%:*}" m="${hm##*:}"
  h=$(( (10#$h + delta + 24) % 24 ))
  printf '%02d:%s' "$h" "$m"
}

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}
chkcontains() {
  case "$3" in
    *"$2"*) echo "  [通过] $1（含「$2」）"; ok=$((ok+1)) ;;
    *) echo "  [失败] $1 未包含「$2」，实际: $(echo "$3" | head -c 200)"; bad=$((bad+1)) ;;
  esac
}
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

bash "$(dirname "$0")/ensure-mock-upstream.sh" || exit 1

GNAME="pricing-rule-group"
CNAME="pricing-rule-channel"
KNAME="pricing-rule-key"
MODEL="pricing-rule-model"

purge() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do curl -s -X DELETE "$API/channels/$cid" >/dev/null; done
  for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME'"); do curl -s -X DELETE "$API/keys/$kid" >/dev/null; done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
  for pid in $(P "SELECT id FROM model_pricings WHERE model_key='$MODEL'"); do curl -s -X DELETE "$API/pricing/$pid" >/dev/null; done
}

echo "=== 0. 准备：渠道（OpenAI 上游，usage 固定为 10 进 5 出）+ 密钥 ==="
purge
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
CID=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\",
  \"group_id\": $GID,
  \"protocol\": \"openai-chat\",
  \"base_url\": \"http://slow-upstream:9999\",
  \"api_key\": \"mock-key\",
  \"weight\": 1,
  \"models\": [{\"public_name\": \"$MODEL\", \"upstream_name\": \"$MODEL\"}]
}" | jqg "d['id']")
SK=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$KNAME\",\"allowed_groups\":[\"$GNAME\"]}" | jqg "d['key']")
echo "  渠道 id=$CID 分组=$GID"
echo "  服务器本地时间: $(APP +'%F %T %u') （星期 $(APP +%u)）"
chkcontains "密钥已签发" "sk-" "$SK"

# 发一次请求并读回最新一条日志的金额。
# 单价故意取整数（输入 1 / 输出 2 每 1M），usage 是 10 进 5 出：
# 原价 = (1*10 + 2*5)/1e6 = 0.00002，倍率直接把结果变成 0.00003 / 0.00004 / 0.00001
call_and_cost() {
  curl -s -m 30 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}" >/dev/null
  sleep 2
  curl -s "$API/logs?page_size=10" | python3 -c "
import sys, json
d = json.load(sys.stdin)
for it in d['items']:
    if it.get('model_requested') == '$MODEL':
        print('%s %s' % (it.get('estimated_cost'), json.dumps(it.get('pricing_snapshot') or {})))
        break
else:
    print('NONE {}')
"
}
cost_of() { echo "$1" | awk '{print $1}'; }
snap_of() { echo "$1" | cut -d' ' -f2-; }

echo
echo "=== 1. 没有定价时金额为 0 ==="
R=$(call_and_cost)
chk "未定价 -> 0" "0" "$(cost_of "$R")"

echo
echo "=== 2. 手工录入单价（固定倍率 1 = 原价）==="
PID=$(curl -s -X POST "$API/pricing" -H 'Content-Type: application/json' -d "{
  \"model_key\": \"$MODEL\",
  \"input_per_1m\": \"1\",
  \"output_per_1m\": \"2\",
  \"multiplier\": 1
}" | jqg "d['id']")
R=$(call_and_cost)
chk "原价 = (1×10 + 2×5)/1e6" "0.00002" "$(cost_of "$R")"
chk "快照记下倍率来源" "none" "$(echo "$(snap_of "$R")" | jqg "d['multiplier_source']")"
chk "快照里的生效倍率" "1" "$(echo "$(snap_of "$R")" | jqg "d['multiplier']")"

echo
echo "=== 3. 固定倍率 1.5 ==="
curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' -d '{"multiplier":1.5}' >/dev/null
R=$(call_and_cost)
chk "固定倍率 1.5 -> 0.00003" "0.00003" "$(cost_of "$R")"
chk "倍率来源记为 fixed" "fixed" "$(echo "$(snap_of "$R")" | jqg "d['multiplier_source']")"

echo
echo "=== 4. 时段倍率（窗口覆盖当前时刻）优先于固定倍率 ==="
TODAY=$(APP +%u)
curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' \
  -d "{\"peak_rules\":[{\"days\":[],\"start\":\"00:00\",\"end\":\"23:59\",\"multiplier\":2,\"label\":\"全天双倍\"}]}" >/dev/null
R=$(call_and_cost)
chk "时段 2 倍覆盖固定 1.5 倍 -> 0.00004" "0.00004" "$(cost_of "$R")"
chk "倍率来源记为 peak" "peak" "$(echo "$(snap_of "$R")" | jqg "d['multiplier_source']")"
chk "命中标签" "全天双倍" "$(echo "$(snap_of "$R")" | jqg "d['peak_label']")"
chk "peak_applied" "True" "$(echo "$(snap_of "$R")" | jqg "d['peak_applied']")"
chk "快照同时保留固定倍率" "1.5" "$(echo "$(snap_of "$R")" | jqg "d['fixed_multiplier']")"

echo
echo "=== 5. 星期不匹配时不命中（回落到固定倍率）==="
OTHER=$(( TODAY % 7 + 1 ))
curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' \
  -d "{\"peak_rules\":[{\"days\":[$OTHER],\"start\":\"00:00\",\"end\":\"23:59\",\"multiplier\":2,\"label\":\"别的星期\"}]}" >/dev/null
R=$(call_and_cost)
chk "窗口限定在星期 $OTHER（今天是 $TODAY）-> 回落 0.00003" "0.00003" "$(cost_of "$R")"
chk "倍率来源回到 fixed" "fixed" "$(echo "$(snap_of "$R")" | jqg "d['multiplier_source']")"

echo
echo "=== 6. 折扣倍率（0.5）要能生效 ==="
NOW=$(APP +%H:%M)
FROM=$(shift_time "$NOW" -1); TO=$(shift_time "$NOW" +1)
curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' \
  -d "{\"peak_rules\":[{\"days\":[],\"start\":\"$FROM\",\"end\":\"$TO\",\"multiplier\":0.5,\"label\":\"折扣时段\"}]}" >/dev/null
R=$(call_and_cost)
chk "窗口 $FROM-$TO 五折 -> 0.00001" "0.00001" "$(cost_of "$R")"
chk "折扣也记为 peak" "peak" "$(echo "$(snap_of "$R")" | jqg "d['multiplier_source']")"

echo
echo "=== 7. 时段完全不覆盖时回落（凌晨跨午夜的窗口也要能正确判断）==="
FROM2=$(shift_time "$NOW" 3); TO2=$(shift_time "$NOW" 4)
curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' \
  -d "{\"peak_rules\":[{\"days\":[],\"start\":\"$FROM2\",\"end\":\"$TO2\",\"multiplier\":3,\"label\":\"稍后\"}]}" >/dev/null
R=$(call_and_cost)
chk "窗口 $FROM2-$TO2 未覆盖当前 -> 0.00003" "0.00003" "$(cost_of "$R")"

echo
echo "=== 8. 价格试算接口（按本地时区解释时刻）==="
AT=$(APP --rfc-3339=seconds)
curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' \
  -d "{\"peak_rules\":[{\"days\":[],\"start\":\"00:00\",\"end\":\"23:59\",\"multiplier\":2,\"label\":\"全天\"}]}" >/dev/null
RES=$(curl -s -X POST "$API/pricing/resolve" -H 'Content-Type: application/json' -d "{\"model\":\"$MODEL\",\"at\":\"$AT\"}")
echo "  试算: $(echo "$RES" | head -c 220)"
chk "试算命中时段" "2" "$(echo "$RES" | jqg "d['snapshot']['multiplier']")"
chk "试算标出来源" "peak" "$(echo "$RES" | jqg "d['snapshot']['multiplier_source']")"
chkcontains "生效时刻带时区偏移" "+" "$(echo "$RES" | jqg "d['snapshot']['resolved_at']")"

echo
echo "=== 9. 规则与倍率的校验（写错的窗口必须当场报错）==="
V1=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' \
  -d '{"peak_rules":[{"days":[],"start":"09:00","end":"18","multiplier":2}]}')
chk "时间格式错 -> 400" "400" "$V1"
V2=$(curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' \
  -d '{"peak_rules":[{"days":[1],"start":"09:00","end":"12:00","multiplier":0}]}')
chkcontains "倍率为 0 的规则被拒绝并指明第几条" "第 1 条" "$V2"
V3=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' -d '{"multiplier":1000}')
chk "倍率超上限 -> 400" "400" "$V3"
V4=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' -d '{"multiplier":-1}')
chk "倍率为负 -> 400" "400" "$V4"
chk "被拒绝后原值没被改动" "2" "$(curl -s "$API/pricing?keyword=$MODEL" | jqg "d['items'][0]['peak_rules'][0]['multiplier']")"

echo
echo "=== 10. 停用后不计费，启用后恢复 ==="
curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' -d '{"active":false}' >/dev/null
R=$(call_and_cost)
chk "停用 -> 0" "0" "$(cost_of "$R")"
curl -s -X PUT "$API/pricing/$PID" -H 'Content-Type: application/json' -d '{"active":true}' >/dev/null
R=$(call_and_cost)
chk "启用 -> 恢复时段价 0.00004" "0.00004" "$(cost_of "$R")"

echo
echo "=== 11. 清理探测数据 ==="
purge
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探测密钥已删除" "0" "$(P "SELECT count(*) FROM api_keys WHERE name='$KNAME'")"
chk "探测定价已删除" "0" "$(P "SELECT count(*) FROM model_pricings WHERE model_key='$MODEL'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
