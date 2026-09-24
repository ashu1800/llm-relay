#!/usr/bin/env bash
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"

SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"responses-test"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
echo "对外密钥已创建"

echo
echo "===== 1. Responses 非流式（字符串 input）====="
curl -s -m 90 "$BASE/v1/responses" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","instructions":"回答要极简","input":"只回复:OK","max_output_tokens":40}' \
  -o /tmp/r1.json -w "  HTTP %{http_code}\n"
python3 - <<'PY'
import json
d=json.load(open('/tmp/r1.json'))
if 'error' in d:
    print('  错误:', json.dumps(d, ensure_ascii=False)[:300])
else:
    print('  object:', d.get('object'), '| status:', d.get('status'), '| id:', d.get('id'))
    print('  output_text:', repr(d.get('output_text'))[:90])
    for it in d.get('output', []):
        t=it.get('type')
        if t=='message':
            print('  message 项:', repr(''.join(p.get('text','') for p in it.get('content',[])))[:70])
        elif t=='reasoning':
            print('  reasoning 项:', repr(it.get('summary'))[:70])
        else:
            print(' ', t, json.dumps(it, ensure_ascii=False)[:90])
    u=d.get('usage',{})
    print('  usage: input=%s output=%s total=%s cached=%s' % (
        u.get('input_tokens'), u.get('output_tokens'), u.get('total_tokens'),
        (u.get('input_tokens_details') or {}).get('cached_tokens')))
PY

echo
echo "===== 2. Responses 流式 ====="
curl -s -m 90 -N "$BASE/v1/responses" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","input":"数到3","max_output_tokens":30,"stream":true}' \
  -o /tmp/r2.sse -w "  HTTP %{http_code}\n"
echo "  --- 事件序列（去重）---"
grep '^event:' /tmp/r2.sse | uniq -c | head -20
echo "  --- 收尾事件 ---"
tail -4 /tmp/r2.sse | head -c 700
echo
echo

echo "===== 3. Responses 端点缺鉴权（应为 Responses 错误结构）====="
curl -s -m 20 -X POST "$BASE/v1/responses" -H 'Content-Type: application/json' -d '{"model":"m","input":"hi"}'
echo
echo
echo "===== 4. 日志入站协议 ====="
sleep 2
curl -s "$BASE/api/admin/logs?page_size=3" -o /tmp/lg.json
python3 - <<'PY'
import json
for it in json.load(open('/tmp/lg.json'))['items']:
    print('  %-20s 入站 %-18s 状态 %s 输入/输出 %s/%s' % (
        it['model_requested'], it['inbound_protocol'], it['status_code'],
        it['prompt_tokens'], it['completion_tokens']))
PY
echo "DONE"
