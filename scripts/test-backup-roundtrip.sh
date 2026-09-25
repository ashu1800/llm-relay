#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888/api/admin"
PSQL="db_psql -t -A"

q() { $PSQL -c "$1"; }

echo "=== 1. 建一个带真实密钥的测试渠道并写入模型白名单 ==="
GID=$(q "SELECT id FROM channel_groups WHERE is_default=true LIMIT 1")
# 密钥一律从环境变量取，脚本里不留明文凭据
: "${DS_KEY:?请先 export DS_KEY 与 DS_BASE}"
: "${DS_BASE:?请先 export DS_KEY 与 DS_BASE}"
CH=$(curl -s -X POST "$BASE/channels" -H 'Content-Type: application/json' \
  -d "{\"name\":\"backup-roundtrip\",\"group_id\":$GID,\"protocol\":\"openai-chat\",\"base_url\":\"$DS_BASE\",\"api_key\":\"$DS_KEY\",\"weight\":1}")
CHID=$(echo "$CH" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
echo "  渠道 id=$CHID"
BIND=$(curl -s -X POST "$BASE/channels/$CHID/models" -H 'Content-Type: application/json' \
  -d '{"public_name":"deepseek-v4-flash","upstream_name":"deepseek-v4-flash"}')
echo "  白名单响应: $(echo "$BIND" | head -c 160)"
echo "  库中白名单条数: $(q "SELECT count(*) FROM channel_models WHERE channel_id=$CHID")"

ENC_BEFORE=$(q "SELECT api_key_enc FROM channels WHERE id=$CHID")
echo "  恢复前密文: ${ENC_BEFORE:0:32}... (长度 ${#ENC_BEFORE})"

echo
echo "=== 2. 先验证这个渠道真的能打通 ==="
SK=$(curl -s -X POST "$BASE/keys" -H 'Content-Type: application/json' -d '{"name":"restore-probe","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
# 临时停掉另外两个渠道，确保请求走到测试渠道
q "UPDATE channels SET enabled=false WHERE id<>$CHID" > /dev/null
code=$(curl -s -o /tmp/probe.json -w "%{http_code}" -m 60 "http://127.0.0.1:8888/v1/chat/completions" \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"说一个字"}]}')
echo "  调用测试渠道: HTTP $code"
python3 -c "
import json
d=json.load(open('/tmp/probe.json'))
print('  回应:', d['choices'][0]['message'].get('content','')[:20] if 'choices' in d else d)
" 2>/dev/null || true

echo
echo "=== 3. 导出后删除该渠道 ==="
curl -s -m 20 "$BASE/backup/export" -o /tmp/rt.json
curl -s -o /dev/null -w "  删除渠道 HTTP %{http_code}\n" -X DELETE "$BASE/channels/$CHID"
echo "  删除后该渠道存在数: $(q "SELECT count(*) FROM channels WHERE name='backup-roundtrip'")"
echo "  删除后绑定数: $(q "SELECT count(*) FROM channel_models WHERE channel_id=$CHID")"

echo
echo "=== 4. 导入恢复 ==="
curl -s -X POST "$BASE/backup/import" -H 'Content-Type: application/json' \
  --data-binary @/tmp/rt.json -o /tmp/rt-imp.json -w "  HTTP %{http_code}\n"
python3 -c "import json;d=json.load(open('/tmp/rt-imp.json'));print('  新增:',d['created'],'| 告警:',d['warnings'] or '(无)')"

echo
echo "=== 5. 比对恢复后的密文 ==="
NEWID=$(q "SELECT id FROM channels WHERE name='backup-roundtrip'")
ENC_AFTER=$(q "SELECT api_key_enc FROM channels WHERE id=$NEWID")
echo "  新渠道 id=$NEWID"
echo "  恢复后密文: ${ENC_AFTER:0:32}... (长度 ${#ENC_AFTER})"
if [ "$ENC_BEFORE" = "$ENC_AFTER" ]; then
  echo "  密文逐字节一致 -> 密钥可用，无需重填"
else
  echo "  密文不一致！备份未能还原密钥"
fi

echo
echo "=== 6. 用恢复出来的渠道真实调用一次 ==="
q "UPDATE channels SET enabled=false WHERE id<>$NEWID" > /dev/null
code=$(curl -s -o /tmp/probe2.json -w "%{http_code}" -m 60 "http://127.0.0.1:8888/v1/chat/completions" \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"说一个字"}]}')
echo "  经恢复渠道调用: HTTP $code"
python3 -c "
import json
d=json.load(open('/tmp/probe2.json'))
print('  回应:', d['choices'][0]['message'].get('content','')[:20] if 'choices' in d else d)
" 2>/dev/null || true

echo
echo "=== 7. 清理 ==="
q "UPDATE channels SET enabled=true" > /dev/null
curl -s -o /dev/null -X DELETE "$BASE/channels/$NEWID"
$PSQL -c "DELETE FROM api_keys WHERE name IN ('restore-probe','backup-roundtrip')" > /dev/null 2>&1 || true
curl -s "$BASE/keys" | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='restore-probe': print('  待清理密钥 id', k['id'])
"
echo "  渠道数: $(curl -s "$BASE/channels" | python3 -c "import sys,json;print(json.load(sys.stdin)['total'])")"
echo "  启用渠道数: $(q "SELECT count(*) FROM channels WHERE enabled=true")"
echo DONE
