#!/usr/bin/env bash
# 模型更新接口的回归验证：切换模型商、部分更新、非法输入
set -uo pipefail
BASE=http://127.0.0.1:8888/api/admin
pass=0; fail=0
chk() { # chk 描述 期望 实际
  if [ "$2" = "$3" ]; then pass=$((pass+1)); printf "  [通过] %s\n" "$1"
  else fail=$((fail+1)); printf "  [失败] %s  期望 %s 实际 %s\n" "$1" "$2" "$3"; fi
}

MID=1
cur() { curl -s "$BASE/models" | python3 -c "import sys,json;print([m for m in json.load(sys.stdin)['items'] if m['id']==$MID][0]['$1'])"; }

echo "=== 1. 切换模型商 1 -> 2 ==="
curl -s -o /tmp/mu.json -X PUT "$BASE/models/$MID" -H 'Content-Type: application/json' \
  -d '{"provider_id":2}'
cat /tmp/mu.json | python3 -c "import sys,json;print('  返回:',json.dumps(json.load(sys.stdin),ensure_ascii=False))"
chk "模型商已变为 2" "2" "$(cur provider_id)"

echo
echo "=== 2. 切回 2 -> 1 ==="
curl -s -o /dev/null -X PUT "$BASE/models/$MID" -H 'Content-Type: application/json' -d '{"provider_id":1}'
chk "模型商已变回 1" "1" "$(cur provider_id)"

echo
echo "=== 3. 只改名称，不得影响 enabled ==="
curl -s -o /dev/null -X PUT "$BASE/models/$MID" -H 'Content-Type: application/json' -d '{"public_name":"deepseek-v4-flash"}'
chk "enabled 仍为 True" "True" "$(cur enabled)"
chk "模型商仍为 1" "1" "$(cur provider_id)"

echo
echo "=== 4. 显式停用与启用应生效 ==="
curl -s -o /dev/null -X PUT "$BASE/models/$MID" -H 'Content-Type: application/json' -d '{"enabled":false}'
chk "可显式停用" "False" "$(cur enabled)"
curl -s -o /dev/null -X PUT "$BASE/models/$MID" -H 'Content-Type: application/json' -d '{"enabled":true}'
chk "可显式启用" "True" "$(cur enabled)"

echo
echo "=== 5. 非法模型商应被拒绝 ==="
code=$(curl -s -o /tmp/me.json -w "%{http_code}" -X PUT "$BASE/models/$MID" -H 'Content-Type: application/json' -d '{"provider_id":999}')
chk "不存在的模型商返回 400" "400" "$code"
python3 -c "import json;print('  ',json.load(open('/tmp/me.json'))['error']['message'])"
chk "非法请求未改动数据" "1" "$(cur provider_id)"

echo
echo "=== 6. 空载荷与不存在的模型 ==="
code=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BASE/models/$MID" -H 'Content-Type: application/json' -d '{}')
chk "空载荷返回 400" "400" "$code"
code=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BASE/models/99999" -H 'Content-Type: application/json' -d '{"provider_id":1}')
chk "不存在的模型返回 404" "404" "$code"

echo
echo "=== 7. 新建模型时指定模型商 ==="
NEW=$(curl -s -X POST "$BASE/models" -H 'Content-Type: application/json' \
  -d '{"public_name":"__probe_model__","provider_id":2,"description":"临时验证"}')
NID=$(echo "$NEW" | python3 -c "import sys,json;print(json.load(sys.stdin).get('id',''))")
chk "新建时可指定模型商 2" "2" "$(curl -s "$BASE/models" | python3 -c "import sys,json;print([m for m in json.load(sys.stdin)['items'] if m['id']==$NID][0]['provider_id'])")"
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/models" -H 'Content-Type: application/json' -d '{"public_name":"__probe_model__"}')
chk "重名返回 409" "409" "$code"
curl -s -o /dev/null -X DELETE "$BASE/models/$NID"
echo "  临时模型已清理"

echo
echo "=== 8. 路由仍正常 ==="
SK=$(curl -s -X POST "$BASE/keys" -H 'Content-Type: application/json' -d '{"name":"model-probe","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
code=$(curl -s -o /dev/null -w "%{http_code}" -m 60 http://127.0.0.1:8888/v1/chat/completions \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}')
chk "改动后仍可正常调用" "200" "$code"
KID=$(curl -s "$BASE/keys" | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='model-probe': print(k['id'])
")
[ -n "$KID" ] && curl -s -o /dev/null -X DELETE "$BASE/keys/$KID"
echo "  测试密钥已清理"

echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" = "0" ] && echo "ALL_PASS" || echo "HAS_FAILURE"
