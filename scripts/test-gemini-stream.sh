#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"

# 密钥的每模型路由（2026-10-26 分组下线）：allowed_models 必填非空
MODEL=$(db_psql -t -A -c "SELECT m.public_name FROM channel_models m JOIN channels c ON c.id = m.channel_id AND c.enabled = true WHERE m.enabled = true ORDER BY m.id LIMIT 1")
CID=$(db_psql -t -A -c "SELECT m.channel_id FROM channel_models m JOIN channels c ON c.id = m.channel_id AND c.enabled = true WHERE public_name='$MODEL' AND m.enabled = true ORDER BY m.id LIMIT 1")
if [ -z "$MODEL" ] || [ -z "$CID" ]; then
  echo "!! 没有任何启用的渠道模型白名单，无法建探针密钥（先给渠道配一个模型）"
  exit 1
fi
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"gem-stream\",\"allowed_models\":[\"$MODEL\"],\"model_channels\":{\"$MODEL\":[$CID]}}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")

echo "=== Gemini 流式（放宽 token 预算）==="
curl -s -m 120 -N "$BASE/v1beta/models/$MODEL:streamGenerateContent" \
  -H "x-goog-api-key: $SK" -H 'Content-Type: application/json' \
  -d '{"contents":[{"role":"user","parts":[{"text":"用一句话介绍杭州"}]}],"generationConfig":{"maxOutputTokens":200}}' \
  -o /tmp/g3.sse -w "  HTTP %{http_code}\n"
echo "  分片数: $(grep -c '^data:' /tmp/g3.sse)"
python3 - <<'PY'
import json
chunks=[json.loads(l[6:]) for l in open('/tmp/g3.sse') if l.startswith('data: ')]
thought=''; text=''
for c in chunks:
    for cand in c.get('candidates',[]):
        for p in cand.get('content',{}).get('parts',[]):
            if p.get('thought'): thought+=p.get('text','')
            else: text+=p.get('text','')
print('  思维链字符数:', len(thought))
print('  正文:', repr(text)[:110])
fin=[c.get('candidates',[{}])[0].get('finishReason') for c in chunks if c.get('candidates',[{}])[0].get('finishReason')]
print('  finishReason 出现于:', fin)
u=chunks[-1].get('usageMetadata',{}) if chunks else {}
print('  末片 usage:', u)
PY
echo "DONE"
