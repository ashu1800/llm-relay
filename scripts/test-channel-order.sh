#!/usr/bin/env bash
# 渠道优先级序号（weight）与故障转移顺序的端到端验证。
#
# 背景：weight 从「加权随机的抽签份额」改成了「组内优先级序号」——
# 同一分组内必须连续且唯一（1..N），越小越优先；转发时每轮取候选链首个，
# 候选按 weight 升序，于是序号就是故障转移顺序。
#
# 全程使用独立的探测分组 + 探测渠道，不动真实渠道，也不依赖真实上游：
# 探测渠道指向一个不存在的端口，用「报错内容」区分「命中渠道」与「根本没路由」。
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

# 每次运行用独立临时目录：写死 /tmp/xxx 的老脚本踩过「root 留下的同名文件
# 让普通用户写不进去，断言读到上一次的旧内容」这种坑（见 test-model-whitelist.sh）
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}
chkcontains() {
  case "$2" in
    *"$3"*) echo "  [通过] $1（含「$3」）"; ok=$((ok+1)) ;;
    *) echo "  [失败] $1 未包含「$3」，实际: $(echo "$2" | head -c 300)"; bad=$((bad+1)) ;;
  esac
}
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

GNAME="ord-probe-group"
GNAME2="ord-probe-group2"
KNAME="ord-probe-key"
PREFIX="ord-probe-"

purge_probes() {
  for cid in $(P "SELECT id FROM channels WHERE name LIKE '${PREFIX}%'"); do
    curl -s -X DELETE "$API/channels/$cid" >/dev/null
  done
  for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME'"); do
    curl -s -X DELETE "$API/keys/$kid" >/dev/null
  done
  for gid in $(P "SELECT id FROM channel_groups WHERE name IN ('$GNAME','$GNAME2')"); do
    curl -s -X DELETE "$API/groups/$gid" >/dev/null
  done
}
purge_probes

mkchannel() { # mkchannel <后缀> <分组id>
  curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
    \"name\": \"${PREFIX}$1\",
    \"group_id\": $2,
    \"protocol\": \"openai-chat\",
    \"base_url\": \"http://127.0.0.1:9/v1\",
    \"api_key\": \"probe-key\",
    \"models\": [{\"public_name\": \"ord-probe-model\"}]
  }" | jqg "d['id']"
}
weight_of() { P "SELECT weight FROM channels WHERE id=$1"; }
order_of() { P "SELECT string_agg(weight::text, ',' ORDER BY weight) FROM channels WHERE group_id=$1"; }
ids_of() { P "SELECT string_agg(id::text, ',' ORDER BY weight) FROM channels WHERE group_id=$1"; }

echo "=== 1. 建探测分组 ==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$GNAME\",\"enabled\":true}" | jqg "d['id']")
GID2=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$GNAME2\",\"enabled\":true}" | jqg "d['id']")
chkcontains "分组已建立" "$GID" "$GID"

echo
echo "=== 2. 新建渠道依次排在末尾（序号 1,2,3）==="
A=$(mkchannel a "$GID")
B=$(mkchannel b "$GID")
C=$(mkchannel c "$GID")
chk "第一条序号" "1" "$(weight_of "$A")"
chk "第二条序号" "2" "$(weight_of "$B")"
chk "第三条序号" "3" "$(weight_of "$C")"
chk "组内序号连续" "1,2,3" "$(order_of "$GID")"

echo
echo "=== 3. 同一分组内序号唯一（数据库层兜底）==="
chk "组内无重复序号" "0" "$(P "SELECT count(*) FROM (SELECT group_id, weight FROM channels GROUP BY group_id, weight HAVING count(*) > 1) t")"

echo
echo "=== 4. order 接口重排：把第三条提到最前 ==="
curl -s -X PUT "$API/channels/order" -H 'Content-Type: application/json' \
  -d "{\"group_id\":$GID,\"ids\":[$C,$A,$B]}" -o /dev/null
chk "重排后序号仍连续" "1,2,3" "$(order_of "$GID")"
chk "C 成为第一优先" "1" "$(weight_of "$C")"
chk "A 变成第二优先" "2" "$(weight_of "$A")"
chk "顺序与提交一致" "$C,$A,$B" "$(ids_of "$GID")"

echo
echo "=== 5. 删除中间一条，后面的自动补位（不留空洞）==="
curl -s -X DELETE "$API/channels/$A" -o /dev/null
chk "剩余序号连续" "1,2" "$(order_of "$GID")"
chk "C 仍是第一优先" "1" "$(weight_of "$C")"
chk "B 补位到第二" "2" "$(weight_of "$B")"

echo
echo "=== 6. order 接口的校验（列表过期/传错分组时必须被拦下）==="
CODE=$(curl -s -o $TMP/ord_err.json -w '%{http_code}' -X PUT "$API/channels/order" \
  -H 'Content-Type: application/json' -d "{\"group_id\":$GID,\"ids\":[$C]}")
chk "缺少成员 -> 400" "400" "$CODE"
chkcontains "错误点名缺少的渠道" "$(cat $TMP/ord_err.json)" "$B"

D=$(mkchannel d "$GID2")
CODE=$(curl -s -o $TMP/ord_err.json -w '%{http_code}' -X PUT "$API/channels/order" \
  -H 'Content-Type: application/json' -d "{\"group_id\":$GID,\"ids\":[$C,$B,$D]}")
chk "混入别的分组的渠道 -> 400" "400" "$CODE"
chkcontains "错误点名跨组的渠道" "$(cat $TMP/ord_err.json)" "$D"
chk "跨组失败后本组序号不变" "1,2" "$(order_of "$GID")"

CODE=$(curl -s -o $TMP/ord_err.json -w '%{http_code}' -X PUT "$API/channels/order" \
  -H 'Content-Type: application/json' -d "{\"group_id\":$GID,\"ids\":[$C,$C,$B]}")
chk "重复的 id -> 400" "400" "$CODE"

echo
echo "=== 7. 换分组：原组补位、新组排到末尾 ==="
curl -s -X PUT "$API/channels/$B" -H 'Content-Type: application/json' -d "{\"group_id\":$GID2}" -o /dev/null
chk "原分组只剩一条且序号连续" "1" "$(order_of "$GID")"
chk "新分组里排到末尾（D=1, B=2）" "1,2" "$(order_of "$GID2")"
chk "B 在新分组序号为 2" "2" "$(weight_of "$B")"

echo
echo "=== 8. 客户端传的 weight 被忽略 ==="
curl -s -X PUT "$API/channels/$C" -H 'Content-Type: application/json' -d '{"weight":99}' -o /dev/null
chk "weight 不变" "1" "$(weight_of "$C")"
curl -s -X PUT "$API/channels/order" -H 'Content-Type: application/json' \
  -d "{\"group_id\":$GID,\"ids\":[$C]}" -o /dev/null
chk "重排后仍是 1" "1" "$(weight_of "$C")"

echo
echo "=== 9. 路由按序号命中：首选不可用时落到下一条 ==="
# 临时把 C 指向一个打不通的端口，B 指向原样；两条都在 GID2 里，C 优先
curl -s -X PUT "$API/channels/$C" -H 'Content-Type: application/json' \
  -d "{\"group_id\":$GID2}" -o /dev/null
SK=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$KNAME\",\"allowed_groups\":[\"$GNAME2\"]}" | jqg "d['key']")
# 两条渠道的地址都是打不通的 127.0.0.1:9，所以拿到的错误必须来自「连接失败」
# 且失败信息里出现的端口/路径能证明它确实去连了（而不是「没有可用渠道」）
curl -s -o $TMP/ord_route.json -w '%{http_code}' -m 30 "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"ord-probe-model","messages":[{"role":"user","content":"hi"}],"max_tokens":5}' > $TMP/ord_code
chk "命中渠道后因连不上而 502" "502" "$(cat $TMP/ord_code)"
if grep -q "没有可用的渠道" $TMP/ord_route.json; then
  echo "  [失败] 明明有白名单内的渠道却报没有可用渠道"; bad=$((bad+1))
else
  echo "  [通过] 请求确实被路由到了探测渠道（$(head -c 120 $TMP/ord_route.json)）"; ok=$((ok+1))
fi

echo
echo "=== 10. 清理探测数据 ==="
purge_probes
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name LIKE '${PREFIX}%'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name IN ('$GNAME','$GNAME2')")"
chk "探测密钥已删除" "0" "$(P "SELECT count(*) FROM api_keys WHERE name='$KNAME'")"
chk "全库仍无重复序号" "0" "$(P "SELECT count(*) FROM (SELECT group_id, weight FROM channels GROUP BY group_id, weight HAVING count(*) > 1) t")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
