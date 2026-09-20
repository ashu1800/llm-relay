#!/usr/bin/env bash
# 「渠道协议 = 上游协议」的 Gemini 端到端验证。
#
# 与 test-upstream-protocol.sh（Anthropic）同样的思路：mock 上游
# （scripts/proto-upstream.js 的 /v1beta/models/... 分支）按真实 Gemini 的行为
# 拒绝 OpenAI 形状的请求，并在漏掉 alt=sse 时返回 JSON 数组而不是 SSE。
# 因此「客户端拿到正常分片流」本身就证明路径与载荷都转对了。
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
chkcontains() {
  case "$3" in
    *"$2"*) echo "  [通过] $1（含「$2」）"; ok=$((ok+1)) ;;
    *) echo "  [失败] $1 未包含「$2」，实际: $(echo "$3" | head -c 220)"; bad=$((bad+1)) ;;
  esac
}
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

bash "$(dirname "$0")/ensure-proto-upstream.sh" || exit 1

GNAME="proto-gemini-group"
CNAME="proto-gemini-channel"
KNAME="proto-gemini-key"
MODEL="proto-gemini-model"
ERR_MODEL="proto-gemini-error"

purge() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do curl -s -X DELETE "$API/channels/$cid" >/dev/null; done
  for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME'"); do curl -s -X DELETE "$API/keys/$kid" >/dev/null; done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
}
purge
curl -s "$MOCK/reset" >/dev/null

echo "=== 准备：一个上游协议为 gemini-generateContent 的渠道 ==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
CID=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\",
  \"group_id\": $GID,
  \"protocol\": \"gemini-generateContent\",
  \"base_url\": \"http://127.0.0.1:9998\",
  \"api_key\": \"mock-gemini-key\",
  \"weight\": 1,
  \"models\": [
    {\"public_name\": \"$MODEL\", \"upstream_name\": \"gemini-mock-1\"},
    {\"public_name\": \"$ERR_MODEL\", \"upstream_name\": \"gemini-trigger-error\"}
  ]
}" | jqg "d['id']")
echo "  渠道 id=$CID 分组=$GID"
SK=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$KNAME\",\"allowed_groups\":[\"$GNAME\"]}" | jqg "d['key']")
chkcontains "密钥已签发" "sk-" "$SK"

echo
echo "=== 1. OpenAI 入站 -> Gemini generateContent（模型名进路径）==="
R=$(curl -s -m 30 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"system\",\"content\":\"简短回答\"},{\"role\":\"user\",\"content\":\"你好\"}],\"max_tokens\":64}")
echo "  响应: $(echo "$R" | head -c 180)"
chk "响应是 OpenAI 结构" "chat.completion" "$(echo "$R" | jqg "d['object']")"
chk "正文来自 Gemini 上游" "hello from gemini" "$(echo "$R" | jqg "d['choices'][0]['message']['content']")"
chk "结束原因已映射" "stop" "$(echo "$R" | jqg "d['choices'][0]['finish_reason']")"

STATS=$(curl -s "$MOCK/stats")
chk "上游收到的是 generateContent 方法路径" "/v1beta/models/gemini-mock-1:generateContent" "$(echo "$STATS" | jqg "d['last_path']")"
chk "上游名取自白名单映射" "gemini-mock-1" "$(echo "$STATS" | jqg "d['last_model']")"
chk "没有被上游拒绝" "0" "$(echo "$STATS" | jqg "d['rejected']")"
chk "system 进了 systemInstruction" "简短回答" "$(echo "$STATS" | jqg "d['last_request']['systemInstruction']['parts'][0]['text']")"
chk "max_tokens 进了 generationConfig" "64" "$(echo "$STATS" | jqg "d['last_request']['generationConfig']['maxOutputTokens']")"
chk "contents 只有一个 user 轮次" "1" "$(echo "$STATS" | jqg "len(d['last_request']['contents'])")"
chk "角色用的是 Gemini 的命名" "user" "$(echo "$STATS" | jqg "d['last_request']['contents'][0]['role']")"

echo
echo "=== 2. Anthropic 入站 -> Gemini 上游 -> 回给客户端 Anthropic 结构 ==="
R2=$(curl -s -m 30 "$BASE/v1/messages" -H "x-api-key: $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":32,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
echo "  响应: $(echo "$R2" | head -c 180)"
chk "响应是 Anthropic 结构" "message" "$(echo "$R2" | jqg "d['type']")"
chk "正文" "hello from gemini" "$(echo "$R2" | jqg "d['content'][0]['text']")"
chk "input_tokens 不含缓存命中（10-2）" "8" "$(echo "$R2" | jqg "d['usage']['input_tokens']")"
chk "缓存命中单独上报" "2" "$(echo "$R2" | jqg "d['usage']['cache_read_input_tokens']")"

echo
echo "=== 3. 流式：路径要带 :streamGenerateContent?alt=sse ==="
S=$(curl -s -m 30 -N "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}],\"stream\":true}")
chkcontains "分片里带正文" "hello " "$S"
chkcontains "正文拼完整" "from gemini" "$S"
chkcontains "有结束分片" "\"finish_reason\":\"stop\"" "$S"
chkcontains "有 [DONE]" "[DONE]" "$S"
case "$S" in
  *not-sse*) echo "  [失败] 上游没走 alt=sse（拿回的是 JSON 数组）"; bad=$((bad+1)) ;;
  *) echo "  [通过] 未出现 not-sse 标记，说明 alt=sse 生效"; ok=$((ok+1)) ;;
esac
chk "上游路径带 alt=sse" "/v1beta/models/gemini-mock-1:streamGenerateContent?alt=sse" "$(curl -s "$MOCK/stats" | jqg "d['last_path']")"

echo
echo "=== 4. 用量落日志（累计型 usageMetadata 只能取最后一帧）==="
sleep 2
LOG=$(curl -s "$API/logs?page_size=30" | python3 -c "
import sys, json
d = json.load(sys.stdin)
rows = [it for it in d['items'] if it.get('model_requested') == '$MODEL']
nonstream = next((r for r in rows if not r.get('stream')), None)
stream = next((r for r in rows if r.get('stream')), None)
print(json.dumps({'nonstream': nonstream, 'stream': stream}))
")
echo "  日志: $(echo "$LOG" | jqg "json.dumps(d['nonstream'])[:200]")"
chk "上游协议记为 gemini-generateContent" "gemini-generateContent" "$(echo "$LOG" | jqg "d['nonstream']['upstream_protocol']")"
# Gemini 的 promptTokenCount 是「子集口径」（含缓存命中），站内归一化成并列语义后
# prompt_tokens 只剩未命中的部分：10 - 2 = 8
chk "输入 token（已扣掉缓存命中）" "8" "$(echo "$LOG" | jqg "d['nonstream']['prompt_tokens']")"
chk "输入总量 = 未命中 + 命中" "10" "$(echo "$LOG" | jqg "d['nonstream']['prompt_tokens'] + d['nonstream']['cached_tokens']")"
chk "缓存命中（非流式那次带了 cachedContentTokenCount）" "2" "$(echo "$LOG" | jqg "d['nonstream']['cached_tokens']")"
chk "输出 token" "6" "$(echo "$LOG" | jqg "d['nonstream']['completion_tokens']")"
# 流式的 usageMetadata 是累计值：最后一帧是 3，累加会变成 1+3=4
chk "流式输出 token 取最后一帧而不是累加" "3" "$(echo "$LOG" | jqg "d['stream']['completion_tokens']")"
chk "流式也要抓到输入 token" "10" "$(echo "$LOG" | jqg "d['stream']['prompt_tokens']")"

echo
echo "=== 5. 上游报错时给出可读原因 ==="
R3=$(curl -s -m 30 -w "\n%{http_code}" "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$ERR_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"x\"}],\"max_tokens\":16}")
R3_CODE=$(echo "$R3" | tail -1)
R3_BODY=$(echo "$R3" | sed '$d')
echo "  HTTP $R3_CODE  响应: $(echo "$R3_BODY" | head -c 200)"
chk "上游 400 原样透出" "400" "$R3_CODE"
chkcontains "带上了上游的拒绝原因" "mock 拒绝了这个模型" "$R3_BODY"
chkcontains "带上了状态名（便于对照官方文档）" "INVALID_ARGUMENT" "$R3_BODY"

echo
echo "=== 6. 清理探测数据 ==="
purge
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探测密钥已删除" "0" "$(P "SELECT count(*) FROM api_keys WHERE name='$KNAME'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
