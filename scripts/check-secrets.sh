#!/usr/bin/env bash
# 全仓凭据泄漏检查。用完整密钥值检索，避免扫描脚本匹配到自己的模式串。
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

# 完整值从环境变量取，脚本本身不留凭据
: "${DS_KEY:?请先 export DS_KEY / OA_KEY / PROXY_PASS}"
: "${OA_KEY:?请先 export DS_KEY / OA_KEY / PROXY_PASS}"
: "${PROXY_PASS:?请先 export DS_KEY / OA_KEY / PROXY_PASS}"

status=0

# 关键：**不要**用 `grep -F "$val"` 检索凭据。
#
# 那会把真实密钥放进 grep 自己的命令行参数，同机任何用户 ps aux 就能读到 ——
# 一个「检查凭据有没有泄漏」的脚本自己成了泄漏点，而运行它的人
# 恰恰是刚怀疑过泄漏、最需要谨慎的时候。
#
# 改为把待查值写进一个 600 权限的临时文件，再用 grep -f 从文件读模式：
# 密钥只出现在文件内容里（属主可读），不进任何进程的 argv。
TMP_PATTERNS="$(mktemp)"
chmod 600 "$TMP_PATTERNS"
# shellcheck disable=SC2064
trap "rm -f '$TMP_PATTERNS'" EXIT INT TERM

echo "=== 历史提交中检索完整凭据值 ==="
for pair in "DeepSeek:$DS_KEY" "OpenAI:$OA_KEY" "代理密码:$PROXY_PASS"; do
  name="${pair%%:*}"; val="${pair#*:}"
  printf '%s\n' "$val" > "$TMP_PATTERNS"
  n=$(git log -p --all 2>/dev/null | grep -cFf "$TMP_PATTERNS" || true)
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
  printf '%s\n' "$val" > "$TMP_PATTERNS"
  hit=$(grep -rlFf "$TMP_PATTERNS" --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=dist . 2>/dev/null || true)
  if [ -z "$hit" ]; then
    echo "  $name -> 无"
  else
    # 只打印命中的**文件名**，绝不打印内容 —— 那等于把密钥又打出来一遍
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
