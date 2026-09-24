#!/usr/bin/env bash
set -uo pipefail

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
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"emb-test"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
curl -s -m 30 -o /tmp/emb.json -w "HTTP %{http_code}\n" "$BASE/v1/embeddings" \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"not-bound-embedding","input":"hello"}'
head -c 220 /tmp/emb.json; echo
echo "DONE"
