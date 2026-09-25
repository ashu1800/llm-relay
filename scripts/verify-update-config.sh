#!/usr/bin/env bash
# 验证「版本更新设置」的 token 三态端到端行为。
#
# 这是本轮唯一没法只靠单元测试确认的地方：单元测试证明请求体能被正确解析，
# 但「保存别的设置会不会顺手抹掉 token」取决于整条链路 ——
# 前端发什么、后端怎么合并、数据库里存了什么。
# 假 token 不会被真正使用（不发起 GitHub 请求），所以可以放心写入。
#
# 鉴权走 admin-auth.sh（与其它 test-*.sh 一致）：它自己找 RELAY_ADMIN_KEY
# （环境变量、./deploy/.env、/opt/llm-relay/deploy/.env 三处），
# 登录一次并给下面每次 curl 自动注入会话 Cookie；鉴权关闭时零适配。
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE=http://127.0.0.1:8888

cfg() { curl -sS "$BASE/api/admin/system/update/config"; }
save() { curl -sS -X PUT "$BASE/api/admin/system/update/config" \
           -H 'Content-Type: application/json' -d "$1" -o /dev/null -w '%{http_code}'; }
has_token() { cfg | python3 -c 'import sys,json;print(json.load(sys.stdin)["has_token"])'; }

echo "初始 has_token: $(has_token)"
echo

echo "=== 1. 设置 token（非空串）==="
code=$(save '{"enabled":true,"repo":"ashu1800/llm-relay","proxy":"","token":"ghp_fake_for_test_000"}')
echo "状态码 $code → has_token: $(has_token)"
[ "$(has_token)" = "True" ] || { echo "!! 设置 token 失败"; exit 1; }
echo

echo "=== 2. 只改代理、不带 token 字段（模拟「用户没动输入框」）==="
code=$(save '{"enabled":true,"repo":"ashu1800/llm-relay","proxy":"http://127.0.0.1:7890"}')
echo "状态码 $code → has_token: $(has_token)"
if [ "$(has_token)" = "True" ]; then
  echo "   正确：token 被保留（这是关键 —— 改别的设置不该抹掉它）"
else
  echo "   !! 错误：token 被清掉了，说明「字段缺失」被当成了「清除」"
  exit 1
fi
echo

echo "=== 3. 显式空串清除 token ==="
code=$(save '{"enabled":true,"repo":"ashu1800/llm-relay","proxy":"http://127.0.0.1:7890","token":""}')
echo "状态码 $code → has_token: $(has_token)"
if [ "$(has_token)" = "False" ]; then
  echo "   正确：token 已被清除"
else
  echo "   !! 错误：空串没有清除 token"
  exit 1
fi
echo

echo "=== 4. 复原（清空代理、恢复默认仓库）==="
code=$(save '{"enabled":true,"repo":"ashu1800/llm-relay","proxy":""}')
echo "状态码 $code"
cfg | python3 -m json.tool
echo
# verify-all.sh 以这个标志判定通过（它要求看到明确的成功标志，
# 而不是「没看到失败」——后者在脚本中途崩掉时也会成立）
echo "ALL_PASS"
