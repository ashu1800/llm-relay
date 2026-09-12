#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888"

echo "=== 1. 创建限额 2 次/分钟的密钥 ==="
K1=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
  -d '{"name":"rpm-test-2","rate_limit_rpm":2}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
echo "  已创建"

echo "=== 2. 创建不限流的密钥 ==="
K2=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
  -d '{"name":"rpm-test-unlimited","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
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
    -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}')
  if [ "$code" = "200" ]; then ok=$((ok+1)); else limited=$((limited+1)); fi
  printf "  第 %d 次: %s\n" "$i" "$code"
done
echo "  成功 $ok 次，被限 $limited 次"

echo
echo "=== 4. 被限时的响应体与 Retry-After ==="
curl -s -D /tmp/hdr.txt -o /tmp/body.json -m 20 "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $K1" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}'
grep -i "^retry-after" /tmp/hdr.txt | sed 's/^/  /' || echo "  （无 Retry-After 头）"
cat /tmp/body.json | python3 -c "import sys,json;print('  响应体:', json.dumps(json.load(sys.stdin), ensure_ascii=False))"

echo
echo "=== 5. 不限流密钥应照常通过 ==="
code=$(curl -s -o /dev/null -w "%{http_code}" -m 60 "$BASE/v1/chat/completions" \
  -H "Authorization: Bearer $K2" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}')
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
