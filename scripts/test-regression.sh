#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888/api/admin"

echo "=== 管理接口回归 ==="
fail=0
total=0
for ep in "stats/summary?range=30d" "stats/timeseries?range=30d" "stats/models?range=30d" "stats/channels?range=30d" "stats/heatmap?range=30d" "routing/analysis?range=30d" "settings" "pricing?page_size=2" "pricing/history" "logs?page_size=2" "channels" "groups" "models" "keys" "channel-templates" "system/info"; do
  total=$((total+1))
  code=$(curl -s -o /dev/null -w "%{http_code}" -m 10 "$BASE/$ep")
  if [ "$code" != "200" ]; then
    fail=$((fail+1))
    printf "  %-34s %s  <-- 异常\n" "$ep" "$code"
  fi
done
echo "  管理接口 $total 个，非 200 的 $fail 个"

echo
echo "=== 四种协议端点连通性 ==="
SK=$(curl -s -X POST "$BASE/keys" -H 'Content-Type: application/json' -d '{"name":"regress","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
HOST="http://127.0.0.1:8888"

CHAT='{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}'
ANTH='{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}'
RESP='{"model":"deepseek-v4-flash","input":"hi","max_output_tokens":5}'
GEMI='{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":5}}'

probe() {
  local name="$1" url="$2" body="$3"
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" -m 60 -X POST "$url" \
    -H "x-api-key: $SK" -H "x-goog-api-key: $SK" -H "Authorization: Bearer $SK" \
    -H 'Content-Type: application/json' -d "$body")
  printf "  %-18s %s\n" "$name" "$code"
}

probe "openai-chat"      "$HOST/v1/chat/completions"                          "$CHAT"
probe "anthropic"        "$HOST/v1/messages"                                  "$ANTH"
probe "openai-responses" "$HOST/v1/responses"                                 "$RESP"
probe "gemini"           "$HOST/v1beta/models/deepseek-v4-flash:generateContent" "$GEMI"
echo DONE
