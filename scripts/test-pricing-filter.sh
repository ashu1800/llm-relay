#!/usr/bin/env bash
# 定价列表的过滤行为。
# 定价全部是手工录入的，没有「来源」这个维度了，这里只验证关键词与
# 「仅看渠道白名单里的模型」两个过滤条件，以及分页总数是否一致。
set -uo pipefail
BASE="http://127.0.0.1:8888"

echo "=== 默认列表前 6 条 ==="
curl -s "$BASE/api/admin/pricing?page_size=6" -o /tmp/p1.json
python3 - <<'PY'
import json
d=json.load(open('/tmp/p1.json'))
print('  总数', d['total'])
for it in d['items']:
    print('  %-26s 输入 %-8s 输出 %-8s 倍率段 %d' % (
        it['model_key'], it['input_per_1m'], it['output_per_1m'],
        len(it.get('peak_rules') or [])))
PY

echo
echo "=== 仅看渠道白名单里的模型 ==="
curl -s "$BASE/api/admin/pricing?bound_only=true" -o /tmp/p2.json
python3 - <<'PY'
import json
d=json.load(open('/tmp/p2.json'))
print('  命中', d['total'], '条')
for it in d['items']:
    print('  %-24s 输入 %-8s 输出 %-8s' % (
        it['model_key'], it['input_per_1m'], it['output_per_1m']))
PY

echo
echo "=== 关键词过滤 ==="
curl -s "$BASE/api/admin/pricing?keyword=deepseek" -o /tmp/p3.json
python3 - <<'PY'
import json
d=json.load(open('/tmp/p3.json'))
print('  deepseek:', d['total'], '条')
bad=[it['model_key'] for it in d['items'] if 'deepseek' not in it['model_key'].lower()]
print('  不含关键词的条目:', bad if bad else '(无，过滤正确)')
PY
echo "DONE"
