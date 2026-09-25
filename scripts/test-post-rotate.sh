#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE=http://127.0.0.1:8888
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
  -d '{"name":"post-rotate","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")

echo "=== 轮换后逐渠道真实调用 ==="
for cid in $(db_psql -t -A -c "SELECT id FROM channels ORDER BY id"); do
  name=$(db_psql -t -A -c "SELECT name FROM channels WHERE id=$cid")
  # 只留这一个渠道可用，确保请求一定走它
  db_psql -t -A -c "UPDATE channels SET enabled=(id=$cid)" >/dev/null
  code=$(curl -s -o /tmp/rc.json -w "%{http_code}" -m 60 "$BASE/v1/chat/completions" \
    -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
    -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"说一个字"}]}')
  printf "  %-16s HTTP %s  " "$name" "$code"
  python3 -c "
import json
d=json.load(open('/tmp/rc.json'))
if 'choices' in d:
    print('回应:', repr(d['choices'][0]['message'].get('content',''))[:30])
else:
    print('错误:', d.get('error',{}).get('message','?')[:60])
" 2>/dev/null || echo "(解析失败)"
done
db_psql -t -A -c "UPDATE channels SET enabled=true" >/dev/null

echo
echo "=== 清理测试密钥 ==="
KID=$(curl -s "$BASE/api/admin/keys" | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='post-rotate': print(k['id'])
")
[ -n "$KID" ] && curl -s -o /dev/null -X DELETE "$BASE/api/admin/keys/$KID"
echo "  已清理"

echo
echo "=== 全接口回归 ==="
bash "$(dirname "${BASH_SOURCE[0]}")/test-regression.sh" 2>&1 | tail -9
echo DONE
