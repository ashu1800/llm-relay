#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888"

echo "===== 1. 触发定价同步（官方 + LiteLLM）====="
time curl -s -m 400 -X POST "$BASE/api/admin/pricing/sync" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for r in d.get('results',[]):
    print(' 来源:', r['source'], '| 状态:', r['status'],
          '| 新增', r['added'], '| 更新', r['updated'],
          '| 未变', r['unchanged'], '| 跳过人工', r['skipped_manual'])
    if r.get('error'): print('   错误:', r['error'][:200])
"
echo
echo "===== 2. 定价表总量与来源分布 ====="
curl -s "$BASE/api/admin/pricing?page_size=1" | python3 -c "
import sys,json;d=json.load(sys.stdin);print('  定价条目总数:', d['total'])
"
echo
echo "===== 3. DeepSeek 官方源价格（应为谷时基准价）====="
curl -s "$BASE/api/admin/pricing?keyword=deepseek&page_size=12" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print('  匹配 deepseek 的条目:', d['total'])
for it in d['items'][:8]:
    print('   %-34s 输入 %-8s 输出 %-8s 缓存读 %-8s 来源 %-9s 峰时规则 %d 条' % (
        it['model_key'], it['input_per_1m'], it['output_per_1m'],
        it['cache_read_per_1m'], it['source'], len(it.get('peak_rules') or [])))
"
echo
echo "===== 4. 峰谷解析验证（deepseek-v4-pro）====="
for AT in "2026-09-14T02:00:00Z|周一02:00应为峰时" "2026-09-14T12:00:00Z|周一12:00应为谷时" "2026-09-12T02:00:00Z|周六02:00应为谷时"; do
  T="${AT%%|*}"; DESC="${AT##*|}"
  echo "  --- $DESC"
  curl -s -X POST "$BASE/api/admin/pricing/resolve" -H 'Content-Type: application/json' \
    -d "{\"model\":\"deepseek-v4-pro\",\"at\":\"$T\"}" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if not d.get('found'):
    print('     未找到定价')
else:
    s=d['snapshot']
    print('     输入 %s | 输出 %s | 倍率 %s | 峰时 %s | 来源 %s' % (
        s['input_per_1m'], s['output_per_1m'], s['multiplier'], s['peak_applied'], s['source']))
"
done
echo
echo "===== 5. 同步历史 ====="
curl -s "$BASE/api/admin/pricing/history" | python3 -c "
import sys,json
for it in json.load(sys.stdin)['items'][:4]:
    print('  #%s %s %s 新增%s 更新%s 跳过%s' % (it['id'], it['source'], it['status'], it['added'], it['updated'], it['skipped_manual']))
"
echo "DONE"
