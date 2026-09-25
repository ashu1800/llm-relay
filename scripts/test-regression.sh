#!/usr/bin/env bash
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888/api/admin"

echo "=== 管理接口回归 ==="
fail=0
total=0
# 模型管理（/models）与定价同步历史（/pricing 及 /pricing/history）已随
# 「模型商 / 模型表 / 自动同步」一起删除：模型目录现在由渠道白名单派生，
# 定价全部手工录入 —— 列表里的 pricing 一项当时漏删，一直报一个假异常
for ep in "stats/summary?range=30d" "stats/timeseries?range=30d" "stats/models?range=30d" "stats/channels?range=30d" "stats/heatmap?range=30d" "settings" "logs?page_size=2" "channels" "groups" "keys" "system/info"; do
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
# 建一把临时密钥用于连通性探测，用完必须删掉。
# 这里原来只建不删，跑一次就在库里留一把，累计到十几把 ——
# 测试污染用户数据是很难察觉的问题：没人会去看密钥列表里多出来的东西。
KEY_JSON=$(curl -s -X POST "$BASE/keys" -H 'Content-Type: application/json' -d '{"name":"regress-tmp","rate_limit_rpm":-1}')
SK=$(echo "$KEY_JSON" | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
KEY_ID=$(echo "$KEY_JSON" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
cleanup() {
  [ -n "${KEY_ID:-}" ] && curl -s -o /dev/null -X DELETE "$BASE/keys/$KEY_ID"
}
trap cleanup EXIT
HOST="http://127.0.0.1:8888"

# 探测用的模型名必须**从库里现取**，不能写死：
# 白名单是用户随时会改的（实测只留 deepseek-v4.1-flash 就把写死 deepseek-v4-flash
# 的探针全变成 502），写死的话每次改白名单都会误报成「协议转换坏了」
PROBE_MODEL=$(db_psql -t -A -c \
  "SELECT m.public_name FROM channel_models m
     JOIN channels c ON c.id = m.channel_id AND c.enabled = true
     JOIN channel_groups g ON g.id = c.group_id AND g.enabled = true
    WHERE m.enabled = true ORDER BY m.id LIMIT 1" | tr -d '[:space:]')
if [ -z "$PROBE_MODEL" ]; then
  echo "  没有任何启用的渠道模型白名单，协议探针无法验证（先给渠道配一个模型）"
  echo DONE
  exit 0
fi
echo "  探针模型（取自渠道白名单）: $PROBE_MODEL"

CHAT="{\"model\":\"$PROBE_MODEL\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
ANTH="{\"model\":\"$PROBE_MODEL\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
RESP="{\"model\":\"$PROBE_MODEL\",\"input\":\"hi\",\"max_output_tokens\":5}"
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
probe "gemini"           "$HOST/v1beta/models/$PROBE_MODEL:generateContent"      "$GEMI"
echo DONE
