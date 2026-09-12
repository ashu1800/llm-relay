#!/usr/bin/env bash
# 分组限定模型商的回归验证。
#
# 约定：分组归属某个模型商，组内只允许该模型商的模型参与路由。
# 这里验证四层：
#   1. 新建分组必须选模型商
#   2. 组里的渠道跟随分组的模型商（不一致直接拒）
#   3. 绑定时拒绝别的模型商的模型
#   4. 路由层真的按分组过滤 —— 用 SQL 造一条越界绑定，看请求是否被挡下
set -uo pipefail
BASE=http://127.0.0.1:8888/api/admin
HOST=http://127.0.0.1:8888
pass=0; fail=0
chk() { # chk 描述 期望 实际
  if [ "$2" = "$3" ]; then pass=$((pass+1)); printf "  [通过] %s\n" "$1"
  else fail=$((fail+1)); printf "  [失败] %s  期望 %s 实际 %s\n" "$1" "$2" "$3"; fi
}
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1" 2>&1; }
gprov() { curl -s "$BASE/groups" | python3 -c "
import sys,json
for g in json.load(sys.stdin)['items']:
    if g['id']==$1: print(g['provider_id'])
"; }
cprov() { curl -s "$BASE/channels" | python3 -c "
import sys,json
for c in json.load(sys.stdin)['items']:
    if c['id']==$1: print(c['provider_id'])
"; }
mprov() { curl -s "$BASE/models" | python3 -c "
import sys,json
for m in json.load(sys.stdin)['items']:
    if m['id']==$1: print(m['provider_id'])
"; }
jsonfield() { curl -s -o /tmp/gp.json -w "%{http_code}" "$@"; }

# purge_probes 按名字清掉探针数据。
# 开头与结尾都要跑：上一次跑到一半失败时，残留的同名数据会让这次的「建组」直接 409，
# 后面每一条都跟着失败，看起来像功能坏了 —— 排查成本远高于多写这几行。
purge_probes() {
  local kind id
  for kind in channels models groups; do
    for n in __probe_chan_scope__ __probe_chan_bad__ __probe_scope_model__ __probe_group_ds__ __probe_g_none__ __probe_g_bad__; do
      id=$(curl -s "$BASE/$kind" | python3 -c "
import sys,json
for x in json.load(sys.stdin)['items']:
    if x.get('name')=='$n' or x.get('public_name')=='$n': print(x['id'])
" 2>/dev/null)
      [ -n "$id" ] && curl -s -o /dev/null -X DELETE "$BASE/$kind/$id"
    done
  done
  id=$(curl -s "$BASE/keys" | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='__probe_scope_key__': print(k['id'])
" 2>/dev/null)
  [ -n "$id" ] && curl -s -o /dev/null -X DELETE "$BASE/keys/$id"
  return 0
}

echo "=== 0. 清理上一次残留的探针数据 ==="
purge_probes
echo "  已清理"

echo
echo "=== 1. 新建分组必须选模型商 ==="
code=$(jsonfield -X POST "$BASE/groups" -H 'Content-Type: application/json' -d '{"name":"__probe_g_none__"}')
chk "不选模型商 -> 400" "400" "$code"
python3 -c "import json;print('  ',json.load(open('/tmp/gp.json'))['error']['message'])"
code=$(jsonfield -X POST "$BASE/groups" -H 'Content-Type: application/json' -d '{"name":"__probe_g_bad__","provider_id":999}')
chk "模型商不存在 -> 400" "400" "$code"
GID=$(curl -s -X POST "$BASE/groups" -H 'Content-Type: application/json' \
  -d '{"name":"__probe_group_ds__","provider_id":2}' | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))")
chk "选了 DeepSeek -> 建组成功" "2" "$(gprov "$GID")"

echo
echo "=== 2. 组内渠道跟随分组的模型商 ==="
CID=$(curl -s -X POST "$BASE/channels" -H 'Content-Type: application/json' \
  -d "{\"name\":\"__probe_chan_scope__\",\"base_url\":\"http://127.0.0.1:9/v1\",\"api_key\":\"sk-probe\",\"group_id\":$GID}" \
  | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))")
chk "不填模型商 -> 继承分组的 2" "2" "$(cprov "$CID")"
code=$(jsonfield -X POST "$BASE/channels" -H 'Content-Type: application/json' \
  -d "{\"name\":\"__probe_chan_bad__\",\"base_url\":\"http://127.0.0.1:9/v1\",\"api_key\":\"sk-probe\",\"group_id\":$GID,\"provider_id\":1}")
chk "填别的模型商 -> 400" "400" "$code"
python3 -c "import json;print('  ',json.load(open('/tmp/gp.json'))['error']['message'])"

echo
echo "=== 3. 绑定别的模型商的模型应被拒 ==="
# gpt-5.6-sol 是 OpenAI 的模型（provider_id=1），不属于这个 DeepSeek 分组
code=$(jsonfield -X POST "$BASE/channels/$CID/models" -H 'Content-Type: application/json' -d '{"public_name":"gpt-5.6-sol"}')
chk "绑 OpenAI 的模型 -> 400" "400" "$code"
python3 -c "import json;print('  ',json.load(open('/tmp/gp.json'))['error']['message'])"
# 新模型名走自动创建：必须归到分组的模型商，而不是写死的 OpenAI
curl -s -o /dev/null -X POST "$BASE/channels/$CID/models" -H 'Content-Type: application/json' -d '{"public_name":"__probe_scope_model__"}'
NID=$(curl -s "$BASE/models" | python3 -c "
import sys,json
for m in json.load(sys.stdin)['items']:
    if m['public_name']=='__probe_scope_model__': print(m['id'])
")
chk "自动创建的模型归到分组模型商(2)" "2" "$(mprov "$NID")"

echo
echo "=== 4. 路由层按分组过滤（用 SQL 造一条越界绑定）==="
# 把它挪到「不限」的默认分组下，再手工插一条「OpenAI 分组里绑 DeepSeek 模型」的越界绑定
P "UPDATE channels SET group_id=1 WHERE id=$CID" >/dev/null
P "UPDATE channel_groups SET provider_id=1 WHERE id=$GID" >/dev/null
P "UPDATE channels SET group_id=$GID WHERE id=$CID" >/dev/null
P "DELETE FROM channel_models WHERE channel_id=$CID" >/dev/null
P "INSERT INTO channel_models (channel_id, model_id, upstream_name, enabled, created_at, updated_at) VALUES ($CID, $NID, '__probe_scope_model__', true, now(), now())" >/dev/null
chk "越界绑定已造好" "1" "$(P "SELECT count(*) FROM channel_models WHERE channel_id=$CID")"
KEY=$(curl -s -X POST "$BASE/keys" -H 'Content-Type: application/json' -d '{"name":"__probe_scope_key__","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
KID=$(curl -s "$BASE/keys" | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='__probe_scope_key__': print(k['id'])
")
call() {
  curl -s -m 20 "$HOST/v1/chat/completions" -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"model":"__probe_scope_model__","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}' \
    | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('error',{}).get('message',''))" 2>/dev/null
}
msg=$(call)
case "$msg" in
  *没有可用的渠道*) chk "分组限定时被路由挡下（没有可用渠道）" "blocked" "blocked" ;;
  *) chk "分组限定时被路由挡下（没有可用渠道）" "blocked" "$msg" ;;
esac
# 对照：分组改成不限，同一个请求就应该真的去连渠道（这里是连不上的 127.0.0.1:9）
P "UPDATE channel_groups SET provider_id=0 WHERE id=$GID" >/dev/null
msg=$(call)
case "$msg" in
  *没有可用的渠道*) chk "改成不限后不再被分组挡下" "已放行到上游" "仍被挡下" ;;
  *)
    # 走到了上游（这里是刻意指向的 127.0.0.1:9，必然连不上），
    # 说明拦截确实来自分组限定，而不是别的原因
    pass=$((pass+1))
    printf "  [通过] 改成不限后不再被分组挡下（上游返回：%s…）\n" "$(echo "$msg" | cut -c1-30)"
    ;;
esac

echo
echo "=== 5. 分组里还有别的模型商的东西时，不允许改归属 ==="
# 此刻组里是 DeepSeek 的渠道与被绑定的 DeepSeek 模型，改成 OpenAI 必须被拒
P "UPDATE channel_groups SET provider_id=2 WHERE id=$GID" >/dev/null
code=$(jsonfield -X PUT "$BASE/groups/$GID" -H 'Content-Type: application/json' -d '{"provider_id":1}')
chk "组内有 DeepSeek 渠道/模型时改成 OpenAI -> 409" "409" "$code"
python3 -c "import json;print('  ',json.load(open('/tmp/gp.json'))['error']['message'][:150])"
chk "冲突时分组归属未被改动" "2" "$(gprov "$GID")"
# 组里没有任何东西时，改归属应当放行
P "DELETE FROM channel_models WHERE channel_id=$CID" >/dev/null
P "UPDATE channels SET provider_id=0 WHERE id=$CID" >/dev/null
code=$(jsonfield -X PUT "$BASE/groups/$GID" -H 'Content-Type: application/json' -d '{"provider_id":1}')
chk "没有冲突时允许改归属" "200" "$code"
chk "分组归属已改成 1" "1" "$(gprov "$GID")"

echo
echo "=== 清理 ==="
purge_probes
echo "  探针数据已删除"

echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" = "0" ] && echo "ALL_PASS" || echo "HAS_FAILURE"
