#!/usr/bin/env bash
# 思考强度的跨协议落地（第三轮审查 R-中1，设计文档 thinking-effort-mapping）。
#
# 单测（convert/thinking_map_test.go）证明映射表本身是对的；这个脚本证明的是
# **真机链路上没有哪一步把它丢掉**：渠道配置 → 选路 → 请求改写 → 上游实际收到。
# 判据取自 mock 上游（scripts/proto-upstream.js）记下的 last_request 原文 ——
# 不是「响应 200 就算过」，因为强度丢失恰恰不会让请求失败，它只会让模型
# 按上游默认档思考，然后静默改变行为与计费。
#
# 覆盖方向：
#   openai-chat 入站 → anthropic 上游（丙的原始场景）
#   anthropic 入站   → anthropic 上游（无损往返不得退化成档位重写）
#   openai-chat 入站 → gemini 上游
#   anthropic 入站   → gemini 上游（跨协议档位换算）
#   openai-chat 入站 → openai 兼容上游（跨协议归一出来的 off/auto 必须剔除）
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
MOCK="http://127.0.0.1:9998"
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }
# lr <表达式>：在 mock 记录的最后一个请求体上取字段，缺失时回「<缺失>」
lr() { curl -s "$MOCK/stats" | python3 -c "import sys,json;d=json.load(sys.stdin);r=d.get('last_request') or {};print($1)"; }
rejected() { curl -s "$MOCK/stats" | jqg "d['rejected']"; }

bash "$(dirname "$0")/ensure-proto-upstream.sh" || exit 1

G_A="think-anthropic-group"; C_A="think-anthropic-channel"; M_A="think-model-anthropic"
G_G="think-gemini-group";    C_G="think-gemini-channel";    M_G="think-model-gemini"
G_O="think-openai-group";    C_O="think-openai-channel";    M_O="think-model-openai"
# 密钥名由渠道名派生（见 make_channel），统一用一个前缀清理
KEY_LIKE="think-%-channel-key"

purge() {
  for c in "$C_A" "$C_G" "$C_O"; do
    for cid in $(P "SELECT id FROM channels WHERE name='$c'"); do curl -s -X DELETE "$API/channels/$cid" >/dev/null; done
  done
  for kid in $(P "SELECT id FROM api_keys WHERE name LIKE '$KEY_LIKE'"); do curl -s -X DELETE "$API/keys/$kid" >/dev/null; done
  for g in "$G_A" "$G_G" "$G_O"; do P "DELETE FROM channel_groups WHERE name='$g'" >/dev/null; done
}
purge
curl -s "$MOCK/reset" >/dev/null

# make_channel <分组名> <渠道名> <协议> <模型名>
# 每个分组只挂一条渠道：同组多渠道会按权重轮询，断言就指不准是哪条收到的了。
make_channel() {
  local gname="$1" cname="$2" proto="$3" model="$4" gid cid
  gid=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$gname\"}" | jqg "d['id']")
  cid=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
    \"name\": \"$cname\",
    \"group_id\": $gid,
    \"protocol\": \"$proto\",
    \"base_url\": \"$MOCK\",
    \"api_key\": \"mock-key\",
    \"weight\": 1,
    \"models\": [{\"public_name\": \"$model\", \"upstream_name\": \"mock-upstream-model\"}]
  }" | jqg "d['id']")
  curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
    -d "{\"name\":\"${cname}-key\",\"allowed_groups\":[\"$gname\"]}" | jqg "d['key']"
}

echo "=== 准备：三条渠道（anthropic / gemini / openai-chat 上游），各挂一个独立分组 ==="
SK_A=$(make_channel "$G_A" "$C_A" "anthropic-messages" "$M_A")
SK_G=$(make_channel "$G_G" "$C_G" "gemini-generateContent" "$M_G")
SK_O=$(make_channel "$G_O" "$C_O" "openai-chat" "$M_O")
case "$SK_A$SK_G$SK_O" in
  *sk-*) echo "  三把探测密钥已签发" ;;
  *) echo "  密钥签发失败，无法继续"; purge; exit 1 ;;
esac

# oai <key> <model> <额外字段片段>
oai() {
  curl -s -o /dev/null -m 30 "$BASE/v1/chat/completions" -H "Authorization: Bearer $1" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$2\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]$3}"
}
# anth <key> <model> <额外字段片段>
anth() {
  curl -s -o /dev/null -m 30 "$BASE/v1/messages" -H "x-api-key: $1" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$2\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]$3}"
}

echo
echo "=== 1. OpenAI 入站 -> Anthropic 上游：强度必须落地（丙的原始场景）==="
oai "$SK_A" "$M_A" ',"reasoning_effort":"high"'
chk "上游路径是 /v1/messages" "/v1/messages" "$(lr "d['last_path']")"
chk "上游收到 output_config.effort" "high" "$(lr "r.get('output_config',{}).get('effort','<缺失>')")"
chk "没有被上游拒绝" "0" "$(rejected)"
chk "不得发 budget_tokens（新模型会 400）" "<缺失>" "$(lr "r.get('thinking',{}).get('budget_tokens','<缺失>')")"
chk "通用语字段不漏给上游" "<缺失>" "$(lr "r.get('reasoning_effort','<缺失>')")"

oai "$SK_A" "$M_A" ',"reasoning_effort":"off"'
chk "off 落成 thinking.type=disabled" "disabled" "$(lr "r.get('thinking',{}).get('type','<缺失>')")"

oai "$SK_A" "$M_A" ',"reasoning_effort":"minimal"'
chk "minimal 向上收敛到 low（Anthropic 没有这一档）" "low" "$(lr "r.get('output_config',{}).get('effort','<缺失>')")"

echo
echo "=== 2. Anthropic 入站 -> Anthropic 上游：无损往返优先于档位重写 ==="
anth "$SK_A" "$M_A" ',"thinking":{"type":"enabled","budget_tokens":2048}'
chk "thinking.type 原样" "enabled" "$(lr "r.get('thinking',{}).get('type','<缺失>')")"
chk "budget_tokens 精确值原样（未被档位化）" "2048" "$(lr "r.get('thinking',{}).get('budget_tokens','<缺失>')")"
chk "无损往返时不再补 output_config" "<缺失>" "$(lr "r.get('output_config',{}).get('effort','<缺失>')")"

anth "$SK_A" "$M_A" ',"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}'
chk "4.6 代 output_config.effort 往返存活" "max" "$(lr "r.get('output_config',{}).get('effort','<缺失>')")"
chk "4.6 代 thinking.type 往返存活" "adaptive" "$(lr "r.get('thinking',{}).get('type','<缺失>')")"

echo
echo "=== 3. OpenAI 入站 -> Gemini 上游：档位换算成预算数值 ==="
oai "$SK_G" "$M_G" ',"reasoning_effort":"medium"'
chk "上游路径是 generateContent" "mock-upstream-model" "$(lr "d['last_model']")"
chk "medium -> thinkingBudget=8192" "8192" "$(lr "r.get('generationConfig',{}).get('thinkingConfig',{}).get('thinkingBudget','<缺失>')")"
chk "没有被上游拒绝" "0" "$(rejected)"

oai "$SK_G" "$M_G" ',"reasoning_effort":"off"'
chk "off -> thinkingBudget=0" "0" "$(lr "r.get('generationConfig',{}).get('thinkingConfig',{}).get('thinkingBudget','<缺失>')")"

echo
echo "=== 4. Anthropic 入站 -> Gemini 上游：跨协议的档位换算 ==="
anth "$SK_G" "$M_G" ',"thinking":{"type":"enabled","budget_tokens":20000}'
chk "budget 20000 -> high -> 16384" "16384" "$(lr "r.get('generationConfig',{}).get('thinkingConfig',{}).get('thinkingBudget','<缺失>')")"

echo
echo "=== 5. OpenAI 兼容上游：剔除它表达不了的档位 ==="
oai "$SK_O" "$M_O" ',"reasoning_effort":"off"'
chk "off 被剔除（上游对 off 一律 400）" "<缺失>" "$(lr "r.get('reasoning_effort','<缺失>')")"

oai "$SK_O" "$M_O" ',"reasoning_effort":"none"'
chk "原生 none 原样透传（不替客户端做取舍）" "none" "$(lr "r.get('reasoning_effort','<缺失>')")"

oai "$SK_O" "$M_O" ',"reasoning_effort":"high"'
chk "原生 high 原样透传" "high" "$(lr "r.get('reasoning_effort','<缺失>')")"

echo
echo "=== 6. 清理探测数据 ==="
purge
curl -s "$MOCK/reset" >/dev/null
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name IN ('$C_A','$C_G','$C_O')")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name IN ('$G_A','$G_G','$G_O')")"
chk "探测密钥已删除" "0" "$(P "SELECT count(*) FROM api_keys WHERE name LIKE '$KEY_LIKE'")"
bash "$(dirname "$0")/purge-test-logs.sh" >/dev/null 2>&1

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
