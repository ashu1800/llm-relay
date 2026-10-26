#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"

# 密钥的每模型路由（2026-10-26 分组下线）：allowed_models 必填非空。
# 模型与承载渠道从库里现取：渠道白名单是用户随时会改的，写死必挂
MODEL=$(db_psql -t -A -c "SELECT m.public_name FROM channel_models m JOIN channels c ON c.id = m.channel_id AND c.enabled = true WHERE m.enabled = true ORDER BY m.id LIMIT 1")
CID=$(db_psql -t -A -c "SELECT channel_id FROM channel_models WHERE public_name='$MODEL' AND enabled = true ORDER BY id LIMIT 1")
if [ -z "$MODEL" ] || [ -z "$CID" ]; then
  echo "!! 没有任何启用的渠道模型白名单，无法建探针密钥（先给渠道配一个模型）"
  exit 1
fi
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"payload-all\",\"allowed_models\":[\"$MODEL\"],\"model_channels\":{\"$MODEL\":[$CID]}}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
MODE=$(curl -s "$BASE/api/admin/settings" | python3 -c "import sys,json;print(json.load(sys.stdin)['runtime']['payload_storage_mode'])")
echo "当前留存模式: $MODE"

echo
echo "=== 非流式成功请求 ==="
curl -s -m 60 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":12,\"messages\":[{\"role\":\"user\",\"content\":\"只回复:好\"}]}" -o /dev/null -w "  HTTP %{http_code}\n"

echo "=== 流式成功请求 ==="
curl -s -m 60 -N "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":12,\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"只回复:行\"}]}" -o /dev/null -w "  HTTP %{http_code}\n"

sleep 3
echo
echo "=== 库中报文（最近 4 条）==="
db_psql -t -A -F'|' -c \
  "SELECT p.log_id, l.status_code, l.is_stream, length(p.request_body), length(p.response_body) FROM request_payloads p JOIN request_logs l ON l.id=p.log_id ORDER BY p.id DESC LIMIT 4"
echo "  （列: log_id|状态|流式|请求体长度|响应体长度）"

echo
echo "=== 流式那条的响应报文片段 ==="
db_psql -t -A -c \
  "SELECT left(p.response_body, 220) FROM request_payloads p JOIN request_logs l ON l.id=p.log_id WHERE l.is_stream=true ORDER BY p.id DESC LIMIT 1"
echo "DONE"
