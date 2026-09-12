#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888"
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"gem-stream"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")

echo "=== Gemini 流式（放宽 token 预算）==="
curl -s -m 120 -N "$BASE/v1beta/models/deepseek-v4-flash:streamGenerateContent" \
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
