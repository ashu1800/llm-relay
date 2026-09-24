#!/usr/bin/env bash
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"

# 路由分析那一节随功能下线一起删除（接口已不存在，留着只会打印一堆 404）

echo
echo "=== 系统设置 ==="
curl -s -m 10 "$BASE/api/admin/settings" -o /tmp/st.json -w "HTTP %{http_code}\n"
python3 - <<'PY'
import json
d = json.load(open('/tmp/st.json'))
r = d['runtime']
for k in ['listen','mode','log_level','max_retries','max_request_body_mb',
          'log_retention_days','payload_storage_mode','database_ok']:
    print('  %-24s %s' % (k, r.get(k)))
print('  计数:', json.dumps(d['counts'], ensure_ascii=False))
print('  跨度:', json.dumps(d['log_span'], ensure_ascii=False))
PY

echo
echo "=== 数据清理（保留期内应删 0 条）==="
curl -s -m 30 -X POST "$BASE/api/admin/settings/cleanup" -H 'Content-Type: application/json' -d '{}'
echo
echo "=== 其他统计接口回归 ==="
for ep in "stats/summary?range=30d" "stats/timeseries?range=30d" "stats/models?range=30d" "stats/channels?range=30d" "stats/heatmap?range=30d" "pricing?page_size=2" "logs?page_size=2" "channels" "keys"; do
  code=$(curl -s -o /dev/null -w "%{http_code}" -m 10 "$BASE/api/admin/$ep")
  printf "  %-32s %s\n" "$ep" "$code"
done
echo "DONE"
