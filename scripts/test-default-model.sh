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

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

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
# 列表的「兜底」胶囊必须按与路由同一判据回显：半残状态亮着胶囊，
# 用户会以为兜底在工作（它永远不会生效）。修复前这里回显 DEF_MODEL
LIST2=$(curl -s "$API/channels")
chk "半残状态下列表不亮兜底胶囊" "" \
  "$(echo "$LIST2" | jqg "next(c for c in d['items'] if c['name']=='$CNAME')['fallback_model']")"
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
echo "=== 8b. 保存校验：兜底目标行在白名单里但停用（同一张表单就能造出这种状态）==="
# 「先选好兜底目标、再回白名单把那一行停用」是一次提交就能带进来的状态：
# 路由 JOIN 要求 enabled = true，放行保存就是「配了不生效」。
# 修复前这里 200 通过，兜底静默失效（2026-09-21 评审发现的缺口）
R7B=$(curl -s -m 30 -o "$TMP/r7b.json" -w '%{http_code}' -X PUT "$API/channels/$CID" \
  -H 'Content-Type: application/json' \
  -d "{\"extra_config\": {\"default_model_enabled\": true, \"default_model\": \"$DEF_MODEL\"},
      \"models\": [{\"public_name\": \"$DEF_MODEL\", \"upstream_name\": \"$DEF_MODEL\", \"enabled\": false}]}")
chk "兜底目标行停用被 400 拦住" "400" "$R7B"
chkcontains "说明是停用问题" "$(cat "$TMP/r7b.json")" "停用"

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

echo
echo "=== 10. 兜底目标行配了「对外名 → 上游名」映射：映射必须照常生效 ==="
# 修复前（2026-09-21 评审实证）：兜底候选的 UpstreamName 被硬写成对外名，
# 该行配置的上游名映射被忽略 —— 上游收到它不认识的名字，复现本功能
# 要消灭的 2ms 502。端到端此前测不到：所有 DEF_MODEL 行的 upstream_name
# 都恰好等于 public_name
MAPPED_UPSTREAM="dm-mapped-upstream"
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d "{
  \"extra_config\": {\"default_model_enabled\": true, \"default_model\": \"$DEF_MODEL\"},
  \"models\": [
    {\"public_name\": \"$DEF_MODEL\", \"upstream_name\": \"$MAPPED_UPSTREAM\",
     \"input_per_1m\": \"10\", \"output_per_1m\": \"20\"}
  ]
}" >/dev/null
curl -s "$MOCK/reset" >/dev/null
R8=$(curl -s -m 30 -o "$TMP/r8.json" -w '%{http_code}' "$BASE/v1/messages" -H "x-api-key: $SK" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$CLIENT_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
chk "映射行的兜底请求成功" "200" "$R8"
chk "上游收到的是映射后的上游名，不是对外名" "$MAPPED_UPSTREAM" \
  "$(curl -s "$MOCK/stats" | jqg "d['last_request']['model']")"
LOG8=$(P "SELECT model_requested || '|' || model_upstream || '|' || fallback_mapped FROM request_logs WHERE channel_id=$CID ORDER BY id DESC LIMIT 1")
chk "日志记映射名且带兜底标记" "$CLIENT_MODEL|$MAPPED_UPSTREAM|true" "$LOG8"
COST8=$(P "SELECT pricing_snapshot->>'model_key' FROM request_logs WHERE channel_id=$CID ORDER BY id DESC LIMIT 1")
chk "计费键仍是对外名（计价按白名单行，映射不影响费用归属）" "$DEF_MODEL" "$COST8"

echo
echo "=== 11. 白名单子端点（整表 PUT / 单条 POST / 单条 DELETE）也不能让兜底失效 ==="
# 渠道表单那条路已由 ec30029 堵上（第 8b 组测的就是它），但白名单还有三个
# 自己的端点：整表替换、单条绑定、单条解绑 —— 它们才是脚本与「渠道列表那个
# 白名单编辑器」实际提交的地方（前端只走 GET + 整表 PUT）。从这一侧把兜底目标
# 删掉或停用，与在表单里做同一件事同因同果：兜底候选按 public_name JOIN 白名单，
# 配不上就不生效。只在表单那条路拦，等于把刚堵上的洞从侧门重开一次
# （2026-09-21 第三轮评审 B-中1，修复前这三条侧门全部放行，且保存成功、无提示）。
OTHER="dm-probe-other"

# 11a 整表替换里兜底目标缺席
R9=$(curl -s -m 30 -o "$TMP/r9.json" -w '%{http_code}' -X PUT "$API/channels/$CID/models" \
  -H 'Content-Type: application/json' \
  -d "{\"items\": [{\"public_name\": \"$OTHER\", \"upstream_name\": \"$OTHER\"}]}")
chk "整表替换丢掉兜底目标被 400 拦住" "400" "$R9"
chkcontains "说明是白名单里配不上" "$(cat "$TMP/r9.json")" "不在"
# 拦下之后白名单必须原样不动：先校验后写，不能拦完还留一地半成品
chk "被拦下时白名单没被改掉" "$DEF_MODEL" \
  "$(P "SELECT public_name FROM channel_models WHERE channel_id=$CID ORDER BY id LIMIT 1")"

# 11b 带着兜底目标的整表替换照常放行（别把正常保存一起拦掉）
R10=$(curl -s -m 30 -o "$TMP/r10.json" -w '%{http_code}' -X PUT "$API/channels/$CID/models" \
  -H 'Content-Type: application/json' \
  -d "{\"items\": [{\"public_name\": \"$DEF_MODEL\", \"upstream_name\": \"$MAPPED_UPSTREAM\"},
                  {\"public_name\": \"$OTHER\", \"upstream_name\": \"$OTHER\"}]}")
chk "带着兜底目标的整表替换放行" "200" "$R10"

# 11c 单条 POST 把兜底目标那一行停用（同渠道内重复对外名 = 改上游名/启用状态）
R11=$(curl -s -m 30 -o "$TMP/r11.json" -w '%{http_code}' -X POST "$API/channels/$CID/models" \
  -H 'Content-Type: application/json' \
  -d "{\"public_name\": \"$DEF_MODEL\", \"upstream_name\": \"$MAPPED_UPSTREAM\", \"enabled\": false}")
chk "单条停用兜底目标被 400 拦住" "400" "$R11"
chkcontains "说明是停用问题" "$(cat "$TMP/r11.json")" "停用"
chk "被拦下时那一行仍是启用的" "t" \
  "$(P "SELECT enabled FROM channel_models WHERE channel_id=$CID AND public_name='$DEF_MODEL'")"

# 11d 单条 POST 加一个无关模型放行
OTHER2="${OTHER}-2"
R12=$(curl -s -m 30 -o "$TMP/r12.json" -w '%{http_code}' -X POST "$API/channels/$CID/models" \
  -H 'Content-Type: application/json' \
  -d "{\"public_name\": \"$OTHER2\", \"upstream_name\": \"$OTHER2\"}")
chk "单条新增无关模型放行" "200" "$R12"

# 11e 单条 DELETE 删掉兜底目标那一行
DEF_BID=$(P "SELECT id FROM channel_models WHERE channel_id=$CID AND public_name='$DEF_MODEL'")
R13=$(curl -s -m 30 -o "$TMP/r13.json" -w '%{http_code}' -X DELETE "$API/channels/$CID/models/$DEF_BID")
chk "单条解绑兜底目标被 400 拦住" "400" "$R13"
chkcontains "说明是白名单里配不上" "$(cat "$TMP/r13.json")" "不在"
chk "被拦下时那一行还在" "$DEF_BID" \
  "$(P "SELECT id FROM channel_models WHERE channel_id=$CID AND public_name='$DEF_MODEL'")"

# 11f 单条 DELETE 删无关行放行
OTHER_BID=$(P "SELECT id FROM channel_models WHERE channel_id=$CID AND public_name='$OTHER2'")
R14=$(curl -s -m 30 -o "$TMP/r14.json" -w '%{http_code}' -X DELETE "$API/channels/$CID/models/$OTHER_BID")
chk "单条解绑无关行放行" "200" "$R14"

# 最后：侧门堵上之后兜底本身要照旧工作（别把功能一起拦死）
curl -s "$MOCK/reset" >/dev/null
R15=$(curl -s -m 30 -o "$TMP/r15.json" -w '%{http_code}' "$BASE/v1/messages" -H "x-api-key: $SK" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$CLIENT_MODEL\",\"max_tokens\":64,\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}]}")
chk "侧门封堵后兜底请求照旧成功" "200" "$R15"

purge
echo
echo "============================================"
echo "  通过 $ok 项，失败 $bad 项"
echo "============================================"
[ "$bad" -eq 0 ]
