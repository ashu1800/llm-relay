#!/usr/bin/env bash
cd "/path/to/llm-relay" || exit 1
bash scripts/redeploy.sh || exit 1
echo
echo "=== 触发一次价格同步（回填模型商归属）==="
curl -s -X POST http://127.0.0.1:8888/api/admin/pricing/sync -m 180 -o /tmp/sync.json -w "  HTTP %{http_code}\n"
python3 -c "
import json
d=json.load(open('/tmp/sync.json'))
for r in d.get('results',[]): print('  %-10s 新增 %s 更新 %s 未变 %s' % (r.get('source'), r.get('added'), r.get('updated'), r.get('unchanged')))
" 2>/dev/null || cat /tmp/sync.json | head -c 300
echo
bash scripts/inspect-pricing-provider.sh
