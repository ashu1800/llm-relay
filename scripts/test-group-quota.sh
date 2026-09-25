#!/usr/bin/env bash
# 分组每分钟额度（RPM / TPM）必须真的生效，而不是只存进数据库。
#
# 这个用例用真实请求验证：把探测分组的 RPM 设成 2，连发 3 次，
# 第 3 次必须是 429 且带 Retry-After；把额度改回 0 之后立即恢复。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { db_psql -t -A -c "$1"; }

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}

MODEL=$(P "SELECT m.public_name FROM channel_models m
   JOIN channels c ON c.id = m.channel_id AND c.enabled = true
   JOIN channel_groups g ON g.id = c.group_id AND g.enabled = true
  WHERE m.enabled = true ORDER BY m.id LIMIT 1" | tr -d '[:space:]')
if [ -z "$MODEL" ]; then echo "没有可用的渠道模型，跳过"; echo ALL_PASS; exit 0; fi
echo "探针模型: $MODEL"

# 探测渠道挂到一个独立分组上，免得把真实分组的额度改来改去
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' \
  -d '{"name":"__quota_probe_group__","strategy":"weighted"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
CHID=$(P "SELECT id FROM channels WHERE enabled = true ORDER BY id LIMIT 1")
OLDGROUP=$(P "SELECT group_id FROM channels WHERE id=$CHID")
echo "探测分组 id=$GID（渠道 $CHID 原属分组 $OLDGROUP）"

cleanup() {
  P "UPDATE channels SET group_id=$OLDGROUP WHERE id=$CHID" >/dev/null
  curl -s -o /dev/null -X DELETE "$API/groups/$GID"
  # 两把探针密钥都要删：明文只在创建时返回一次，所以「建一把拿 id、再建一把拿明文」
  # 会留下两把。漏删的那把是可用密钥，且会一直堆在密钥列表里
  for kid in $(P "SELECT id FROM api_keys WHERE name LIKE 'quota-probe%'"); do
    curl -s -o /dev/null -X DELETE "$API/keys/$kid"
  done
}
trap cleanup EXIT

P "UPDATE channels SET group_id=$GID WHERE id=$CHID" >/dev/null
curl -s -o /dev/null -X PUT "$API/groups/$GID" -H 'Content-Type: application/json' -d '{"rpm":2,"tpm":0}'
# 密钥明文只在创建时返回一次，创建时就要接住它（名字固定，便于 cleanup 按前缀清理）
SKPLAIN=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' -d '{"name":"quota-probe","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")

call() {
  curl -s -o /tmp/quota.json -w "%{http_code}" -m 60 -X POST "$BASE/v1/chat/completions" \
    -H "Authorization: Bearer $SKPLAIN" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$MODEL\",\"max_tokens\":4,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
}

echo
echo "=== 1. RPM=2 时前两次放行、第三次 429 ==="
chk "第 1 次" "200" "$(call)"
chk "第 2 次" "200" "$(call)"
CODE=$(call)
chk "第 3 次（额度已用满）" "429" "$CODE"
echo "  返回体: $(head -c 200 /tmp/quota.json)"
chk "带 Retry-After" "60" "$(curl -s -D - -o /dev/null -m 30 -X POST "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $SKPLAIN" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":4,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}" \
  | tr -d '\r' | awk 'tolower($1)=="retry-after:"{print $2}')"

echo
echo "=== 2. 额度改回 0（不限制）后立即恢复 ==="
curl -s -o /dev/null -X PUT "$API/groups/$GID" -H 'Content-Type: application/json' -d '{"rpm":0}'
chk "再发一次" "200" "$(call)"

echo
echo "=== 3. TPM：第一次放行（用量滞后一拍的代价），第二次被拦 ==="
# TPM 是「按分钟对齐的固定窗口」计数，上一节的调用已经把当前窗口的 token 用掉了。
# 必须等跨过窗口边界再从 0 开始，否则这一节测到的是上一节的残留。
WAIT=$((61 - 10#$(date +%S)))
echo "  等 ${WAIT}s 跨过分钟窗口…"
sleep "$WAIT"
curl -s -o /dev/null -X PUT "$API/groups/$GID" -H 'Content-Type: application/json' -d '{"rpm":0,"tpm":1}'
chk "第一次（额度 1，尚未记账）" "200" "$(call)"
sleep 1
chk "第二次（已记账，超限）" "429" "$(call)"
echo "  返回体: $(head -c 200 /tmp/quota.json)"
curl -s -o /dev/null -X PUT "$API/groups/$GID" -H 'Content-Type: application/json' -d '{"tpm":0}'

echo
echo "=== 4. 接口层校验 ==="
chk "负数 rpm 被拒" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/groups/$GID" \
  -H 'Content-Type: application/json' -d '{"rpm":-1}')"
chk "非法颜色被拒" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/groups/$GID" \
  -H 'Content-Type: application/json' -d '{"color":"red"}')"
chk "合法颜色被接受" "200" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/groups/$GID" \
  -H 'Content-Type: application/json' -d '{"color":"#1677FF"}')"
chk "颜色已归一成小写" "#1677ff" "$(P "SELECT color FROM channel_groups WHERE id=$GID")"
chk "清空颜色（恢复自动配色）" "200" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/groups/$GID" \
  -H 'Content-Type: application/json' -d '{"color":""}')"
chk "颜色已清空" "" "$(P "SELECT color FROM channel_groups WHERE id=$GID")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
