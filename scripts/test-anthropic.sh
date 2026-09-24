#!/usr/bin/env bash
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"

SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"anthropic-test"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
echo "对外密钥已创建"

echo
echo "===== 1. Anthropic 非流式请求 ====="
curl -s -m 90 "$BASE/v1/messages" \
  -H "x-api-key: $SK" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":40,"system":"回答要极简","messages":[{"role":"user","content":"只回复:OK"}]}' \
  -o /tmp/anth.json -w "  HTTP %{http_code}\n"
python3 - <<'PY'
import json
d=json.load(open('/tmp/anth.json'))
if 'error' in d:
    print('  错误:', json.dumps(d, ensure_ascii=False)[:300])
else:
    print('  type:', d.get('type'), '| role:', d.get('role'), '| id:', d.get('id'))
    print('  stop_reason:', d.get('stop_reason'))
    for b in d.get('content', []):
        t = b.get('type')
        if t == 'text': print('  text 块:', repr(b.get('text'))[:80])
        elif t == 'thinking': print('  thinking 块:', repr(b.get('thinking'))[:60])
        else: print(' ', t, json.dumps(b, ensure_ascii=False)[:100])
    u=d.get('usage',{})
    print('  usage: input=%s output=%s cache_read=%s' % (u.get('input_tokens'),u.get('output_tokens'),u.get('cache_read_input_tokens')))
PY

echo
echo "===== 2. Anthropic 流式请求 ====="
curl -s -m 90 -N "$BASE/v1/messages" \
  -H "x-api-key: $SK" -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":30,"messages":[{"role":"user","content":"数到3"}],"stream":true}' \
  -o /tmp/anth.sse -w "  HTTP %{http_code}\n"
echo "  --- 事件序列 ---"
grep '^event:' /tmp/anth.sse | head -20
echo "  --- 收尾事件 ---"
tail -8 /tmp/anth.sse

echo
echo "===== 3. 缺失鉴权应 401 且为 Anthropic 错误结构 ====="
curl -s -m 20 -X POST "$BASE/v1/messages" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}' | head -c 250
echo
echo
echo "===== 4. 日志中的入站协议 ====="
sleep 2
curl -s "$BASE/api/admin/logs?page_size=3" -o /tmp/lg.json
python3 - <<'PY'
import json
for it in json.load(open('/tmp/lg.json'))['items']:
    print('  %-22s 入站 %-20s 出站 %-12s 状态 %s 输入/输出 %s/%s' % (
        it['model_requested'], it['inbound_protocol'], it['upstream_protocol'],
        it['status_code'], it['prompt_tokens'], it['completion_tokens']))
PY
echo "DONE"
