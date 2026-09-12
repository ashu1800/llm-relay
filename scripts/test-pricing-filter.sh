#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888"

echo "=== 默认排序（priority DESC）前 6 条 ==="
curl -s "$BASE/api/admin/pricing?page_size=6" -o /tmp/p1.json
python3 - <<'PY'
import json
d=json.load(open('/tmp/p1.json'))
print('  总数', d['total'])
for it in d['items']:
    print('  %-26s 来源 %-9s 优先级 %-4s 输入 %-8s 峰时 %d 段' % (
        it['model_key'], it['source'], it['priority'], it['input_per_1m'],
        len(it.get('peak_rules') or [])))
PY

echo
echo "=== 仅看已绑定模型 ==="
curl -s "$BASE/api/admin/pricing?bound_only=true" -o /tmp/p2.json
python3 - <<'PY'
import json
d=json.load(open('/tmp/p2.json'))
print('  命中', d['total'], '条')
for it in d['items']:
    print('  %-24s 来源 %-9s 输入 %-8s 输出 %-8s 缓存读 %s' % (
        it['model_key'], it['source'], it['input_per_1m'],
        it['output_per_1m'], it['cache_read_per_1m']))
PY

echo
echo "=== 关键词 + 来源组合过滤 ==="
curl -s "$BASE/api/admin/pricing?keyword=deepseek&source=official" -o /tmp/p3.json
python3 - <<'PY'
import json
d=json.load(open('/tmp/p3.json'))
print('  deepseek + official:', d['total'], '条')
for it in d['items']:
    print('  %-22s 输入 %-7s 输出 %-7s 缓存读 %s' % (
        it['model_key'], it['input_per_1m'], it['output_per_1m'], it['cache_read_per_1m']))
PY
echo "DONE"
