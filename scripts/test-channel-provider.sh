#!/usr/bin/env bash
# 渠道模型商的回归验证。
#
# 背景：渠道的模型商曾经只存在于数据库里 —— 建渠道的表单没有这一项，
# 后端又把空值兜底成 1（OpenAI），于是「没选过模型商」的渠道全挂着 OpenAI 徽标，
# 用户看到的和实际填的完全不是一回事。这个脚本锁住修好后的行为：
#   不填 = 未指定(0)，显式传 0 能改回未指定，填了就以填的为准。
set -uo pipefail
BASE=http://127.0.0.1:8888/api/admin
pass=0; fail=0
chk() { # chk 描述 期望 实际
  if [ "$2" = "$3" ]; then pass=$((pass+1)); printf "  [通过] %s\n" "$1"
  else fail=$((fail+1)); printf "  [失败] %s  期望 %s 实际 %s\n" "$1" "$2" "$3"; fi
}
pid() { curl -s "$BASE/channels" | python3 -c "
import sys,json
for c in json.load(sys.stdin)['items']:
    if c['id']==$1: print(c['provider_id'])
"; }
mkch() { # mkch 名称 json额外字段
  curl -s -X POST "$BASE/channels" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\",\"base_url\":\"http://127.0.0.1:9/v1\",\"api_key\":\"sk-probe\"$2}" \
    | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))"
}

echo "=== 1. 不填模型商：应为未指定(0)，不再兜底成 OpenAI ==="
A=$(mkch __probe_no_provider__ "")
chk "新建时不填 -> 0" "0" "$(pid "$A")"

echo
echo "=== 2. 填了就以填的为准 ==="
B=$(mkch __probe_with_provider__ ',"provider_id":2')
chk "新建时填 2 -> 2" "2" "$(pid "$B")"

echo
echo "=== 3. 编辑：显式改回未指定必须生效 ==="
# 这条以前会静默失效：载荷用值类型时「传 0」与「没传」分不开
curl -s -o /dev/null -X PUT "$BASE/channels/$B" -H 'Content-Type: application/json' -d '{"provider_id":0}'
chk "显式传 0 -> 0" "0" "$(pid "$B")"

echo
echo "=== 4. 编辑：不传模型商不得改动它 ==="
curl -s -o /dev/null -X PUT "$BASE/channels/$A" -H 'Content-Type: application/json' -d '{"name":"__probe_no_provider__"}'
chk "只改名字后仍是 0" "0" "$(pid "$A")"
curl -s -o /dev/null -X PUT "$BASE/channels/$A" -H 'Content-Type: application/json' -d '{"provider_id":2}'
chk "改成 2 生效" "2" "$(pid "$A")"

echo
echo "=== 5. 非法模型商应被拒绝且不改动数据 ==="
code=$(curl -s -o /tmp/cp_put.json -w "%{http_code}" -X PUT "$BASE/channels/$A" -H 'Content-Type: application/json' -d '{"provider_id":999}')
chk "编辑传不存在的模型商 -> 400" "400" "$code"
chk "非法请求未改动数据" "2" "$(pid "$A")"
code=$(curl -s -o /tmp/cp_post.json -w "%{http_code}" -X POST "$BASE/channels" -H 'Content-Type: application/json' \
  -d '{"name":"__probe_bad__","base_url":"http://127.0.0.1:9/v1","api_key":"sk-probe","provider_id":999}')
chk "新建传不存在的模型商 -> 400" "400" "$code"
python3 -c "import json;print('  ',json.load(open('/tmp/cp_post.json'))['error']['message'])"

echo
echo "=== 6. 用模板建渠道同样能指定模型商 ==="
TID=$(curl -s "$BASE/channel-templates" | python3 -c "
import sys,json
for t in json.load(sys.stdin)['items']:
    if t['name'].startswith('DeepSeek'): print(t['id']); break
")
C=$(curl -s -X POST "$BASE/channel-templates/$TID/apply" -H 'Content-Type: application/json' \
  -d '{"name":"__probe_tpl_provider__","api_key":"sk-probe","provider_id":2}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['channel']['id'])")
chk "模板建渠道填 2 -> 2" "2" "$(pid "$C")"
D=$(curl -s -X POST "$BASE/channel-templates/$TID/apply" -H 'Content-Type: application/json' \
  -d '{"name":"__probe_tpl_none__","api_key":"sk-probe"}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['channel']['id'])")
chk "模板建渠道不填 -> 0" "0" "$(pid "$D")"

echo
echo "=== 清理 ==="
for id in "$A" "$B" "$C" "$D"; do
  [ -n "$id" ] && curl -s -o /dev/null -X DELETE "$BASE/channels/$id"
done
echo "  临时渠道已删除"

echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" = "0" ] && echo "ALL_PASS" || echo "HAS_FAILURE"
