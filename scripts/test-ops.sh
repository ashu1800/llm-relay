#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888"

echo "=== 路由分析 ==="
curl -s -m 10 "$BASE/api/admin/routing/analysis?range=30d" -o /tmp/ra.json -w "HTTP %{http_code}\n"
python3 - <<'PY'
import json
d = json.load(open('/tmp/ra.json'))
s = d['summary']
print('  汇总: 请求 %s 失败 %s 重试率 %s 平均 %sms 首字节 %sms 命中率 %s' % (
    s['requests'], s['errors'], s['retry_rate'], s['avg_ms'], s['avg_first_byte_ms'], s['cache_hit_rate']))
print('  渠道:')
for c in d['channels']:
    print('    %-20s 权重%-3s 期望%-7s 实际%-7s 偏差%-8s 请求%-4s 成功率%-7s 平均%sms %s' % (
        c['channel_name'], c['weight'], c['expected_share'], c['actual_share'], c['deviation'],
        c['requests'], c['success_rate'], c['avg_ms'], '[无流量]' if c['idle'] else ''))
print('  模型分布:')
for m in d['models']:
    detail = ', '.join('%s(%s)' % (x['channel_name'], x['requests']) for x in m['channels'])
    print('    %-22s %s 次 -> %s' % (m['model'], m['requests'], detail))
print('  异常记录: %d 条' % len(d['incidents']))
for i in d['incidents'][:3]:
    print('    %s %s %s 状态%s 重试%s %sms' % (
        i['created_at'], i['model'], i['channel_name'], i['status_code'], i['retry_count'], i['total_ms']))
PY

echo
echo "=== 系统设置 ==="
curl -s -m 10 "$BASE/api/admin/settings" -o /tmp/st.json -w "HTTP %{http_code}\n"
python3 - <<'PY'
import json
d = json.load(open('/tmp/st.json'))
r = d['runtime']
for k in ['listen','mode','log_level','max_retries','max_request_body_mb',
          'log_retention_days','payload_storage_mode','pricing_interval_hours',
          'database_ok','redis_enabled']:
    print('  %-24s %s' % (k, r.get(k)))
print('  计数:', json.dumps(d['counts'], ensure_ascii=False))
print('  跨度:', json.dumps(d['log_span'], ensure_ascii=False))
PY

echo
echo "=== 数据清理（保留期内应删 0 条）==="
curl -s -m 30 -X POST "$BASE/api/admin/settings/cleanup" -H 'Content-Type: application/json' -d '{}'
echo
echo "=== 其他统计接口回归 ==="
for ep in "stats/summary?range=30d" "stats/timeseries?range=30d" "stats/models?range=30d" "stats/channels?range=30d" "stats/heatmap?range=30d" "pricing?page_size=2" "logs?page_size=2" "channels" "models" "keys"; do
  code=$(curl -s -o /dev/null -w "%{http_code}" -m 10 "$BASE/api/admin/$ep")
  printf "  %-32s %s\n" "$ep" "$code"
done
echo "DONE"
