#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888"
echo "=== 路由分析（修正 GORM 列映射后）==="
curl -s -m 10 "$BASE/api/admin/routing/analysis?range=30d" -o /tmp/ra.json -w "HTTP %{http_code}\n"
python3 - <<'PY'
import json
d = json.load(open('/tmp/ra.json'))
s = d['summary']
print('  汇总: 请求 %s 失败 %s 重试率 %s 平均 %sms 首字节 %sms 命中率 %s 费用 %s' % (
    s['requests'], s['errors'], s['retry_rate'], s['avg_ms'],
    s['avg_first_byte_ms'], s['cache_hit_rate'], s['cost']))
print('  渠道:')
for c in d['channels']:
    print('    %-16s 权重%-2s 期望%-7s 实际%-7s 偏差%-8s 请求%-3s 成功率%-7s 平均%-9s 首字节%-8s 命中%-7s 费用%s' % (
        c['channel_name'], c['weight'], c['expected_share'], c['actual_share'], c['deviation'],
        c['requests'], c['success_rate'], c['avg_ms'], c['avg_first_byte_ms'],
        c['cache_hit_rate'], c['cost']))
PY
echo
echo "=== 与看板汇总做交叉校验 ==="
curl -s -m 10 "$BASE/api/admin/stats/summary?range=30d" -o /tmp/sm.json
python3 - <<'PY'
import json
a = json.load(open('/tmp/ra.json'))['summary']
b = json.load(open('/tmp/sm.json'))
print('  路由分析: 请求 %s 失败 %s 平均 %sms 首字节 %sms 命中率 %s' % (
    a['requests'], a['errors'], a['avg_ms'], a['avg_first_byte_ms'], a['cache_hit_rate']))
print('  看板汇总: 请求 %s 失败 %s 平均 %sms 首字节 %sms 命中率 %s' % (
    b.get('requests'), b.get('errors'), b.get('avg_total_ms'), b.get('avg_first_byte_ms'), b.get('cache_hit_rate')))
ok = str(a['requests']) == str(b.get('requests')) and a['cache_hit_rate'] == str(b.get('cache_hit_rate'))
print('  一致:', '是' if ok else '否')
PY
echo "DONE"
