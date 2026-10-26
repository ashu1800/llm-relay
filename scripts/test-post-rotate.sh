#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE=http://127.0.0.1:8888
# 密钥的每模型路由（2026-10-26 分组下线）：allowed_models 必填非空。
# 只要有任意渠道承载 deepseek-v4-flash 就能验「逐渠道真实调用」；
# 没有的话跳过（这个脚本本来就是手动排障用的探针）
MCID=$(db_psql -t -A -c "SELECT channel_id FROM channel_models WHERE public_name='deepseek-v4-flash' AND enabled = true ORDER BY id LIMIT 1" 2>/dev/null)
if [ -n "$MCID" ]; then
  SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
    -d "{\"name\":\"post-rotate\",\"rate_limit_rpm\":-1,\"allowed_models\":[\"deepseek-v4-flash\"],\"model_channels\":{\"deepseek-v4-flash\":[$MCID]}}" \
    | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
else
  echo "  !! 渠道白名单里没有 deepseek-v4-flash，无法建探针密钥"
  exit 1
fi

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
