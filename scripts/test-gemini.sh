#!/usr/bin/env bash
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"

SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"gemini-test"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
echo "对外密钥已创建"

echo
echo "===== 1. Gemini 非流式 ====="
curl -s -m 90 "$BASE/v1beta/models/deepseek-v4-flash:generateContent" \
  -H "x-goog-api-key: $SK" -H 'Content-Type: application/json' \
  -d '{"systemInstruction":{"parts":[{"text":"回答要极简"}]},"contents":[{"role":"user","parts":[{"text":"只回复:OK"}]}],"generationConfig":{"maxOutputTokens":40}}' \
  -o /tmp/g1.json -w "  HTTP %{http_code}\n"
python3 - <<'PY'
import json
d=json.load(open('/tmp/g1.json'))
if 'error' in d:
    print('  错误:', json.dumps(d, ensure_ascii=False)[:300])
else:
    for c in d.get('candidates', []):
        print('  finishReason:', c.get('finishReason'), '| role:', c.get('content',{}).get('role'))
        for p in c.get('content',{}).get('parts',[]):
            if 'text' in p: print('  text:', repr(p['text'])[:80])
            else: print(' ', json.dumps(p, ensure_ascii=False)[:90])
    u=d.get('usageMetadata',{})
    print('  usage: prompt=%s candidates=%s total=%s' % (
        u.get('promptTokenCount'), u.get('candidatesTokenCount'), u.get('totalTokenCount')))
PY

echo
echo "===== 2. Gemini 流式 ====="
curl -s -m 90 -N "$BASE/v1beta/models/deepseek-v4-flash:streamGenerateContent" \
  -H "x-goog-api-key: $SK" -H 'Content-Type: application/json' \
  -d '{"contents":[{"role":"user","parts":[{"text":"数到3"}]}],"generationConfig":{"maxOutputTokens":30}}' \
  -o /tmp/g2.sse -w "  HTTP %{http_code}\n"
echo "  分片数: $(grep -c '^data:' /tmp/g2.sse)"
echo "  --- 最后一片 ---"
grep '^data:' /tmp/g2.sse | tail -1 | head -c 500
echo
echo

echo "===== 3. 不支持的 Gemini 方法 ====="
curl -s -m 20 -X POST "$BASE/v1beta/models/deepseek-v4-flash:countTokens" \
  -H "x-goog-api-key: $SK" -H 'Content-Type: application/json' -d '{}' -w "  HTTP %{http_code}\n"
echo

echo "===== 4. 缺鉴权应为 Gemini 错误结构 ====="
curl -s -m 20 -X POST "$BASE/v1beta/models/m:generateContent" -H 'Content-Type: application/json' -d '{}'
echo
echo

echo "===== 5. 日志入站协议 ====="
sleep 2
curl -s "$BASE/api/admin/logs?page_size=3" -o /tmp/lg.json
python3 - <<'PY'
import json
for it in json.load(open('/tmp/lg.json'))['items']:
    print('  %-20s 入站 %-24s 状态 %s 输入/输出 %s/%s' % (
        it['model_requested'], it['inbound_protocol'], it['status_code'],
        it['prompt_tokens'], it['completion_tokens']))
PY
echo "DONE"
