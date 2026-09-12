#!/usr/bin/env bash
# 定价的备份/恢复：倍率与时段规则必须跟着走，旧备份（没有 multiplier 字段）也要能恢复。
#
# 为什么单独测：multiplier 这一列是 not null 且没有默认值，而旧备份反序列化出来是 0；
# 0 会被 GORM 从 INSERT 里省掉，恢复旧备份就会整条写不进去。
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }
MODEL="__backup_probe__"

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}

cleanup() { P "DELETE FROM model_pricings WHERE model_key='$MODEL'" >/dev/null; }
cleanup

echo "=== 1. 建一条带倍率的定价，导出后看备份里的字段 ==="
curl -s -X POST "$API/pricing" -H 'Content-Type: application/json' -d "{
  \"model_key\": \"$MODEL\",
  \"input_per_1m\": \"1.25\",
  \"output_per_1m\": \"2.5\",
  \"multiplier\": 1.5,
  \"peak_rules\": [{\"days\":[1,2,3],\"start\":\"09:00\",\"end\":\"12:00\",\"multiplier\":2,\"label\":\"上午高峰\"}]
}" >/dev/null
curl -s -m 20 "$API/backup/export" -o /tmp/pricing-backup.json
python3 -c "
import json
b = json.load(open('/tmp/pricing-backup.json'))
row = next((p for p in (b.get('pricings') or []) if p['model_key'] == '$MODEL'), None)
print('  备份里的这条:', json.dumps(row, ensure_ascii=False)[:200] if row else '(没有)')
"
chk "备份带上了固定倍率" "1.5" "$(python3 -c "
import json
b = json.load(open('/tmp/pricing-backup.json'))
print(next(p['multiplier'] for p in b['pricings'] if p['model_key'] == '$MODEL'))
")"
chk "备份带上了时段规则" "2" "$(python3 -c "
import json
b = json.load(open('/tmp/pricing-backup.json'))
print(next(p['peak_rules'][0]['multiplier'] for p in b['pricings'] if p['model_key'] == '$MODEL'))
")"

echo
echo "=== 2. 删掉再恢复：倍率与规则都应回来 ==="
cleanup
chk "删除成功" "0" "$(P "SELECT count(*) FROM model_pricings WHERE model_key='$MODEL'")"
curl -s -X POST "$API/backup/import" -H 'Content-Type: application/json' \
  --data-binary @/tmp/pricing-backup.json -o /tmp/pricing-import.json
python3 -c "
import json
d = json.load(open('/tmp/pricing-import.json'))
print('  导入结果:', d['created'], '告警:', d['warnings'] or '(无)')
"
chk "恢复后的固定倍率" "1.5000" "$(P "SELECT multiplier FROM model_pricings WHERE model_key='$MODEL'")"
chk "恢复后的时段规则" "2" "$(P "SELECT peak_rules->0->>'multiplier' FROM model_pricings WHERE model_key='$MODEL'")"
chk "恢复后的单价" "1.25000000" "$(P "SELECT input_per1_m FROM model_pricings WHERE model_key='$MODEL'")"

echo
echo "=== 3. 旧备份（没有 multiplier 字段）也要能恢复 ==="
python3 -c "
import json
b = json.load(open('/tmp/pricing-backup.json'))
for p in b.get('pricings') or []:
    p.pop('multiplier', None)   # 模拟加固定倍率之前导出的备份
json.dump(b, open('/tmp/pricing-backup-old.json','w'), ensure_ascii=False)
"
cleanup
curl -s -X POST "$API/backup/import" -H 'Content-Type: application/json' \
  --data-binary @/tmp/pricing-backup-old.json -o /tmp/pricing-import2.json
python3 -c "
import json
d = json.load(open('/tmp/pricing-import2.json'))
print('  导入结果:', d['created'], '告警:', d['warnings'] or '(无)')
"
chk "旧备份也能恢复（不会因 not null 失败）" "1" "$(P "SELECT count(*) FROM model_pricings WHERE model_key='$MODEL'")"
chk "缺失的倍率按原价兜底" "1.0000" "$(P "SELECT multiplier FROM model_pricings WHERE model_key='$MODEL'")"

echo
echo "=== 4. 清理 ==="
cleanup
chk "探针定价已删除" "0" "$(P "SELECT count(*) FROM model_pricings WHERE model_key='$MODEL'")"
rm -f /tmp/pricing-backup.json /tmp/pricing-backup-old.json /tmp/pricing-import.json /tmp/pricing-import2.json

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
