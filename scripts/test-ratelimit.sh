#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"

# 密钥的每模型路由（2026-10-26 分组下线）：allowed_models 必填非空。
# 探针模型与承载它的渠道从库里现取 —— 写死的话改一次白名单这里就 400/502
RL_MODEL=$(db_psql -t -A -c \
  "SELECT m.public_name FROM channel_models m
     JOIN channels c ON c.id = m.channel_id AND c.enabled = true
    WHERE m.enabled = true ORDER BY m.id LIMIT 1")
RL_CID=$(db_psql -t -A -c \
  "SELECT m.channel_id FROM channel_models m
     JOIN channels c ON c.id = m.channel_id AND c.enabled = true
    WHERE m.enabled = true ORDER BY m.id LIMIT 1")
if [ -z "$RL_MODEL" ] || [ -z "$RL_CID" ]; then
  echo "  !! 没有任何启用的渠道模型白名单，无法建探针密钥（先给渠道配一个模型）"
  exit 1
fi
KEY_BODY="{\"name\":\"%s\",\"rate_limit_rpm\":%s,\"allowed_models\":[\"$RL_MODEL\"],\"model_channels\":{\"$RL_MODEL\":[$RL_CID]}}"

echo "=== 1. 创建限额 2 次/分钟的密钥 ==="
K1=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
  -d "$(printf "$KEY_BODY" "rpm-test-2" 2)" | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
echo "  已创建"

echo "=== 2. 创建不限流的密钥 ==="
K2=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
  -d "$(printf "$KEY_BODY" "rpm-test-unlimited" -1)" | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
LIMPREFIX=$(curl -s "$BASE/api/admin/keys" | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='rpm-test-unlimited': print(k['rate_limit_rpm'])
")
echo "  已创建，库中 rate_limit_rpm=$LIMPREFIX"

echo
echo "=== 3. 对限额密钥连发 6 次 ==="
ok=0; limited=0
for i in 1 2 3 4 5 6; do
  code=$(curl -s -o /tmp/rl.json -w "%{http_code}" -m 60 "$BASE/v1/chat/completions" \
    -H "Authorization: Bearer $K1" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$RL_MODEL\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
  if [ "$code" = "200" ]; then ok=$((ok+1)); else limited=$((limited+1)); fi
  printf "  第 %d 次: %s\n" "$i" "$code"
done
echo "  成功 $ok 次，被限 $limited 次"

echo
echo "=== 4. 被限时的响应体与 Retry-After ==="
curl -s -D /tmp/hdr.txt -o /tmp/body.json -m 20 "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $K1" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$RL_MODEL\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
grep -i "^retry-after" /tmp/hdr.txt | sed 's/^/  /' || echo "  （无 Retry-After 头）"
cat /tmp/body.json | python3 -c "import sys,json;print('  响应体:', json.dumps(json.load(sys.stdin), ensure_ascii=False))"

echo
echo "=== 5. 不限流密钥应照常通过 ==="
code=$(curl -s -o /dev/null -w "%{http_code}" -m 60 "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $K2" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$RL_MODEL\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
echo "  HTTP $code"

echo
echo "=== 6. 运行参数已生效 ==="
curl -s "$BASE/api/admin/settings" -o /tmp/st.json
python3 - <<'PY'
import json
r = json.load(open('/tmp/st.json'))['runtime']
print('  上游并发上限:', r.get('max_concurrency'))
print('  默认每分钟上限:', r.get('default_rpm'))
PY
echo "DONE"
