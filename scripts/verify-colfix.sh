#!/usr/bin/env bash
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1
BASE=http://127.0.0.1:8888/api/admin
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

bash scripts/redeploy.sh || exit 1

echo
echo "=== 1. 重新同步（此前 updated 恒为 0）==="
curl -s -X POST "$BASE/pricing/sync" -m 300 -o /tmp/s3.json -w "  HTTP %{http_code}\n"
python3 -c "
import json
d=json.load(open('/tmp/s3.json'))
for r in d.get('results',[]):
    print('  %-9s 新增 %-4s 更新 %-5s 未变 %-5s 跳过 %-3s 错误 %s' % (
      r.get('source'), r.get('added'), r.get('updated'), r.get('unchanged'),
      r.get('skipped_manual'), (r.get('error') or '')[:40]))
"

echo
echo "=== 2. 归属分布（修复后）==="
P "SELECT provider_id, count(*) FROM model_pricings GROUP BY provider_id ORDER BY provider_id"

echo
echo "=== 3. DeepSeek 官方定价的归属 ==="
P "SELECT provider_id, model_key, source FROM model_pricings WHERE source='official' ORDER BY model_key"

echo
echo "=== 4. LiteLLM 里的 DeepSeek 条目 ==="
P "SELECT provider_id, count(*) FROM model_pricings WHERE source='litellm' AND model_key LIKE '%deepseek%' GROUP BY provider_id"

echo
echo "=== 5. 手工改价是否真的写进去了 ==="
PID=$(P "SELECT id FROM model_pricings WHERE model_key='deepseek-flash' AND source='official' LIMIT 1")
BEFORE=$(P "SELECT input_per1_m FROM model_pricings WHERE id=$PID")
curl -s -o /dev/null -X PUT "$BASE/pricing/$PID" -H 'Content-Type: application/json' -d '{"input_per_1m":"0.15"}'
AFTER=$(P "SELECT input_per1_m FROM model_pricings WHERE id=$PID")
echo "  改价前 $BEFORE -> 改价后 $AFTER"
if [ "$BEFORE" = "$AFTER" ]; then echo "  [失败] 手工改价仍未生效"; else echo "  [通过] 手工改价已写入"; fi

echo
echo "=== 6. 真实调用仍正常 ==="
SK=$(curl -s -X POST "$BASE/keys" -H 'Content-Type: application/json' -d '{"name":"post-colfix","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
code=$(curl -s -o /tmp/c.json -w "%{http_code}" -m 60 http://127.0.0.1:8888/v1/chat/completions \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}')
echo "  HTTP $code"
KID=$(curl -s "$BASE/keys" | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='post-colfix': print(k['id'])
")
[ -n "$KID" ] && curl -s -o /dev/null -X DELETE "$BASE/keys/$KID"
echo DONE
