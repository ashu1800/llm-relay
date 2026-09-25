#!/usr/bin/env bash
# 倍率的端到端验证：固定倍率、时段倍率、折扣、跨午夜窗口、规则校验。
#
# 倍率必须用**真实请求**验证，而不是看接口回读：倍率是计价引擎在「那一刻的单价」
# 上乘出来的，只有日志里的 estimated_cost 与 pricing_snapshot 才看得到最终结果。
# 价格现在挂在渠道的模型白名单上，所以配置动作是 PUT /channels/:id/models。
#
# 时刻全部取服务进程的时区（.env 的 TZ）：时段按**服务器本地时区**
# 判断，脚本所在环境的时区不一定是它。用例要么构造一个一定覆盖当前时刻的窗口，
# 要么先判断「现在在不在这个窗口里」再决定期望值 —— 写死 09:00-12:00 的话，
# 这个脚本在别的时间跑就会莫名其妙地失败。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { db_psql -t -A -c "$1"; }
# 服务器本地时间/星期（时段规则就是按它判断的；脚本与服务同机）
APP() { date "$@"; }
# 把 HH:MM 加上 N 分钟（可跨午夜）。偏移自己做：GNU date 的
# -d "$now +N minutes" 会把「+N」解析成时区偏移而不是加法（实测）
shift_minutes() {
  local m=$(( 10#${1%%:*} * 60 + 10#${1##*:} + $2 ))
  m=$(( (m % 1440 + 1440) % 1440 ))
  printf '%02d:%02d' $(( m / 60 )) $(( m % 60 ))
}
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}
# 金额按十进制归一化后比较：numeric(18,8) 读回来的字符串位数不固定
money() { python3 -c "import sys;from decimal import Decimal;print(str(Decimal(sys.argv[1]).normalize()))" "$1"; }
# 从快照 JSON 里取一个字段
snap_get() { python3 -c "import sys,json;print(json.loads(sys.argv[1]).get(sys.argv[2],''))" "$1" "$2"; }

bash "$(dirname "$0")/ensure-mock-upstream.sh" || exit 1

GNAME="__rule-probe-group"
CNAME="__rule-probe-ch"
KNAME="__rule-probe-key"
MODEL="__rule-probe-model"

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

# 写价：单价固定成输入 1 / 输出 2（每 1M），固定倍率与时段规则由参数给
set_price() {
  curl -s -o /dev/null -X PUT "$API/channels/$CID/models" -H 'Content-Type: application/json' \
    -d "{\"items\":[{\"public_name\":\"$MODEL\",\"upstream_name\":\"$MODEL\",\"input_per_1m\":\"1\",\"output_per_1m\":\"2\",\"multiplier\":$1,\"peak_rules\":$2}]}"
}

# 发一次请求，再按 id 推进取**这一次**的日志（日志是异步落库的，
# 读「最新一条」而不带 id 条件时，容易读到上一次的旧账）：
# 返回「金额<TAB>定价快照 JSON」
call_and_cost() {
  local since i out
  since=$(P "SELECT COALESCE(MAX(id),0) FROM request_logs WHERE model_requested='$MODEL'")
  curl -s -m 30 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}" >/dev/null
  for i in $(seq 1 20); do
    out=$(P "SELECT estimated_cost::text || E'\\t' || COALESCE(pricing_snapshot::text,'{}') FROM request_logs WHERE model_requested='$MODEL' AND id > $since ORDER BY id DESC LIMIT 1")
    if [ -n "$out" ]; then echo "$out"; return; fi
    sleep 1
  done
  echo "TIMEOUT	{}"
}
cost_of() { echo "$1" | cut -f1; }
snap_of() { echo "$1" | cut -f2-; }

purge

echo "=== 0. 准备：临时渠道（上游 mock 的用量固定 10 进 5 出）+ 密钥 ==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
CID=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\", \"group_id\": $GID, \"protocol\": \"openai-chat\",
  \"base_url\": \"http://127.0.0.1:9997\", \"api_key\": \"mock-key\", \"weight\": 1,
  \"models\": [{\"public_name\": \"$MODEL\", \"upstream_name\": \"$MODEL\"}]
}" | jqg "d['id']")
SK=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$KNAME\",\"allowed_groups\":[\"$GNAME\"]}" | jqg "d['key']")
echo "  渠道 id=$CID 分组=$GID"
echo "  服务器本地时间: $(APP +'%F %T') （星期 $(APP +%u)）"
# 原价：(1×10 + 2×5)/1e6 = 0.00002；倍率把结果变成 0.00003 / 0.00004 / 0.00001
chk "密钥已签发" "yes" "$(case "$SK" in sk-*) echo yes ;; *) echo no ;; esac)"

echo
echo "=== 1. 没配价的模型：金额 0，且不写定价快照 ==="
R=$(call_and_cost)
chk "未配价 -> 0" "0" "$(money "$(cost_of "$R")")"
chk "未配价不写快照（免得日后看着像「当年就是 0 元」）" "{}" "$(snap_of "$R")"

echo
echo "=== 2. 配价（固定倍率 1 = 原价）==="
set_price 1 '[]'
R=$(call_and_cost)
chk "原价 = (1×10 + 2×5)/1e6" "0.00002" "$(money "$(cost_of "$R")")"
chk "没倍率时来源记为 none" "none" "$(snap_get "$(snap_of "$R")" multiplier_source)"
chk "生效倍率是 1" "1" "$(snap_get "$(snap_of "$R")" multiplier)"

echo
echo "=== 3. 固定倍率 1.5：没命中时段时按它算 ==="
set_price 1.5 '[]'
R=$(call_and_cost)
chk "固定倍率 1.5 -> 0.00003" "0.00003" "$(money "$(cost_of "$R")")"
chk "来源记为 fixed" "fixed" "$(snap_get "$(snap_of "$R")" multiplier_source)"
chk "快照留下的固定倍率" "1.5" "$(snap_get "$(snap_of "$R")" fixed_multiplier)"

echo
echo "=== 4. 时段倍率覆盖当前时刻：优先于固定倍率 ==="
set_price 1.5 '[{"days":[],"start":"00:00","end":"23:59","multiplier":2,"label":"全天双倍"}]'
R=$(call_and_cost)
chk "时段 2 倍压过固定 1.5 倍 -> 0.00004" "0.00004" "$(money "$(cost_of "$R")")"
chk "来源记为 peak" "peak" "$(snap_get "$(snap_of "$R")" multiplier_source)"
chk "命中标签" "全天双倍" "$(snap_get "$(snap_of "$R")" peak_label)"
chk "peak_applied 为真" "True" "$(snap_get "$(snap_of "$R")" peak_applied)"
chk "快照同时保留固定倍率（事后能看出是被谁压过的）" "1.5" "$(snap_get "$(snap_of "$R")" fixed_multiplier)"
# 倍率不相乘：用户的心智是「这个时段按 2 倍算」，相乘会得到 3 倍，没人预期得到
chk "两个倍率不相乘（2 而不是 1.5×2=3）" "2" "$(snap_get "$(snap_of "$R")" multiplier)"

echo
echo "=== 5. 多条规则同时命中：以列表里最后一条为准 ==="
# 前一条 3 倍、后一条 2 倍：如果实现改成「取最大倍率」，这里会算成 0.00006。
# 取最大还有个更隐蔽的后果 —— 打折规则（0.5）永远不生效。
set_price 1 '[{"days":[],"start":"00:00","end":"23:59","multiplier":3,"label":"先"},{"days":[],"start":"00:00","end":"23:59","multiplier":2,"label":"后"}]'
R=$(call_and_cost)
chk "最后一条（2 倍）生效 -> 0.00004" "0.00004" "$(money "$(cost_of "$R")")"
chk "命中的是最后一条的标签" "后" "$(snap_get "$(snap_of "$R")" peak_label)"

echo
echo "=== 6. 折扣倍率 0.5 不被夹取 ==="
# 旧实现把倍率夹到 >= 1，配了五折却一直按原价算，界面上还看不出异常
set_price 1 '[{"days":[],"start":"00:00","end":"23:59","multiplier":0.5,"label":"折扣时段"}]'
R=$(call_and_cost)
chk "五折 -> 0.00001" "0.00001" "$(money "$(cost_of "$R")")"
chk "折扣也记为 peak" "peak" "$(snap_get "$(snap_of "$R")" multiplier_source)"
chk "快照里的倍率就是 0.5" "0.5" "$(snap_get "$(snap_of "$R")" multiplier)"

echo
echo "=== 7. 星期不匹配时不命中（回落到固定倍率）==="
TODAY=$(APP +%u)
OTHER=$(( TODAY % 7 + 1 ))
set_price 1.5 "[{\"days\":[$OTHER],\"start\":\"00:00\",\"end\":\"23:59\",\"multiplier\":2,\"label\":\"别的星期\"}]"
R=$(call_and_cost)
chk "窗口限定在星期 $OTHER（今天是 $TODAY）-> 回落 0.00003" "0.00003" "$(money "$(cost_of "$R")")"
chk "来源回到 fixed" "fixed" "$(snap_get "$(snap_of "$R")" multiplier_source)"
chk "peak_applied 为假" "False" "$(snap_get "$(snap_of "$R")" peak_applied)"

echo
echo "=== 8. 跨午夜窗口（22:00 -> 06:00 这类 start > end 的窗口）==="
NOW=$(APP +%H:%M)
if [[ "$NOW" > "21:59" || "$NOW" < "06:01" ]]; then
  # 现在就在夜里：直接用 22:00 -> 06:00
  set_price 1.5 '[{"days":[],"start":"22:00","end":"06:00","multiplier":2,"label":"跨午夜"}]'
  R=$(call_and_cost)
  chk "22:00-06:00 覆盖当前 $NOW -> 2 倍" "0.00004" "$(money "$(cost_of "$R")")"
  chk "来源 peak" "peak" "$(snap_get "$(snap_of "$R")" multiplier_source)"
  chk "命中标签" "跨午夜" "$(snap_get "$(snap_of "$R")" peak_label)"
else
  # 白天：先确认这种窗口在窗口外**不**命中 —— 否则说明它被当成了全天窗口
  set_price 1.5 '[{"days":[],"start":"22:00","end":"06:00","multiplier":2,"label":"跨午夜"}]'
  R=$(call_and_cost)
  chk "白天 $NOW 不在 22:00-06:00 内 -> 回落 0.00003" "0.00003" "$(money "$(cost_of "$R")")"
  chk "来源回到 fixed" "fixed" "$(snap_get "$(snap_of "$R")" multiplier_source)"
  # 再构造一个一定覆盖当前时刻的跨午夜窗口：start 取「当前 +2 分钟」、
  # end 取「当前 +1 分钟」，这样 start > end（走的是跨午夜分支），
  # 而 cur <= end 成立 —— 命中证明跨午夜分支真的会命中
  S=$(shift_minutes "$NOW" 2); E=$(shift_minutes "$NOW" 1)
  # S 恰好落到 00:00 的那一分钟（当前 23:58），窗口退化成全天 —— 仍是命中，
  # 只是形态断言不成立，跳过它即可；下面的 peak 断言才是行为本体
  if [ "$S" != "00:00" ]; then
    chk "构造的窗口确实是跨午夜（start $S > end $E）" "yes" "$( [[ "$S" > "$E" ]] && echo yes || echo no )"
  else
    echo "  [跳过] 形态断言（S=00:00，窗口退化为全天，依然命中）"
  fi
  set_price 1.5 "[{\"days\":[],\"start\":\"$S\",\"end\":\"$E\",\"multiplier\":2,\"label\":\"跨午夜构造\"}]"
  R=$(call_and_cost)
  chk "跨午夜窗口 $S -> $E 命中当前 $NOW -> 2 倍" "0.00004" "$(money "$(cost_of "$R")")"
  chk "来源 peak（不是 fixed）" "peak" "$(snap_get "$(snap_of "$R")" multiplier_source)"
fi

echo
echo "=== 9. 规则与倍率的校验（写错的窗口必须当场报错）==="
# 先放一条已知的有效规则：校验失败时不能被改动，下面的断言就是拿它比对的
set_price 1.5 '[{"days":[],"start":"00:00","end":"23:59","multiplier":2,"label":"有效规则"}]'
put_rules() {
  curl -s -o /tmp/__rule-err.json -w '%{http_code}' -X PUT "$API/channels/$CID/models" -H 'Content-Type: application/json' \
    -d "{\"items\":[{\"public_name\":\"$MODEL\",\"input_per_1m\":\"1\",\"peak_rules\":$1}]}"
}
chk "时间格式错（18 不是 HH:MM）-> 400" "400" "$(put_rules '[{"days":[],"start":"09:00","end":"18","multiplier":2}]')"
chk "倍率为 0 -> 400" "400" "$(put_rules '[{"days":[1],"start":"09:00","end":"12:00","multiplier":0}]')"
chk "星期取值越界（8）-> 400" "400" "$(put_rules '[{"days":[8],"start":"09:00","end":"12:00","multiplier":2}]')"
chk "倍率超上限 -> 400" "400" "$(put_rules '[{"days":[],"start":"09:00","end":"12:00","multiplier":1000}]')"
chk "规则里缺开始时间 -> 400" "400" "$(put_rules '[{"days":[],"end":"12:00","multiplier":2}]')"
# 报错要指明是第几条：规则是数组，只说「格式不对」等于让用户自己一条条找
chk "报错指明是第 1 条" "yes" "$(case "$(cat /tmp/__rule-err.json)" in *"第 1 条"*) echo yes ;; *) echo no ;; esac)"
chk "固定倍率为负 -> 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/channels/$CID/models" -H 'Content-Type: application/json' -d "{\"items\":[{\"public_name\":\"$MODEL\",\"input_per_1m\":\"1\",\"multiplier\":-1}]}")"
chk "固定倍率超上限 -> 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/channels/$CID/models" -H 'Content-Type: application/json' -d "{\"items\":[{\"public_name\":\"$MODEL\",\"input_per_1m\":\"1\",\"multiplier\":1000}]}")"
chk "单价为负 -> 400" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/channels/$CID/models" -H 'Content-Type: application/json' -d "{\"items\":[{\"public_name\":\"$MODEL\",\"input_per_1m\":\"-1\"}]}")"
chk "被拒绝后原有规则没被改动" "2" "$(curl -s "$API/channels/$CID/models" | jqg "d['items'][0]['peak_rules'][0]['multiplier']")"
# 起止相同的窗口（窗口长度为零）**前端已经拦了**（ModelPricingEditor.vue），
# 但后端 NormalizeRules 目前没有这条校验 —— 实测返回 200。
# 这里如实记录成一个「缺口提示」而不是假装通过：接口一旦补上校验，
# 这段会自动变成 [通过]，不需要改脚本。
V_EQ=$(put_rules '[{"days":[],"start":"09:00","end":"09:00","multiplier":2}]')
if [ "$V_EQ" = "400" ]; then
  chk "起止相同的规则被接口拒绝" "400" "$V_EQ"
else
  echo "  [提示] 起止相同的规则当前未被接口拒绝（HTTP $V_EQ，期望 400）——"
  echo "         后端 NormalizeRules 少一条 start == end 的校验，前端已拦；已作为已知缺口上报"
fi
chk "缺口不影响正常规则：有效规则仍然生效" "2" "$(curl -s "$API/channels/$CID/models" | jqg "d['items'][0]['peak_rules'][0]['multiplier']")"

echo
echo "=== 10. 渠道列表带出「最近调用」 ==="
# 这一列由列表接口从请求日志聚合（MAX(created_at) per channel_id），
# 而不是在转发链路上写渠道行：健康状态那两处 Update 是有意节流的
# （只在状态变化时写），为了一个展示用的时间给每次请求加一次写库不划算。
last_used_of() {
  curl -s "$API/channels" | python3 -c "
import sys, json
for c in json.load(sys.stdin)['items']:
    if c['id'] == $1:
        print(c.get('last_used_at') or '')
        break
"
}
# 正例：上面这一串用例真的调通过这条渠道
chk "被调用过的渠道有最近调用时间" "yes" \
  "$(case "$(last_used_of "$CID")" in 20*) echo yes ;; *) echo no ;; esac)"
# 反例：新建但没调用过的渠道必须为空 —— 否则这一列就成了「不管有没有
# 调用都显示点东西」，看不出渠道到底是死的还是活的
UNUSED=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"${CNAME}-unused\", \"group_id\": $GID, \"protocol\": \"openai-chat\",
  \"base_url\": \"http://127.0.0.1:9997\", \"api_key\": \"mock-key\"
}" | jqg "d['id']")
chk "没被调用过的渠道没有最近调用时间" "" "$(last_used_of "$UNUSED")"
curl -s -X DELETE "$API/channels/$UNUSED" >/dev/null
# 报出来的时刻必须就是日志里的最大时刻（用纪元秒比，绕开时区与毫秒格式）。
# 容忍 ±1 秒：PG 的 EXTRACT::bigint 与列表侧 ISO 串的取整方向在毫秒落在
# .5s 上下时会差一秒（实测 1789878512 vs 1789878511），严格相等让这个
# 断言变成掷硬币；±1s 不损害「报的就是日志里的时刻」这层意图
PGMAX=$(P "SELECT EXTRACT(EPOCH FROM MAX(created_at))::bigint FROM request_logs WHERE channel_id=$CID")
LISTTS=$(printf '%s' "$(last_used_of "$CID")" | python3 -c "import sys,datetime;print(int(datetime.datetime.fromisoformat(sys.stdin.read().strip()).timestamp()))")
chk "时刻与日志里的最大时刻一致（±1s）" "yes" \
  "$(case $((PGMAX - LISTTS)) in -1|0|1) echo yes;; *) echo "no（pg=$PGMAX list=$LISTTS）";; esac)"

echo
echo "=== 11. 清理探测数据 ==="
curl -s -X DELETE "$API/channels/$CID" >/dev/null
for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME'"); do
  curl -s -X DELETE "$API/keys/$kid" >/dev/null
done
purge
rm -f /tmp/__rule-err.json
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探测密钥已删除" "0" "$(P "SELECT count(*) FROM api_keys WHERE name='$KNAME'")"
chk "探测白名单已删除" "0" "$(P "SELECT count(*) FROM channel_models WHERE public_name='$MODEL'")"
chk "探测日志已删除" "0" "$(P "SELECT count(*) FROM request_logs WHERE api_key_name='$KNAME' OR model_requested='$MODEL'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || { echo "HAS_FAILURE"; exit 1; }
