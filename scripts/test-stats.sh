#!/usr/bin/env bash
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"

echo "===== 1. 聚合接口自检 ====="
for EP in "stats/summary?range=today" "stats/timeseries?range=today" "stats/models?range=7d&limit=5" "stats/channels?range=7d&limit=5" "stats/heatmap?days=30"; do
  CODE=$(curl -s -o /tmp/st.json -w "%{http_code}" "$BASE/api/admin/$EP")
  SIZE=$(wc -c < /tmp/st.json)
  printf "  %-42s HTTP %s  %s 字节\n" "$EP" "$CODE" "$SIZE"
done
echo
echo "===== 2. summary 内容 ====="
curl -s "$BASE/api/admin/stats/summary?range=7d" | python3 -m json.tool | head -22
echo
echo "===== 3. 制造流量（双渠道各若干次）====="
SK=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"dash-seed"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
for i in 1 2 3; do
  curl -s -m 60 -o /dev/null "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
    -d "{\"model\":\"deepseek-v4-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"第 $i 次：说一句话\"}],\"max_tokens\":30}"
  echo "  deepseek 第 $i 次完成"
done
for i in 1 2; do
  curl -s -m 120 -o /dev/null "$BASE/v1/chat/completions" -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
    -d "{\"model\":\"gpt-5.6-sol\",\"messages\":[{\"role\":\"user\",\"content\":\"round $i: say hi\"}],\"max_tokens\":20}"
  echo "  gpt-5.6-sol 第 $i 次完成"
done
sleep 2
echo
echo "===== 4. 聚合结果复查 ====="
curl -s "$BASE/api/admin/stats/summary?range=7d" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print('  请求数 %s | 成功 %s | 失败 %s | 成功率 %.1f%%' % (d['requests'],d['success'],d['errors'],d['success_rate']*100))
print('  Token 输入 %s / 输出 %s / 缓存命中 %s | 命中率 %.1f%%' % (d['prompt_tokens'],d['completion_tokens'],d['cached_tokens'],d['cache_hit_rate']*100))
print('  预估金额 \$%s' % d['estimated_cost'])
print('  平均首包 %.0fms / 平均完成 %.0fms' % (d['avg_first_byte_ms'],d['avg_total_ms']))
"
echo
curl -s "$BASE/api/admin/stats/models?range=7d&limit=5" | python3 -c "
import sys,json
print('  模型分布:')
for it in json.load(sys.stdin)['items']:
    print('    %-22s 请求 %-3s 费用 \$%s' % (it['name'], it['requests'], it['cost']))
"
echo
curl -s "$BASE/api/admin/stats/heatmap?days=30" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print('  热力图: %d 个格子, 时区 %s' % (len(d['items']), d['timezone']))
print('  样例:', d['items'][:3])
"
echo "DONE"
