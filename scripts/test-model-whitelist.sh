#!/usr/bin/env bash
# 渠道模型白名单的端到端验证。
#
# 背景：模型表被删除后，白名单是「某个模型在系统里存在」的唯一依据。
# 因此这条链路必须整条通：建渠道时写白名单 -> 列表能看到 -> /v1/models 汇总 ->
# 路由按白名单命中 -> 白名单外的模型报错时给出「现在能调什么」。
#
# 全程使用独立的探测分组 + 探测密钥，不动真实渠道，也不依赖真实上游：
# 探测渠道指向一个不存在的端口，用「报错内容」区分「命中渠道」与「根本没路由」。
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

ok=0; bad=0
# chk <说明> <期望> <实际>
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}
chkcontains() {
  case "$2" in
    *"$3"*) echo "  [通过] $1（含「$3」）"; ok=$((ok+1)) ;;
    *) echo "  [失败] $1 未包含「$3」，实际: $(echo "$2" | head -c 200)"; bad=$((bad+1)) ;;
  esac
}
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

GNAME="wl-probe-group"
CNAME="wl-probe-channel"
KNAME="wl-probe-key"

purge_probes() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do
    curl -s -X DELETE "$API/channels/$cid" >/dev/null
  done
  for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME'"); do
    curl -s -X DELETE "$API/keys/$kid" >/dev/null
  done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
}
purge_probes

echo "=== 1. 建探测分组与渠道（白名单 3 条，其中一条上游名留空）==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$GNAME\",\"enabled\":true}" | jqg "d['id']")
chkcontains "分组已建立" "$GID" "$GID"

CH=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\",
  \"group_id\": $GID,
  \"protocol\": \"openai-chat\",
  \"base_url\": \"http://127.0.0.1:9/v1\",
  \"api_key\": \"probe-key\",
  \"weight\": 1,
  \"models\": [
    {\"public_name\": \"wl-probe-a\"},
    {\"public_name\": \"wl-probe-b\", \"upstream_name\": \"upstream-b-real\"},
    {\"public_name\": \"wl-probe-c\", \"enabled\": false}
  ]
}")
CID=$(echo "$CH" | jqg "d['id']")
echo "  渠道 id=$CID"
chk "库中白名单条数" "3" "$(P "SELECT count(*) FROM channel_models WHERE channel_id=$CID")"
chk "上游名留空时与对外名相同" "wl-probe-a" "$(P "SELECT upstream_name FROM channel_models WHERE channel_id=$CID AND public_name='wl-probe-a'")"
chk "显式上游名被保留" "upstream-b-real" "$(P "SELECT upstream_name FROM channel_models WHERE channel_id=$CID AND public_name='wl-probe-b'")"
chk "未传 enabled 默认启用" "t" "$(P "SELECT enabled FROM channel_models WHERE channel_id=$CID AND public_name='wl-probe-a'")"
chk "显式 enabled=false 生效" "f" "$(P "SELECT enabled FROM channel_models WHERE channel_id=$CID AND public_name='wl-probe-c'")"
# 白名单是唯一一份模型清单，停用只能靠 enabled —— 存不进 false 就等于停不掉
chk "库里确实有停用条目" "1" "$(P "SELECT count(*) FROM channel_models WHERE channel_id=$CID AND enabled=false")"

echo
echo "=== 2. 渠道列表要带上白名单摘要 ==="
LIST=$(curl -s "$API/channels?group_id=$GID")
chk "列表里该渠道的 model_count" "3" "$(echo "$LIST" | jqg "[c for c in d['items'] if c['id']==$CID][0]['model_count']")"
chk "列表里带出模型名" "wl-probe-a" "$(echo "$LIST" | jqg "[c for c in d['items'] if c['id']==$CID][0]['models'][0]")"

echo
echo "=== 3. 白名单读接口 ==="
curl -s "$API/channels/$CID/models" | python3 -c "
import sys,json
items=json.load(sys.stdin)['items']
print('  条目:', [(i['public_name'], i['upstream_name'], i['enabled']) for i in items])
print('  按对外名排序:', [i['public_name'] for i in items])
"

echo
echo "=== 4. 非法白名单必须当场被拒 ==="
code=$(curl -s -o /tmp/wl.json -w '%{http_code}' -X POST "$API/channels" -H 'Content-Type: application/json' \
  -d "{\"name\":\"wl-dup\",\"group_id\":$GID,\"base_url\":\"http://127.0.0.1:9\",\"models\":[{\"public_name\":\"x\"},{\"public_name\":\"x\"}]}")
chk "重复对外名 -> 400" "400" "$code"
chkcontains "报错说明是重复" "$(cat /tmp/wl.json)" "重复"
code=$(curl -s -o /tmp/wl.json -w '%{http_code}' -X POST "$API/channels" -H 'Content-Type: application/json' \
  -d "{\"name\":\"wl-empty-name\",\"group_id\":$GID,\"base_url\":\"http://127.0.0.1:9\",\"models\":[{\"public_name\":\"  \"}]}")
chk "空对外名 -> 400" "400" "$code"
code=$(curl -s -o /tmp/wl.json -w '%{http_code}' -X PUT "$API/channels/999999/models" -H 'Content-Type: application/json' \
  -d '{"items":[{"public_name":"ghost"}]}')
chk "给不存在的渠道写白名单 -> 404" "404" "$code"

echo
echo "=== 5. 改渠道时白名单是整表替换，不传则不动 ==="
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' \
  -d '{"models":[{"public_name":"wl-probe-d"}]}' -o /tmp/wl.json
chk "整表替换后条数" "1" "$(P "SELECT count(*) FROM channel_models WHERE channel_id=$CID")"
chk "替换后只剩新条目" "wl-probe-d" "$(P "SELECT public_name FROM channel_models WHERE channel_id=$CID")"
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d '{"weight":7}' -o /tmp/wl.json
chk "PUT 不带 models 时白名单不动" "wl-probe-d" "$(P "SELECT public_name FROM channel_models WHERE channel_id=$CID")"
chk "渠道字段确实改了" "7" "$(P "SELECT weight FROM channels WHERE id=$CID")"
# 还原成 3 条，供后面的路由用例使用
curl -s -X PUT "$API/channels/$CID/models" -H 'Content-Type: application/json' \
  -d '{"items":[{"public_name":"wl-probe-a"},{"public_name":"wl-probe-b","upstream_name":"upstream-b-real"},{"public_name":"wl-probe-c","enabled":false}]}' -o /dev/null
chk "整表写回后条数" "3" "$(P "SELECT count(*) FROM channel_models WHERE channel_id=$CID")"

echo
echo "=== 5b. 单条接口也能把已有条目停用（false 不能被零值语义吞掉）==="
curl -s -X POST "$API/channels/$CID/models" -H 'Content-Type: application/json' \
  -d '{"public_name":"wl-probe-a","upstream_name":"wl-probe-a","enabled":false}' -o /dev/null
chk "重复 POST 不新增条目" "3" "$(P "SELECT count(*) FROM channel_models WHERE channel_id=$CID")"
chk "重复 POST 视为停用该条" "f" "$(P "SELECT enabled FROM channel_models WHERE channel_id=$CID AND public_name='wl-probe-a'")"
curl -s -X POST "$API/channels/$CID/models" -H 'Content-Type: application/json' \
  -d '{"public_name":"wl-probe-a","enabled":true}' -o /dev/null
chk "再 POST 一次能重新启用" "t" "$(P "SELECT enabled FROM channel_models WHERE channel_id=$CID AND public_name='wl-probe-a'")"

echo
echo "=== 6. /v1/models 汇总白名单（去重、只列启用的）==="
SK=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$KNAME\",\"allowed_groups\":[\"$GNAME\"]}" | jqg "d['key']")
MODELS=$(curl -s "$BASE/v1/models" -H "Authorization: Bearer $SK")
echo "  返回: $(echo "$MODELS" | jqg "[m['id'] for m in d['data']]")"
chkcontains "含 wl-probe-a" "$MODELS" "wl-probe-a"
chkcontains "含 wl-probe-b" "$MODELS" "wl-probe-b"
chk "停用条目不入列表" "0" "$(echo "$MODELS" | jqg "sum(1 for m in d['data'] if m['id']=='wl-probe-c')")"

echo
echo "=== 7. 路由按白名单命中（探测渠道的地址打不通，报错必须来自连接而不是「没有可用渠道」）==="
curl -s -o /tmp/wl_route.json -w '%{http_code}' -m 30 "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"wl-probe-b","messages":[{"role":"user","content":"hi"}],"max_tokens":5}' > /tmp/wl_code
chk "命中白名单 -> 502（上游连不上）" "502" "$(cat /tmp/wl_code)"
if grep -q "没有可用的渠道" /tmp/wl_route.json; then
  echo "  [失败] 白名单里的模型竟然没被路由"; bad=$((bad+1))
else
  echo "  [通过] 请求确实被路由到了探测渠道（$(head -c 120 /tmp/wl_route.json)）"; ok=$((ok+1))
fi

echo
echo "=== 8. 白名单外的模型：报错必须列出当前可用的模型 ==="
curl -s -o /tmp/wl_miss.json -w '%{http_code}' -m 30 "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"wl-probe-nope","messages":[{"role":"user","content":"hi"}],"max_tokens":5}' > /tmp/wl_miss_code
chk "白名单外的模型 -> 502" "502" "$(cat /tmp/wl_miss_code)"
chkcontains "报错说明没有可用渠道" "$(cat /tmp/wl_miss.json)" "没有可用的渠道"
chkcontains "报错列出可用模型 wl-probe-a" "$(cat /tmp/wl_miss.json)" "wl-probe-a"

echo
echo "=== 9. 停用渠道后它的模型不再可用 ==="
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d '{"enabled":false}' -o /dev/null
MODELS2=$(curl -s "$BASE/v1/models" -H "Authorization: Bearer $SK")
chk "停用渠道后 wl-probe-a 消失" "0" "$(echo "$MODELS2" | jqg "sum(1 for m in d['data'] if m['id']=='wl-probe-a')")"
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d '{"enabled":true}' -o /dev/null

echo
echo "=== 10. 清理探测数据 ==="
purge_probes
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探测白名单已级联清除" "0" "$(P "SELECT count(*) FROM channel_models WHERE public_name LIKE 'wl-probe-%'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探测密钥已删除" "0" "$(P "SELECT count(*) FROM api_keys WHERE name='$KNAME'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
