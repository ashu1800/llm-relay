#!/usr/bin/env bash
# 全仓凭据泄漏检查。用完整密钥值检索，避免扫描脚本匹配到自己的模式串。
cd "/path/to/llm-relay" || exit 1

# 完整值从环境变量取，脚本本身不留凭据
: "${DS_KEY:?请先 export DS_KEY / OA_KEY / PROXY_PASS}"
: "${OA_KEY:?请先 export DS_KEY / OA_KEY / PROXY_PASS}"
: "${PROXY_PASS:?请先 export DS_KEY / OA_KEY / PROXY_PASS}"

status=0
echo "=== 历史提交中检索完整凭据值 ==="
for pair in "DeepSeek:$DS_KEY" "OpenAI:$OA_KEY" "代理密码:$PROXY_PASS"; do
  name="${pair%%:*}"; val="${pair#*:}"
  n=$(git log -p --all 2>/dev/null | grep -cF "$val" || true)
  if [ "$n" = "0" ]; then
    echo "  $name -> 未出现"
  else
    echo "  $name -> $n 处  <-- 泄漏！"
    status=1
  fi
done

echo
echo "=== 工作区文件中检索完整凭据值 ==="
for pair in "DeepSeek:$DS_KEY" "OpenAI:$OA_KEY" "代理密码:$PROXY_PASS"; do
  name="${pair%%:*}"; val="${pair#*:}"
  hit=$(grep -rlF "$val" --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=dist . 2>/dev/null || true)
  if [ -z "$hit" ]; then
    echo "  $name -> 无"
  else
    echo "  $name -> $hit  <-- 泄漏！"
    status=1
  fi
done

echo
echo "=== 形似密钥的通用模式（可能含误报）==="
grep -rnE 'sk-[a-zA-Z0-9_-]{24,}' --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=dist \
  --exclude=check-secrets.sh . 2>/dev/null | head -10 || echo "  无"

echo
if [ "$status" = "0" ]; then echo "结论：未发现凭据泄漏"; else echo "结论：发现泄漏，需处理"; fi
exit $status
