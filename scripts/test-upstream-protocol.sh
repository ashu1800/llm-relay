#!/usr/bin/env bash
# 「渠道协议 = 上游协议」的端到端验证：上游是 Anthropic 原生端点时，
# 无论客户端用 OpenAI 还是 Anthropic 协议，都要能被转换过去并转回来。
#
# 判据来自 mock 上游（scripts/proto-upstream.js）：它按真实 Anthropic 的行为
# 拒绝 OpenAI 形状的请求（未知字段、缺 max_tokens、system 在 messages 里）。
# 所以「转发成功」本身就证明请求被转换了，而不是被一个宽容的 mock 放过。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
MOCK="http://127.0.0.1:9998"
P() { db_psql -t -A -c "$1"; }

ok=0; bad=0
# chk <说明> <期望> <实际>
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

GNAME="proto-upstream-group"
CNAME="proto-anthropic-channel"
KNAME="proto-upstream-key"
MODEL="proto-chat-model"
ERR_MODEL="proto-error-model"

purge() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do curl -s -X DELETE "$API/channels/$cid" >/dev/null; done
  for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME'"); do curl -s -X DELETE "$API/keys/$kid" >/dev/null; done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
}
purge
curl -s "$MOCK/reset" >/dev/null

echo "=== 准备：一个上游协议为 anthropic-messages 的渠道 ==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
CH=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\",
  \"group_id\": $GID,
  \"protocol\": \"anthropic-messages\",
  \"base_url\": \"http://127.0.0.1:9998\",
  \"api_key\": \"mock-anthropic-key\",
  \"weight\": 1,
  \"models\": [
    {\"public_name\": \"$MODEL\", \"upstream_name\": \"claude-mock-1\"},
    {\"public_name\": \"$ERR_MODEL\", \"upstream_name\": \"trigger-error\"}
  ]
}")
CID=$(echo "$CH" | jqg "d['id']")
echo "  渠道 id=$CID 分组=$GID"
SK=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$KNAME\",\"allowed_groups\":[\"$GNAME\"]}" | jqg "d['key']")
chkcontains "密钥已签发" "sk-" "$SK"

echo
echo "=== 1. 客户端用 OpenAI 协议调，上游收到的是 Anthropic 请求 ==="
R=$(curl -s -m 30 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"system\",\"content\":\"简短回答\"},{\"role\":\"user\",\"content\":\"你好\"}],\"max_tokens\":64,\"stream\":false}")
echo "  响应: $(echo "$R" | head -c 200)"
chk "响应是 OpenAI 结构" "chat.completion" "$(echo "$R" | jqg "d['object']")"
chk "正文来自 Anthropic 上游" "hello from anthropic" "$(echo "$R" | jqg "d['choices'][0]['message']['content']")"
chk "结束原因已映射" "stop" "$(echo "$R" | jqg "d['choices'][0]['finish_reason']")"

STATS=$(curl -s "$MOCK/stats")
chk "上游只收到 Anthropic 路径" "/v1/messages" "$(echo "$STATS" | jqg "d['last_path']")"
chk "没有被上游拒绝" "0" "$(echo "$STATS" | jqg "d['rejected']")"
chk "上游名换成了白名单里的映射名" "claude-mock-1" "$(echo "$STATS" | jqg "d['last_request']['model']")"
chk "system 提到顶层" "简短回答" "$(echo "$STATS" | jqg "d['last_request']['system']")"
chk "messages 里不再有 system" "1" "$(echo "$STATS" | jqg "len(d['last_request']['messages'])")"
chk "max_tokens 按客户端给的值透传" "64" "$(echo "$STATS" | jqg "d['last_request']['max_tokens']")"

# OpenAI 的 max_tokens 是可选的，Anthropic 的是必填：不传时必须兜底，
# 否则客户端会收到来自上游的 "max_tokens: Field required"，很难联想到中转站
curl -s -m 30 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}" >/dev/null
chk "不传 max_tokens 时被兜底成 4096" "4096" "$(curl -s "$MOCK/stats" | jqg "d['last_request']['max_tokens']")"

echo
echo "=== 2. 客户端用 Anthropic 协议调，回给客户端的也是 Anthropic 结构 ==="
R2=$(curl -s -m 30 "$BASE/v1/messages" -H "x-api-key: $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":32,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
echo "  响应: $(echo "$R2" | head -c 200)"
chk "响应是 Anthropic 结构" "message" "$(echo "$R2" | jqg "d['type']")"
chk "正文" "hello from anthropic" "$(echo "$R2" | jqg "d['content'][0]['text']")"
chk "stop_reason 已映射" "end_turn" "$(echo "$R2" | jqg "d['stop_reason']")"
chk "input_tokens 不含缓存命中" "12" "$(echo "$R2" | jqg "d['usage']['input_tokens']")"
chk "缓存命中单独上报" "3" "$(echo "$R2" | jqg "d['usage']['cache_read_input_tokens']")"

echo
echo "=== 3. 流式：OpenAI 入站 -> OpenAI 分片 ==="
S=$(curl -s -m 30 -N "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}],\"max_tokens\":32,\"stream\":true}")
chkcontains "分片里带正文" "hello " "$S"
chkcontains "正文拼完整" "from anthropic" "$S"
chkcontains "有结束分片" "finish_reason" "$S"
chkcontains "有 [DONE]" "[DONE]" "$S"
if echo "$S" | grep -q "^event:"; then
  echo "  [失败] 下游流里不该出现 Anthropic 的 event: 行"; bad=$((bad+1))
else
  echo "  [通过] 下游流里没有 event: 行（已转成 OpenAI 的 data 分片）"; ok=$((ok+1))
fi

echo
echo "=== 4. 流式：Anthropic 入站 -> Anthropic 事件流 ==="
S2=$(curl -s -m 30 -N "$BASE/v1/messages" -H "x-api-key: $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":32,\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
chkcontains "有 message_start" "message_start" "$S2"
chkcontains "有 content_block_delta" "content_block_delta" "$S2"
chkcontains "有 message_stop" "message_stop" "$S2"
chkcontains "正文" "from anthropic" "$S2"

echo
echo "=== 5. 用量与费用落到日志（不能退化成按长度估算）==="
sleep 2
LOG=$(curl -s "$API/logs?page_size=50" | python3 -c "
import sys, json
d = json.load(sys.stdin)
rows = [it for it in d['items'] if it.get('model_requested') == '$MODEL' and not it.get('stream')]
# 两种入站协议各取一条：日志按 id 倒序，第一条是最新的（Anthropic 入站那次）
by_proto = {}
for it in rows:
    by_proto.setdefault(it['inbound_protocol'], it)
openai_row = by_proto.get('openai-chat')
anthropic_row = by_proto.get('anthropic-messages')
print(json.dumps({
    'openai': openai_row, 'anthropic': anthropic_row,
    'counts': {k: len([r for r in rows if r['inbound_protocol'] == k]) for k in by_proto},
}))
")
echo "  日志条数: $(echo "$LOG" | jqg "d['counts']")"
chk "OpenAI 入站那次记下了" "openai-chat" "$(echo "$LOG" | jqg "d['openai']['inbound_protocol']")"
chk "上游协议记为 anthropic-messages" "anthropic-messages" "$(echo "$LOG" | jqg "d['openai']['upstream_protocol']")"
chk "输入 token 已归一化（12 未命中）" "12" "$(echo "$LOG" | jqg "d['openai']['prompt_tokens']")"
chk "缓存命中单独计量" "3" "$(echo "$LOG" | jqg "d['openai']['cached_tokens']")"
chk "输出 token" "7" "$(echo "$LOG" | jqg "d['openai']['completion_tokens']")"
chk "Anthropic 入站那次也记下了" "anthropic-messages" "$(echo "$LOG" | jqg "d['anthropic']['inbound_protocol']")"
chk "它有独立的费用快照（金额非空）" "True" "$(echo "$LOG" | jqg "bool(d['anthropic']['estimated_cost'])")"

echo
echo "=== 6. 上游报错时给出可读原因，而不是整段 JSON ==="
R3=$(curl -s -m 30 -w "\n%{http_code}" "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$ERR_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"x\"}],\"max_tokens\":16}")
R3_CODE=$(echo "$R3" | tail -1)
R3_BODY=$(echo "$R3" | sed '$d')
echo "  HTTP $R3_CODE  响应: $(echo "$R3_BODY" | head -c 220)"
chk "上游 400 原样透出" "400" "$R3_CODE"
chkcontains "带上了上游的拒绝原因，而不是整段 JSON" "mock 拒绝了这个模型" "$R3_BODY"
chkcontains "错误类型是 upstream_error" "upstream_error" "$R3_BODY"

echo
echo "=== 7. 清理探测数据 ==="
purge
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探测密钥已删除" "0" "$(P "SELECT count(*) FROM api_keys WHERE name='$KNAME'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
