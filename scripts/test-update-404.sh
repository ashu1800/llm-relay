#!/usr/bin/env bash
# 不存在的 ID 做更新，应当返回 404，而不是谎报「更新成功」
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE=http://127.0.0.1:8888/api/admin
pass=0; fail=0
chk() {
  if [ "$2" = "$3" ]; then pass=$((pass+1)); printf "  [通过] %-28s %s\n" "$1" "$3"
  else fail=$((fail+1)); printf "  [失败] %-28s 期望 %s 实际 %s\n" "$1" "$2" "$3"; fi
}
probe() { # probe 说明 路径 载荷
  local code
  code=$(curl -s -o /tmp/u404.json -w "%{http_code}" -X PUT "$BASE/$2" \
    -H 'Content-Type: application/json' -d "$3")
  chk "$1" "404" "$code"
}

echo "=== 不存在的 ID 更新应返回 404 ==="
probe "渠道"   "channels/999999"  '{"name":"x"}'
probe "分组"   "groups/999999"    '{"name":"x"}'
probe "密钥"   "keys/999999"      '{"name":"x"}'
probe "定价"   "pricing/999999"   '{"match_type":"exact"}'
python3 -c "import json;print('  错误信息示例:', json.load(open('/tmp/u404.json')).get('error',{}).get('message','?'))"

echo
echo "=== 存在的 ID 仍应正常更新 ==="
# 原来这里用 /models，那是「模型商 + 模型目录」时代就有的脚本；
# 那个功能整体下线后，改成用分组做同样的验证（改成原名，幂等、不动真实数据）
read -r GID GNAME <<<"$(curl -s "$BASE/groups" | python3 -c "
import sys,json
g=json.load(sys.stdin)['items'][0]
print(g['id'], g['name'])
")"
code=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BASE/groups/$GID" \
  -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}")
chk "分组正常更新" "200" "$code"

echo
echo "=== 全接口回归 ==="
bash "$(dirname "${BASH_SOURCE[0]}")/test-regression.sh" 2>&1 | tail -8

echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" = "0" ] && echo "ALL_PASS" || echo "HAS_FAILURE"
