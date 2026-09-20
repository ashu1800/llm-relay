#!/usr/bin/env bash
# 渠道「默认模型映射」（兜底映射）的端到端验证。
#
# 背景：客户端（Claude Code）发的模型名（claude-sonnet-4-6 / claude-opus-4-7）
# 不在任何渠道白名单里，请求得到 1-3ms、token 全 0 的 502 —— 因为路由器按
# channel_models.public_name 精确匹配选渠道，匹配不到就等于「没有可用渠道」。
# 兜底映射让开了开关的渠道接住这些请求，统一改用指定模型发往上游。
#
# 上游用 anthropic mock（proto-upstream）：它会记录**实际收到的请求体**，
# 于是「模型名到底换没换」是可断言的，而不是靠猜。它同时按真实 Anthropic
# 的行为拒绝畸形请求（rejected 计数），所以「转发成功」证明请求形状也对。
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
MOCK="http://127.0.0.1:9998"
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}
chkcontains() {
  case "$2" in
    *"$3"*) echo "  [通过] $1（含「$3」）"; ok=$((ok+1)) ;;
    *) echo "  [失败] $1 未包含「$3」，实际: $(echo "$2" | head -c 240)"; bad=$((bad+1)) ;;
  esac
}
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

bash "$(dirname "$0")/ensure-proto-upstream.sh" || exit 1

GNAME="dm-probe-group"
CNAME="dm-probe-channel"
KNAME="dm-probe-key"
KNAME_STRICT="dm-probe-key-strict"
# 客户端会请求这个（模拟 Claude Code 发来的 Anthropic 侧模型名）
CLIENT_MODEL="claude-opus-4-7"
# 渠道白名单里真正登记、并作为兜底目标的模型
DEF_MODEL="dm-upstream-model"

purge() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do curl -s -X DELETE "$API/channels/$cid" >/dev/null; done
  for kid in $(P "SELECT id FROM api_keys WHERE name='$KNAME' OR name='$KNAME_STRICT'"); do curl -s -X DELETE "$API/keys/$kid" >/dev/null; done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
}
purge
curl -s "$MOCK/reset" >/dev/null

echo "=== 1. 建分组 + 渠道（只登记 $DEF_MODEL，并打开默认模型映射）==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$GNAME\",\"enabled\":true}" | jqg "d['id']")
CH=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\",
  \"group_id\": $GID,
  \"protocol\": \"anthropic-messages\",
  \"base_url\": \"http://127.0.0.1:9998\",
  \"api_key\": \"mock-anthropic-key\",
  \"extra_config\": {\"default_model_enabled\": true, \"default_model\": \"$DEF_MODEL\"},
  \"models\": [{\"public_name\": \"$DEF_MODEL\", \"upstream_name\": \"$DEF_MODEL\"}]
}")
CID=$(echo "$CH" | jqg "d['id']")
chkcontains "渠道已建立" "$CH" "\"id\""
echo "  渠道 id=$CID 分组=$GID"

# 列表接口要按与路由**同一判据**回显兜底模型，否则会出现
# 「界面上标着兜底、路由里没兜底」
LIST=$(curl -s "$API/channels")
chk "列表回显兜底模型" "$DEF_MODEL" \
  "$(echo "$LIST" | jqg "next(c for c in d['items'] if c['name']=='$CNAME')['fallback_model']")"

SK=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$KNAME\",\"allowed_groups\":[\"$GNAME\"]}" | jqg "d['key']")
chkcontains "密钥已签发" "$SK" "sk-"

echo
echo "=== 2. 请求一个不在白名单里的模型（原来必然 502）==="
curl -s "$MOCK/reset" >/dev/null
R=$(curl -s -m 30 "$BASE/v1/messages" -H "x-api-key: $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$CLIENT_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
echo "  响应: $(echo "$R" | head -c 200)"
chk "不再 502，正文来自上游" "hello from anthropic" "$(echo "$R" | jqg "d['content'][0]['text']")"

STATS=$(curl -s "$MOCK/stats")
chk "上游确实收到了请求" "1" "$(echo "$STATS" | jqg "d['anthropic_hits']")"
chk "没有被上游拒绝" "0" "$(echo "$STATS" | jqg "d['rejected']")"
# 这一条就是本次修复的核心：客户端发 claude-opus-4-7，上游应收到兜底模型名。
# 修复前它是原样转发，DeepSeek 这类上游会立刻 502（token 全 0、耗时 1-3ms）
chk "上游收到的是兜底模型名，不是客户端原名" "$DEF_MODEL" \
  "$(echo "$STATS" | jqg "d['last_request']['model']")"

echo
echo "=== 3. 日志：请求名记原名、上游名记兜底名、带兜底标记 ==="
# 注意 psql 里用 || 把 boolean 拼进字符串会转成 'true'/'false'（不是 t/f）
LOG=$(P "SELECT model_requested || '|' || model_upstream || '|' || fallback_mapped FROM request_logs WHERE channel_id=$CID ORDER BY id DESC LIMIT 1")
chk "模型名与兜底标记" "$CLIENT_MODEL|$DEF_MODEL|true" "$LOG"
# 响应里的模型名是**上游回显的那个**，不是客户端请求的名字。
# 这是既有行为、与兜底无关：精确命中但配了 upstream_name 的请求同样如此
# （见下面第 4 组：请求 $CLIENT_MODEL 会回显 exact-mapped-name）。
# 兜底沿用同一套，所以客户端会看到兜底目标名。
chk "响应回显上游名（既有行为，与兜底无关）" "$DEF_MODEL" "$(echo "$R" | jqg "d['model']")"

echo
echo "=== 4. 精确命中优先：把客户端模型名也登记进白名单后，不再走兜底 ==="
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d "{
  \"models\": [
    {\"public_name\": \"$DEF_MODEL\", \"upstream_name\": \"$DEF_MODEL\"},
    {\"public_name\": \"$CLIENT_MODEL\", \"upstream_name\": \"exact-mapped-name\"}
  ]
}" >/dev/null
curl -s "$MOCK/reset" >/dev/null
R2=$(curl -s -m 30 "$BASE/v1/messages" -H "x-api-key: $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$CLIENT_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
STATS2=$(curl -s "$MOCK/stats")
chk "走的是白名单的映射名，不是兜底目标" "exact-mapped-name" \
  "$(echo "$STATS2" | jqg "d['last_request']['model']")"
LOG2=$(P "SELECT model_upstream || '|' || fallback_mapped FROM request_logs WHERE channel_id=$CID ORDER BY id DESC LIMIT 1")
chk "日志不标兜底" "exact-mapped-name|false" "$LOG2"
# 精确命中时响应也回显上游名，与兜底一致 —— 顺便证明这个回显行为不因兜底而改变
chk "精确命中的响应同样回显上游名" "exact-mapped-name" "$(echo "$R2" | jqg "d['model']")"

echo
echo "=== 5. 密钥白名单不豁免：配了 allowed_models 就挡住兜底 ==="
SK2=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$KNAME_STRICT\",\"allowed_groups\":[\"$GNAME\"],\"allowed_models\":[\"$DEF_MODEL\"]}" | jqg "d['key']")
R3=$(curl -s -m 30 -o "$TMP/r3.json" -w '%{http_code}' "$BASE/v1/messages" -H "x-api-key: $SK2" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$CLIENT_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
# 403 而不是静默兜底：密钥上配的模型限制不能被兜底绕开 ——
# 「看起来有访问控制、实际没有」比没有这个字段更危险
chk "不在密钥白名单里的模型名被 403" "403" "$R3"
chkcontains "说明是密钥限制" "$(cat "$TMP/r3.json")" "不允许调用模型"
# 而白名单内的模型名照常走
R4=$(curl -s -m 30 -o "$TMP/r4.json" -w '%{http_code}' "$BASE/v1/messages" -H "x-api-key: $SK2" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$DEF_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
chk "密钥白名单内的模型名正常" "200" "$R4"

echo
echo "=== 6. 关掉开关：回到「没有可用渠道」 ==="
# 必须先把白名单恢复成「只登记兜底目标」：上一组把 $CLIENT_MODEL 加进去了，
# 留着的话它会被**精确命中**，关掉开关也照样 200 —— 那样这组就没测到兜底开关。
# extra_config 里只关开关、保留模型名，这样渠道配的仍是「开关关着」这个
# 合法状态（而 NOT 半残）—— 见下一组的对照
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d "{
  \"extra_config\": {\"default_model_enabled\": false, \"default_model\": \"$DEF_MODEL\"},
  \"models\": [{\"public_name\": \"$DEF_MODEL\", \"upstream_name\": \"$DEF_MODEL\"}]
}" >/dev/null
curl -s "$MOCK/reset" >/dev/null
R5=$(curl -s -m 30 -o "$TMP/r5.json" -w '%{http_code}' "$BASE/v1/messages" -H "x-api-key: $SK" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$CLIENT_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
chk "开关关掉后回到 502" "502" "$R5"
chkcontains "提示里列出范围可用的模型" "$(cat "$TMP/r5.json")" "$DEF_MODEL"
# 开关**明确关掉**是用户的决定，不是配置坏掉 —— 提示里不该把它算成半残。
# 把「关着」也报警会变成误报，而反复误报的提示会让人开始整体忽略它
if grep -q '但不会生效' "$TMP/r5.json"; then
  echo "  [失败] 开关明确关掉的渠道不该被当成半残配置报警"; bad=$((bad+1))
else
  echo "  [通过] 开关关着不算半残（不误报）"; ok=$((ok+1))
fi
# 上游一次都不该被打到：这个模型名没有任何渠道能接
chk "没有走到上游" "0" "$(curl -s "$MOCK/stats" | jqg "d['anthropic_hits']")"

echo
echo "=== 6b. 真正的半残（开关开着但兜底目标不在启用的白名单里）要在提示里点名 ==="
# 上一组把开关关掉了，这一组要**打开它**才算半残。
# 半残 = 「开关开着」但「兜底目标匹配不到启用的白名单行」。
# 造法：直接把那条白名单条目停用 —— 保存接口拦得住「同时提交半残配置」的，
# 拦不住这种分两步走的（先配好、事后停用白名单）。
# 用停用而不是删除，顺带验证「停用的条目不再是兜底目标」
# （候选查询的 JOIN 带 channel_models.enabled = true）。
P "UPDATE channels SET extra_config = jsonb_set(extra_config, '{default_model_enabled}', 'true'::jsonb) WHERE id=$CID" >/dev/null
P "UPDATE channel_models SET enabled=false WHERE channel_id=$CID AND public_name='$DEF_MODEL'" >/dev/null
R5b=$(curl -s -m 30 -o "$TMP/r5b.json" -w '%{http_code}' "$BASE/v1/messages" -H "x-api-key: $SK" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$CLIENT_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
chk "半残状态下仍是 502" "502" "$R5b"
chkcontains "提示里点名半残的兜底配置" "$(cat "$TMP/r5b.json")" "默认模型映射"
# 恢复：开关归位、白名单重新启用，供最后一组用
P "UPDATE channel_models SET enabled=true WHERE channel_id=$CID AND public_name='$DEF_MODEL'" >/dev/null
P "UPDATE channels SET extra_config = jsonb_set(extra_config, '{default_model_enabled}', 'true'::jsonb) WHERE id=$CID" >/dev/null

echo
echo "=== 7. 保存校验：开关开着却不填模型名（后端也要拦）==="
R6=$(curl -s -m 30 -o "$TMP/r6.json" -w '%{http_code}' -X PUT "$API/channels/$CID" \
  -H 'Content-Type: application/json' \
  -d "{\"extra_config\": {\"default_model_enabled\": true, \"default_model\": \"\"}}")
chk "配置半残被 400 拦住" "400" "$R6"
chkcontains "说明原因" "$(cat "$TMP/r6.json")" "默认模型"

echo "=== 8. 保存校验：默认模型不在白名单里 ==="
R7=$(curl -s -m 30 -o "$TMP/r7.json" -w '%{http_code}' -X PUT "$API/channels/$CID" \
  -H 'Content-Type: application/json' \
  -d "{\"extra_config\": {\"default_model_enabled\": true, \"default_model\": \"not-in-whitelist\"}}")
chk "指向白名单外的模型被 400 拦住" "400" "$R7"
chkcontains "说明原因" "$(cat "$TMP/r7.json")" "不在"

echo
echo "=== 9. 兜底请求按「指定模型」的单价计费 ==="
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d "{
  \"extra_config\": {\"default_model_enabled\": true, \"default_model\": \"$DEF_MODEL\"},
  \"models\": [
    {\"public_name\": \"$DEF_MODEL\", \"upstream_name\": \"$DEF_MODEL\",
     \"input_per_1m\": \"10\", \"output_per_1m\": \"20\"}
  ]
}" >/dev/null
curl -s "$MOCK/reset" >/dev/null
curl -s -m 30 "$BASE/v1/messages" -H "x-api-key: $SK" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$CLIENT_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}" >/dev/null
COST=$(P "SELECT pricing_snapshot->>'model_key' FROM request_logs WHERE channel_id=$CID ORDER BY id DESC LIMIT 1")
# 计费键是兜底目标那条白名单行的对外名 —— 客户端的模型名在渠道里没配价，
# 按它查只会得到 0 元
chk "按兜底模型的单价定价" "$DEF_MODEL" "$COST"

purge
echo
echo "============================================"
echo "  通过 $ok 项，失败 $bad 项"
echo "============================================"
[ "$bad" -eq 0 ]
