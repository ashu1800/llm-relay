#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"payload-test"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")

MODE=$(curl -s "$BASE/api/admin/settings" | python3 -c "import sys,json;print(json.load(sys.stdin)['runtime']['payload_storage_mode'])")
echo "当前留存模式: $MODE"

echo
echo "=== 1. 成功请求（errors 模式下不应留存）==="
curl -s -m 60 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":10,"messages":[{"role":"user","content":"说:好"}]}' -o /dev/null -w "  HTTP %{http_code}\n"

echo "=== 2. 失败请求（应留存）==="
curl -s -m 30 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"definitely-not-exist-model","messages":[{"role":"user","content":"hi"}]}' -o /dev/null -w "  HTTP %{http_code}\n"

sleep 3
echo
echo "=== 3. 库中报文情况 ==="
db_psql -t -A -F'|' -c \
  "SELECT p.log_id, l.status_code, l.model_requested, length(p.request_body), length(p.response_body) FROM request_payloads p JOIN request_logs l ON l.id=p.log_id ORDER BY p.id DESC LIMIT 5"
echo "  （列: log_id|状态|模型|请求体长度|响应体长度）"

echo
echo "=== 4. 详情接口返回 ==="
LID=$(curl -s "$BASE/api/admin/logs?page_size=1" | python3 -c "import sys,json;print(json.load(sys.stdin)['items'][0]['id'])")
curl -s -m 10 "$BASE/api/admin/logs/$LID" -o /tmp/detail.json
python3 - <<'PY'
import json
d = json.load(open('/tmp/detail.json'))
lg = d['log']; pl = d.get('payload')
print('  日志: 状态 %s 模型 %s 入站 %s' % (lg['status_code'], lg['model_requested'], lg['inbound_protocol']))
if not pl:
    print('  报文: 未留存（null）')
else:
    print('  请求体:', repr(pl['request_body'])[:120])
    print('  响应体:', repr(pl['response_body'])[:120])
    hdr = pl.get('request_headers') or {}
    print('  Authorization 头:', hdr.get('Authorization') or hdr.get('authorization') or '(未记录)')
    print('  Content-Type 头:', hdr.get('Content-Type') or hdr.get('content-type') or '(未记录)')
PY
echo "DONE"
