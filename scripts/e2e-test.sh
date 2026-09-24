#!/usr/bin/env bash
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
# 上游凭据与地址全部由环境变量注入，避免把密钥写进仓库
DS_KEY="${DS_KEY:-}"
DS_BASE="${DS_BASE:-}"
OA_KEY="${OA_KEY:-}"
OA_BASE="${OA_BASE:-}"
if [[ -z "$DS_KEY" || -z "$OA_KEY" || -z "$DS_BASE" || -z "$OA_BASE" ]]; then
  cat <<'TIP'
请先设置环境变量，例如：
  export DS_KEY=sk-xxx  DS_BASE=https://your-deepseek-mirror
  export OA_KEY=sk-yyy  OA_BASE=https://your-openai-mirror/v1
再执行本脚本。
TIP
  exit 1
fi

jqget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }

# 用 --data-binary @- 从标准输入喂 JSON，而不是 -d "{...}"。
#
# 后者会把整个请求体（含上游 api_key 明文）放进 curl 的命令行参数里 ——
# 同机任何用户 ps aux 就能读到。这些是**真实**的上游密钥，
# 而脚本运行的场景（排障、验收）往往正发生在共享主机上。
# 走标准输入不留任何可读痕迹。
post_json() {
  curl -s -X POST "$BASE$1" -H 'Content-Type: application/json' --data-binary @-
}

echo "===== 1. 创建 DeepSeek 渠道 ====="
CH1=$(post_json /api/admin/channels <<JSON
{
  "name": "ohub-deepseek",
  "protocol": "openai-chat",
  "base_url": "$DS_BASE",
  "api_key": "$DS_KEY",
  "weight": 1,
  "enabled": true
}
JSON
)
echo "$CH1" | head -c 400; echo
ID1=$(echo "$CH1" | jqget "['id']")

echo
echo "===== 2. 创建 OpenAI 渠道 ====="
CH2=$(post_json /api/admin/channels <<JSON
{
  "name": "fast-openai",
  "protocol": "openai-chat",
  "base_url": "$OA_BASE",
  "api_key": "$OA_KEY",
  "weight": 1,
  "enabled": true
}
JSON
)
echo "$CH2" | head -c 400; echo
ID2=$(echo "$CH2" | jqget "['id']")

echo
echo "===== 3. 绑定模型 ====="
curl -s -X POST "$BASE/api/admin/channels/$ID1/models" -H 'Content-Type: application/json' \
  -d '{"public_name":"deepseek-v4-flash","upstream_name":"deepseek-v4-flash"}'; echo
curl -s -X POST "$BASE/api/admin/channels/$ID2/models" -H 'Content-Type: application/json' \
  -d '{"public_name":"gpt-5.6-sol","upstream_name":"gpt-5.6-sol"}'; echo

echo
echo "===== 4. 创建对外密钥 ====="
KEYRES=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"local-test"}')
SK=$(echo "$KEYRES" | jqget "['key']")
echo "对外密钥: $SK"

echo
echo "===== 5. /v1/models ====="
curl -s "$BASE/v1/models" -H "Authorization: Bearer $SK" | head -c 500; echo

echo
echo "===== 6. 非流式对话（DeepSeek）====="
curl -s -m 90 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"只回复:OK"}],"max_tokens":20}' | head -c 900
echo

echo
echo "===== 7. 流式对话（DeepSeek，看 usage）====="
curl -s -m 90 -N "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"数到3"}],"max_tokens":30,"stream":true}' | tail -5
echo

echo
echo "===== 8. 非流式对话（OpenAI gpt-5.6-sol）====="
curl -s -m 90 "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"只回复:OK"}],"max_tokens":20}' | head -c 700
echo

echo
echo "===== 9. 无密钥应 401 ====="
curl -s -o /dev/null -w "no-auth=%{http_code}\n" "$BASE/v1/chat/completions" -H 'Content-Type: application/json' -d '{"model":"x","messages":[]}'

echo
echo "===== 10. 未知模型应报错 ====="
curl -s -m 30 -o /tmp/unk.json -w "unknown-model=%{http_code}\n" "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"not-exist-model","messages":[{"role":"user","content":"hi"}]}'
head -c 300 /tmp/unk.json; echo

echo
echo "===== 11. 请求日志（含计量）====="
sleep 2
curl -s "$BASE/api/admin/logs?page_size=5" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print('总计', d['total'], '条')
for it in d['items']:
    print('---')
    print(' 模型:', it['model_requested'], '-> 上游', it.get('model_upstream'))
    print(' 渠道:', it.get('channel_name'), '| 状态:', it['status_code'], '| 流式:', it['stream'])
    print(' tokens 输入/输出/缓存命中/缓存写入:', it['prompt_tokens'], '/', it['completion_tokens'], '/', it['cached_tokens'], '/', it['cache_creation_tokens'])
    print(' 推理tokens:', it['reasoning_tokens'], '| 首包ms:', it['first_byte_ms'], '| 总ms:', it['total_ms'])
"
echo
echo "E2E_DONE"
