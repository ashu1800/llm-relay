#!/usr/bin/env bash
set -uo pipefail
BASE=http://127.0.0.1:8888/api/admin
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

echo "=== 当前 deepseek-flash 各来源行 ==="
P "SELECT id, model_key, source, priority, input_per1_m FROM model_pricings WHERE model_key='deepseek-flash' ORDER BY id"

PID=$(P "SELECT id FROM model_pricings WHERE model_key='deepseek-flash' ORDER BY id LIMIT 1")
BEFORE=$(P "SELECT input_per1_m FROM model_pricings WHERE id=$PID")
SRCB=$(P "SELECT source FROM model_pricings WHERE id=$PID")
echo
echo "=== 手工改价 ==="
echo "  id=$PID  改前 输入价=$BEFORE  来源=$SRCB"
curl -s -o /tmp/pe.json -w "  接口 HTTP %{http_code}\n" -X PUT "$BASE/pricing/$PID" \
  -H 'Content-Type: application/json' -d '{"input_per_1m":"0.19"}'
python3 -c "import json;print('  返回:',json.dumps(json.load(open('/tmp/pe.json')),ensure_ascii=False))" 2>/dev/null
AFTER=$(P "SELECT input_per1_m FROM model_pricings WHERE id=$PID")
SRCA=$(P "SELECT source FROM model_pricings WHERE id=$PID")
PRA=$(P "SELECT priority FROM model_pricings WHERE id=$PID")
echo "  改后 输入价=$AFTER  来源=$SRCA  优先级=$PRA"
if [ "$AFTER" = "0.19000000" ]; then echo "  [通过] 手工改价已写入数据库"; else echo "  [失败] 仍是 $AFTER"; fi
if [ "$SRCA" = "manual" ]; then echo "  [通过] 同时提升为 manual，后续同步不会覆盖"; fi

echo
echo "=== 同步一轮，确认手工价受保护 ==="
curl -s -X POST "$BASE/pricing/sync" -m 300 -o /tmp/s5.json
python3 -c "
import json
d=json.load(open('/tmp/s5.json'))
for r in d.get('results',[]):
    print('  %-9s 更新 %-5s 未变 %-5s 手工保护跳过 %s' % (r.get('source'), r.get('updated'), r.get('unchanged'), r.get('skipped_manual')))
"
AFTER2=$(P "SELECT input_per1_m FROM model_pricings WHERE id=$PID")
echo "  同步后 输入价=$AFTER2 （应仍为 0.19）"
if [ "$AFTER2" = "0.19000000" ]; then echo "  [通过] 手工价未被同步冲掉"; else echo "  [失败] 被冲成了 $AFTER2"; fi
echo DONE
