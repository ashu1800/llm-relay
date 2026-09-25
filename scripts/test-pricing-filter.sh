#!/usr/bin/env bash
# 渠道列表的「未配价」计数（unpriced_count）。
#
# 旧脚本测的是「模型定价」列表的筛选与分页 —— 那套列表随价格表一起下线了，
# 原有用例已经没有对应物。它真正守住的价值是「配了价的条目要能被挑出来看」，
# 在新结构里由渠道列表的 unpriced_count 承接：漏配价的后果是这笔调用被记成
# 0 元，而账面上完全看不出异常，只能靠列表上这个数字点名。
#
# 所以这里验三件事：计数按渠道归属（不串台）、判据与计价引擎一字不差、
# 以及配价之后计数会回落。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { db_psql -t -A -c "$1"; }
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}

# 测试数据一律 __ 前缀，清理按精确名字（不用 LIKE '__%'：下划线是通配符）
GNAME="__price-filter-group"
CA="__price-filter-ch-a"
CB="__price-filter-ch-b"
MA1="__price-filter-a-priced"
MA2="__price-filter-a-blank"
MB1="__price-filter-b-blank1"
MB2="__price-filter-b-blank2"

purge() {
  for cname in "$CA" "$CB"; do
    for cid in $(P "SELECT id FROM channels WHERE name='$cname'"); do
      curl -s -X DELETE "$API/channels/$cid" >/dev/null
    done
  done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
  P "DELETE FROM request_payloads WHERE log_id IN (SELECT id FROM request_logs WHERE model_requested IN ('$MA1','$MA2','$MB1','$MB2'))" >/dev/null
  P "DELETE FROM request_logs WHERE model_requested IN ('$MA1','$MA2','$MB1','$MB2')" >/dev/null
}

# 一次请求里把两条渠道的 (模型数, 未配价数) 都取出来：分两次查的话，
# 「计数串台」这种错误可能被两次读到的同一份错误值掩盖过去
counts2() {
  curl -s "$API/channels" | python3 -c "
import sys, json
d = json.load(sys.stdin)
want = {'$CA': None, '$CB': None}
for it in d['items']:
    if it['name'] in want:
        want[it['name']] = '%s|%s' % (it['model_count'], it['unpriced_count'])
print('%s / %s' % (want['$CA'], want['$CB']))
"
}

# 一条渠道的白名单摘要（模型名列表，按名字排序，便于定值比较）
models_of() {
  curl -s "$API/channels/$1/models" | python3 -c "
import sys, json
rows = json.load(sys.stdin).get('items') or []
print(','.join(sorted(r['public_name'] for r in rows)))
"
}

purge

echo "=== 1. 建两条渠道：A 一条有价一条没价，B 两条都没价 ==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
# 上游地址不会被拨到：本用例只读列表，不发请求
CIDA=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CA\", \"group_id\": $GID, \"protocol\": \"openai-chat\",
  \"base_url\": \"http://127.0.0.1:1\", \"api_key\": \"probe\", \"weight\": 1,
  \"models\": [
    {\"public_name\": \"$MA1\", \"input_per_1m\": \"1\", \"output_per_1m\": \"2\"},
    {\"public_name\": \"$MA2\"}
  ]}" | jqg "d['id']")
CIDB=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CB\", \"group_id\": $GID, \"protocol\": \"openai-chat\",
  \"base_url\": \"http://127.0.0.1:1\", \"api_key\": \"probe\", \"weight\": 1,
  \"models\": [{\"public_name\": \"$MB1\"}, {\"public_name\": \"$MB2\"}]}" | jqg "d['id']")
echo "  A id=$CIDA  B id=$CIDB 分组=$GID"
# 摘要按模型名排序输出，所以这里的期望值也按字典序写
chk "A 的白名单是两条" "$MA2,$MA1" "$(models_of "$CIDA")"
chk "B 的白名单是两条" "$MB1,$MB2" "$(models_of "$CIDB")"

echo
echo "=== 2. 未配价数按渠道归属：A=1、B=2（互不串台）==="
chk "A|未配价 / B|未配价" "2|1 / 2|2" "$(counts2)"

echo
echo "=== 3. 给 A 的另一条也配上价：A 归零，B 不动 ==="
curl -s -o /dev/null -X PUT "$API/channels/$CIDA/models" -H 'Content-Type: application/json' -d "{\"items\":[
  {\"public_name\":\"$MA1\",\"input_per_1m\":\"1\",\"output_per_1m\":\"2\"},
  {\"public_name\":\"$MA2\",\"input_per_1m\":\"0.5\"}]}"
chk "A 已配齐 / B 仍是 2" "2|0 / 2|2" "$(counts2)"

echo
echo "=== 4. 「没配价」的判据必须与计价引擎一致 ==="
# 引擎的判据是「四个单价全 0 且固定倍率 <= 1」。只填固定倍率 1.5 属于「配了价」
# （虽然算出来仍是 0 元）—— 判据回答的是「配没配」，不是「贵不贵」。
# 两边各写一份判据迟早会不一致：列表说配好了、引擎却当没配，是最难查的一类。
curl -s -o /dev/null -X PUT "$API/channels/$CIDB/models" -H 'Content-Type: application/json' -d "{\"items\":[
  {\"public_name\":\"$MB1\",\"multiplier\":1.5},
  {\"public_name\":\"$MB2\",\"input_per_1m\":\"0.25\"}]}"
chk "只填倍率也算已配价（B 归零）" "2|0 / 2|0" "$(counts2)"
# 倍率 0 与不传都归一成 1，仍然算未配价（0 不等于免费）
curl -s -o /dev/null -X PUT "$API/channels/$CIDB/models" -H 'Content-Type: application/json' -d "{\"items\":[
  {\"public_name\":\"$MB1\",\"multiplier\":0},
  {\"public_name\":\"$MB2\"}]}"
chk "倍率 0（归一成 1）仍算未配价" "2|0 / 2|2" "$(counts2)"

echo
echo "=== 5. 清理探测数据 ==="
curl -s -X DELETE "$API/channels/$CIDA" >/dev/null
curl -s -X DELETE "$API/channels/$CIDB" >/dev/null
purge
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name IN ('$CA','$CB')")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探测白名单已删除" "0" "$(P "SELECT count(*) FROM channel_models WHERE public_name IN ('$MA1','$MA2','$MB1','$MB2')")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || { echo "HAS_FAILURE"; exit 1; }
