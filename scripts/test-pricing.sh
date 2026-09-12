#!/usr/bin/env bash
# 定价（全部手工录入）的回归：列表、倍率解析。
# 自动同步已移除，这里不再有「触发同步」的步骤。
set -uo pipefail
BASE="http://127.0.0.1:8888"

echo "===== 1. 定价表总量 ====="
curl -s "$BASE/api/admin/pricing?page_size=1" | python3 -c "
import sys,json;d=json.load(sys.stdin);print('  定价条目总数:', d['total'])
"
echo
echo "===== 2. 含倍率规则的条目 ====="
curl -s "$BASE/api/admin/pricing?page_size=200" | python3 -c "
import sys,json
d=json.load(sys.stdin)
withrules=[it for it in d['items'] if it.get('peak_rules')]
print('  本页 %d 条，其中带倍率规则 %d 条' % (len(d['items']), len(withrules)))
for it in withrules[:8]:
    rules=it['peak_rules']
    desc='、'.join('%s-%s×%s' % (r.get('start'), r.get('end'), r.get('multiplier')) for r in rules[:3])
    print('   %-30s %s' % (it['model_key'], desc))
"
echo
echo "===== 3. 倍率解析 ====="
for AT in "${PRICING_PROBE_MODEL:-deepseek-v4-pro}|2026-09-14T02:00:00Z|周一02:00" "deepseek-v4-pro|2026-09-14T12:00:00Z|周一12:00" "deepseek-v4-pro|2026-09-12T02:00:00Z|周六02:00"; do
  M="${AT%%|*}"; REST="${AT#*|}"; T="${REST%%|*}"; DESC="${REST##*|}"
  echo "  --- $DESC"
  curl -s -X POST "$BASE/api/admin/pricing/resolve" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$M\",\"at\":\"$T\"}" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if not d.get('found'):
    print('     未找到定价（该模型还没录入单价）')
else:
    s=d['snapshot']
    print('     输入 %s | 输出 %s | 倍率 %s | 命中 %s' % (
        s['input_per_1m'], s['output_per_1m'], s['multiplier'], s['peak_applied']))
"
done
echo "DONE"
