#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888"
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"payload-all"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
MODE=$(curl -s "$BASE/api/admin/settings" | python3 -c "import sys,json;print(json.load(sys.stdin)['runtime']['payload_storage_mode'])")
echo "当前留存模式: $MODE"

echo
echo "=== 非流式成功请求 ==="
curl -s -m 60 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":12,"messages":[{"role":"user","content":"只回复:好"}]}' -o /dev/null -w "  HTTP %{http_code}\n"

echo "=== 流式成功请求 ==="
curl -s -m 60 -N "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":12,"stream":true,"messages":[{"role":"user","content":"只回复:行"}]}' -o /dev/null -w "  HTTP %{http_code}\n"

sleep 3
echo
echo "=== 库中报文（最近 4 条）==="
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -F'|' -c \
  "SELECT p.log_id, l.status_code, l.is_stream, length(p.request_body), length(p.response_body) FROM request_payloads p JOIN request_logs l ON l.id=p.log_id ORDER BY p.id DESC LIMIT 4"
echo "  （列: log_id|状态|流式|请求体长度|响应体长度）"

echo
echo "=== 流式那条的响应报文片段 ==="
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT left(p.response_body, 220) FROM request_payloads p JOIN request_logs l ON l.id=p.log_id WHERE l.is_stream=true ORDER BY p.id DESC LIMIT 1"
echo "DONE"
