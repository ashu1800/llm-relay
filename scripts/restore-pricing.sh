#!/usr/bin/env bash
set -uo pipefail
BASE=http://127.0.0.1:8888/api/admin
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

echo "=== 清掉测试留下的手工价，让它回到官方来源 ==="
P "DELETE FROM model_pricings WHERE model_key='deepseek-flash' AND source='manual'" >/dev/null
curl -s -X POST "$BASE/pricing/sync" -m 300 -o /tmp/s6.json
python3 -c "
import json
d=json.load(open('/tmp/s6.json'))
for r in d.get('results',[]):
    print('  %-9s 新增 %-3s 更新 %-5s 未变 %s' % (r.get('source'), r.get('added'), r.get('updated'), r.get('unchanged')))
"
echo
echo "=== 恢复后的 deepseek-flash ==="
P "SELECT id, model_key, source, priority, input_per1_m FROM model_pricings WHERE model_key='deepseek-flash'"
echo
echo "=== 归属分布 ==="
P "SELECT provider_id, count(*) FROM model_pricings GROUP BY provider_id ORDER BY provider_id"
