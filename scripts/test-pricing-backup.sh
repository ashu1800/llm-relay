#!/usr/bin/env bash
# 配置备份在新结构下的行为：价格随渠道模型走，旧备份里的全局定价要被「捞回来」。
#
# 为什么要单独测：价格从 model_pricings 表搬到了 channel_models 上，备份的载体
# 跟着变了。两件容易坏的事必须守住：
#   1) 新备份里不该再出现 pricings 数组（出现了就是两份真相），但白名单条目的
#      价格字段必须在 —— 否则「导出的备份恢复不出价格」，而备份看起来是成功的；
#   2) 老备份（只有全局 pricings）导入时要按模型名把价格应用到匹配的渠道模型上，
#      匹配不到的必须出现在 warnings 里。静默丢掉的话，用户只会发现「某个模型
#      突然按 0 元计费了」。
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { docker exec -i llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}

GNAME="__backup-probe-group"
CNAME="__backup-probe-ch"
M_PRICED="__backup-probe-priced"
M_BLANK="__backup-probe-blank"
M_MISSING="__backup-probe-missing"
EXPORT=/tmp/__backup-export.json
LEGACY=/tmp/__backup-legacy.json
IMPORT=/tmp/__backup-import.json

purge() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do
    curl -s -X DELETE "$API/channels/$cid" >/dev/null
  done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
  P "DELETE FROM request_payloads WHERE log_id IN (SELECT id FROM request_logs WHERE model_requested IN ('$M_PRICED','$M_BLANK'))" >/dev/null
  P "DELETE FROM request_logs WHERE model_requested IN ('$M_PRICED','$M_BLANK')" >/dev/null
}

# 一行白名单的价格，按十进制最短形式输出：numeric(18,8) 读回来的位数不固定。
# 每个列都显式 ::text 再拼接：这四个列的类型理论上都是 numeric，但**本机**
# 的 cache_write_per1_m 曾是 text（历史漂移），直接 COALESCE(col, 0) 会报
# 「COALESCE types text and integer cannot be matched」—— 而这个类型不匹配
# 正是 GET /api/admin/settings 500 与旧备份价格导不进来的同一个根因。
dbrow() {
  P "SELECT COALESCE(input_per1_m::text,'0')||'|'||COALESCE(output_per1_m::text,'0')||'|'||COALESCE(cache_read_per1_m::text,'0')||'|'||COALESCE(cache_write_per1_m::text,'0')||'|'||multiplier::text||'|'||COALESCE(jsonb_array_length(peak_rules),0) FROM channel_models WHERE public_name='$1'" | python3 -c "
import sys
from decimal import Decimal
raw = sys.stdin.read().strip()
if not raw:
    print('NONE'); raise SystemExit
print('|'.join(str(Decimal(x).normalize()) for x in raw.split('|')))
"
}

purge
rm -f "$EXPORT" "$LEGACY" "$IMPORT"

echo "=== 1. 建临时渠道：一条有价（含倍率与时段规则）、一条没价 ==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
CID=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\", \"group_id\": $GID, \"protocol\": \"openai-chat\",
  \"base_url\": \"http://127.0.0.1:1\", \"api_key\": \"probe\", \"weight\": 1,
  \"models\": [
    {\"public_name\": \"$M_PRICED\", \"input_per_1m\": \"1.25\", \"output_per_1m\": \"2.5\",
     \"cache_read_per_1m\": \"0.125\", \"multiplier\": 1.5,
     \"peak_rules\": [{\"days\":[1,2,3],\"start\":\"09:00\",\"end\":\"12:00\",\"multiplier\":2,\"label\":\"上午高峰\"}]},
    {\"public_name\": \"$M_BLANK\"}
  ]}" | jqg "d['id']")
echo "  渠道 id=$CID 分组=$GID"
chk "有价那条的价格" "1.25|2.5|0.125|0|1.5|1" "$(dbrow "$M_PRICED")"
chk "没价那条的默认值" "0|0|0|0|1|0" "$(dbrow "$M_BLANK")"

echo
echo "=== 2. 导出：不该再有 pricings 数组，但白名单条目的价格必须在 ==="
curl -s -m 30 "$API/backup/export" -o "$EXPORT"
python3 - "$CID" "$M_PRICED" "$M_BLANK" <<'PY' >> /tmp/__backup-checks.txt
import json, sys
cid, priced, blank = sys.argv[1], sys.argv[2], sys.argv[3]
b = json.load(open('/tmp/__backup-export.json'))

def emit(desc, exp, act):
    print('%s\t%s\t%s' % (desc, exp, act))

# 旧字段只剩「只读兼容」这一层含义：导出必须是空的（null 或 []），
# 非空就说明代码里还留着一处会写它的路径 —— 那又会变成两份真相
for key in ('pricings', 'manual_pricings'):
    v = b.get(key)
    emit('新备份里的 %s 为空' % key, 'empty', 'empty' if not v else json.dumps(v, ensure_ascii=False)[:120])

rows = b.get('channel_models') or []
mine = {r['public_name']: r for r in rows if r['public_name'] in (priced, blank)}
emit('备份里带上了我的两条白名单', '2', str(len(mine)))
emit('白名单条目都带价格字段', 'yes',
     'yes' if rows and all('input_per_1m' in r and 'multiplier' in r and 'peak_rules' in r for r in rows) else 'no')
p = mine.get(priced, {})
emit('有价那条的四个单价与倍率', '1.25|2.5|0.125|0|1.5',
     '|'.join(str(p.get(k)) for k in ('input_per_1m','output_per_1m','cache_read_per_1m','cache_write_per_1m','multiplier')))
emit('有价那条的时段规则跟着走', '2', str((p.get('peak_rules') or [{}])[0].get('multiplier')))
emit('备份里记了它属于哪条渠道', str(cid), str(p.get('channel_id')))
PY
while IFS=$'\t' read -r desc exp act; do
  [ -z "$desc" ] && continue
  chk "$desc" "$exp" "$act"
done < /tmp/__backup-checks.txt
rm -f /tmp/__backup-checks.txt

echo
echo "=== 3. 构造一份旧格式备份（只有全局 pricings）并导入 ==="
# 三条：一条能匹配到「没配价」的白名单（应当被应用）、一条匹配到「已配价」的
# （不该覆盖用户的现价）、一条根本匹配不到（必须出现在 warnings 里）
cat > "$LEGACY" <<'JSON'
{
  "app": "llm-relay",
  "version": 1,
  "pricings": [
    {"model_key": "__backup-probe-blank",
     "input_per_1m": "3.5", "output_per_1m": "7",
     "cache_read_per_1m": "0.35", "cache_write_per_1m": "1.4",
     "multiplier": 2,
     "peak_rules": [{"days": [], "start": "00:00", "end": "23:59", "multiplier": 3, "label": "旧备份带来的时段"}]},
    {"model_key": "__backup-probe-priced", "input_per_1m": "9", "output_per_1m": "9"},
    {"model_key": "__backup-probe-missing", "input_per_1m": "1"}
  ]
}
JSON
chk "导入返回 200" "200" "$(curl -s -o "$IMPORT" -w '%{http_code}' -m 30 -X POST "$API/backup/import" -H 'Content-Type: application/json' --data-binary @"$LEGACY")"
python3 - <<'PY' >> /tmp/__backup-checks.txt
import json, sys
d = json.load(open('/tmp/__backup-import.json'))
def emit(desc, exp, act):
    print('%s\t%s\t%s' % (desc, exp, act))
created = d.get('created') or {}
warn = d.get('warnings') or []
emit('应用了 1 条旧定价', '1', str(created.get('模型定价', 0)))
# 只带 pricings 的旧备份不该顺带建出渠道/分组：产物越多，越可能碰到用户的数据
emit('没有建出任何渠道', '0', str(created.get('渠道', 0)))
emit('没有建出任何分组', '0', str(created.get('分组', 0)))
emit('匹配不到的定价出现在 warnings 里', 'yes',
     'yes' if any('__backup-probe-missing' in w for w in warn) else 'no')
emit('已有价格的那条被跳过并说明', 'yes',
     'yes' if any('__backup-probe-priced' in w for w in warn) else 'no')
PY
while IFS=$'\t' read -r desc exp act; do
  [ -z "$desc" ] && continue
  chk "$desc" "$exp" "$act"
done < /tmp/__backup-checks.txt
rm -f /tmp/__backup-checks.txt

echo
echo "=== 4. 价格真的落到白名单上了吗 ==="
chk "没价那条被旧备份的价填上（含倍率与时段规则）" "3.5|7|0.35|1.4|2|1" "$(dbrow "$M_BLANK")"
# 已经在渠道里配好价的那条不能被旧表覆盖：旧表是全局一份，用户后来按渠道
# 配的价才更准
chk "已有的价没被旧备份覆盖" "1.25|2.5|0.125|0|1.5|1" "$(dbrow "$M_PRICED")"
# 旧备份里没有 multiplier 字段时，0 的语义是「没配」，由代码归一到 1；
# 这里顺带确认「没这一列也不会因为 not null 写不进去」
cat > "$LEGACY" <<'JSON'
{
  "app": "llm-relay",
  "version": 1,
  "pricings": [
    {"model_key": "__backup-probe-priced", "input_per_1m": "9"}
  ]
}
JSON
curl -s -o /dev/null -m 30 -X POST "$API/backup/import" -H 'Content-Type: application/json' --data-binary @"$LEGACY"
chk "重复导入不会破坏已有价格（幂等）" "1.25|2.5|0.125|0|1.5|1" "$(dbrow "$M_PRICED")"

echo
echo "=== 5. 清理探测数据 ==="
curl -s -X DELETE "$API/channels/$CID" >/dev/null
purge
rm -f "$EXPORT" "$LEGACY" "$IMPORT" /tmp/__backup-checks.txt
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探测白名单已删除" "0" "$(P "SELECT count(*) FROM channel_models WHERE public_name IN ('$M_PRICED','$M_BLANK')")"
# 匹配不到的那条只应该出现在 warnings 里，绝不能凭空建出一条白名单来
chk "匹配不到的定价没有凭空造出白名单" "0" "$(P "SELECT count(*) FROM channel_models WHERE public_name='$M_MISSING'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || { echo "HAS_FAILURE"; exit 1; }
