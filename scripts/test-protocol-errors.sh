#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
echo "=== Anthropic 端点缺鉴权（应为 type=error 结构）==="
curl -s -m 20 -X POST "$BASE/v1/messages" -H 'Content-Type: application/json' \
  -d '{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}'
echo
echo "=== OpenAI 端点缺鉴权（应保持 OpenAI 结构）==="
curl -s -m 20 -X POST "$BASE/v1/chat/completions" -H 'Content-Type: application/json' \
  -d '{"model":"m","messages":[]}'
echo
echo "=== 向量化端点连通性（用不存在的模型看错误是否规范）==="
# 密钥的每模型路由（2026-10-26 分组下线）：allowed_models 必填非空，
# 这里任意挑一个启用模型满足校验 —— 请求本身仍用白名单外的鬼名字
MODEL=$(db_psql -t -A -c "SELECT m.public_name FROM channel_models m JOIN channels c ON c.id = m.channel_id AND c.enabled = true WHERE m.enabled = true ORDER BY m.id LIMIT 1")
CID=$(db_psql -t -A -c "SELECT channel_id FROM channel_models WHERE public_name='$MODEL' AND enabled = true ORDER BY id LIMIT 1")
if [ -z "$MODEL" ] || [ -z "$CID" ]; then
  echo "!! 没有任何启用的渠道模型白名单，无法建探针密钥（先给渠道配一个模型）"
  exit 1
fi
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"emb-test\",\"allowed_models\":[\"$MODEL\"],\"model_channels\":{\"$MODEL\":[$CID]}}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
curl -s -m 30 -o /tmp/emb.json -w "HTTP %{http_code}\n" "$BASE/v1/embeddings" \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"not-bound-embedding","input":"hello"}'
head -c 220 /tmp/emb.json; echo
echo "DONE"
