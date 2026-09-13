#!/usr/bin/env bash
# 升级路径回归：老库（表里已有数据、缺了某个价格列）能不能自动补列并正常起来。
#
# 为什么单独测这一条：GORM AutoMigrate 给已有数据的表加「not null 且无 default」的列时，
# 生成的是 ALTER TABLE ... ADD COLUMN multiplier numeric(10,4) NOT NULL，
# Postgres 直接报 "column of relation contains null values"，AutoMigrate 失败 →
# 应用起不来 → 容器反复重启（实测就是在几千行定价上这样挂的）。修法是让迁移代码在
# AutoMigrate 之前自己建列（带 DEFAULT 再 DROP DEFAULT），这个脚本就是那条路径的守门人。
#
# 新结构下价格搬到了 channel_models 上（input_per1_m / output_per1_m /
# cache_read_per1_m / cache_write_per1_m / multiplier），所以探针从 model_pricings
# 换成了渠道白名单：价格由接口写进白名单，行还是「老库里有数据」的行。
#
# 顺带修一处**实测到的类型漂移**：本机的 cache_write_per1_m 是 text，而代码里
# 所有 SQL 都按 numeric 用它（系统设置的计数、旧备份导入的 WHERE、备份脚本的读取），
# 结果是 GET /api/admin/settings 直接 500、老备份里的价格一条都应用不上。
# 迁移用的是 ADD COLUMN IF NOT EXISTS —— 对「列在、类型错」无能为力，
# 所以脚本把类型不对的价格列一并删掉重建：跑完这一条，漂移就被修好了。
#
# 注意：它会删列并重新部署（install.sh，几分钟不可用），跑之前确认没有正在进行的调用。
# 删列前会把这几列的既有值存进 __probe_col_backup，跑完按原值还原 ——
# 用户的渠道价格不该为了一次回归测试而丢。
set -uo pipefail
BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { docker exec -i llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}

GNAME="__col-probe-group"
CNAME="__col-probe-ch"
MODEL="__col-probe-model"
BACKUP_TABLE="__probe_col_backup"

purge() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do
    curl -s -X DELETE "$API/channels/$cid" >/dev/null
  done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
  P "DELETE FROM request_logs WHERE model_requested='$MODEL'" >/dev/null
}

# 白名单里的价格：::text 会按列的小数位补齐（1 变成 1.00000000），
# 所以先归一化成最短十进制形式再比
prow() {
  P "SELECT input_per1_m::text||'|'||output_per1_m::text||'|'||cache_read_per1_m::text||'|'||cache_write_per1_m::text||'|'||multiplier::text FROM channel_models WHERE public_name='$1'" | python3 -c "
import sys
from decimal import Decimal
raw = sys.stdin.read().strip()
print('NONE' if not raw else '|'.join(str(Decimal(x).normalize()) for x in raw.split('|')))
"
}

purge

echo "=== 1. 建探针渠道：白名单里带一条配了价的模型（库里就有数据了）==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
CID=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\", \"group_id\": $GID, \"protocol\": \"openai-chat\",
  \"base_url\": \"http://127.0.0.1:1\", \"api_key\": \"probe\", \"weight\": 1,
  \"models\": [{\"public_name\": \"$MODEL\", \"input_per_1m\": \"1\", \"output_per_1m\": \"2\", \"multiplier\": 1.5}]
}" | jqg "d['id']")
ROWS_BEFORE=$(P "SELECT count(*) FROM channel_models")
echo "  渠道 id=$CID 分组=$GID 白名单行数=$ROWS_BEFORE"
chk "探针行已建好且带价" "1|2|0|0|1.5" "$(prow "$MODEL")"
chk "老库里有数据（升级场景的前提）" "yes" "$( [ "$ROWS_BEFORE" -gt 0 ] && echo yes || echo no )"

echo
echo "=== 2. 退回旧结构：删掉价格列与倍率列，并先把既有值备份下来 ==="
# 要删的列：input_per1_m（常规补列）、multiplier（not null 且无默认值 —— 历史上
# 就是它把 AutoMigrate 卡死的），外加任何**类型不是 numeric** 的价格列（漂移修复）
DRIFT=$(P "SELECT COALESCE(string_agg(column_name, ' '),'') FROM information_schema.columns WHERE table_name='channel_models' AND column_name IN ('input_per1_m','output_per1_m','cache_read_per1_m','cache_write_per1_m') AND data_type <> 'numeric'")
[ -n "$DRIFT" ] && echo "  检测到类型漂移的价格列: $DRIFT"
DROPCOLS=""
for col in input_per1_m multiplier $DRIFT; do
  case " $DROPCOLS " in *" $col "*) continue ;; esac
  DROPCOLS="$DROPCOLS $col"
done
DROPCOLS=$(echo $DROPCOLS)
echo "  本次会删掉并等迁移补回: $DROPCOLS"
P "DROP TABLE IF EXISTS $BACKUP_TABLE" >/dev/null
P "CREATE TABLE $BACKUP_TABLE (row_id bigint, col text, val text)" >/dev/null
for col in $DROPCOLS; do
  P "INSERT INTO $BACKUP_TABLE (row_id, col, val) SELECT id, '$col', COALESCE($col::text,'') FROM channel_models" >/dev/null
done
chk "既有值已备份（行数一致）" "$ROWS_BEFORE" "$(P "SELECT count(*) FROM $BACKUP_TABLE WHERE col='input_per1_m'")"
for col in $DROPCOLS; do
  P "ALTER TABLE channel_models DROP COLUMN IF EXISTS $col" >/dev/null
done
chk "列已删掉" "0" "$(P "SELECT count(*) FROM information_schema.columns WHERE table_name='channel_models' AND column_name='input_per1_m'")"
chk "倍率列已删掉" "0" "$(P "SELECT count(*) FROM information_schema.columns WHERE table_name='channel_models' AND column_name='multiplier'")"
chk "表里还有数据（正是 AutoMigrate 会翻车的场景）" "$ROWS_BEFORE" "$(P "SELECT count(*) FROM channel_models")"

echo
echo "=== 3. 重新部署（install.sh：同步源码 + 重建镜像 + 重启）==="
echo "  这一步要几分钟，期间服务不可用"
bash "$ROOT/deploy/install.sh" > /tmp/__col-deploy.log 2>&1
DEPLOY_RC=$?
chk "install.sh 退出码 0（构建/启动没报错）" "0" "$DEPLOY_RC"
HEALTH=""
for i in $(seq 1 45); do
  sleep 2
  HEALTH=$(docker inspect -f '{{.State.Health.Status}}' llm-relay 2>/dev/null || echo none)
  [ "$HEALTH" = "healthy" ] && break
done
if [ "$HEALTH" != "healthy" ]; then
  echo "  部署日志末尾（排查用）:"
  tail -20 /tmp/__col-deploy.log | sed 's/^/    /'
fi
chk "应用恢复健康（不是重启循环）" "healthy" "$HEALTH"
# 部署时 compose 会连 postgres 一起重建，应用偶尔比库先起来、连不上而重启一次 ——
# 那不是重启循环。判据取「重启次数不再增长」：真的在崩溃循环里时它会一直涨。
RC1=$(docker inspect -f '{{.RestartCount}}' llm-relay)
sleep 6
RC2=$(docker inspect -f '{{.RestartCount}}' llm-relay)
chk "6 秒内没有再次重启（部署期最多容忍 1 次）" "$RC1" "$RC2"
chk "重启次数不超过 1" "yes" "$( [ "$RC2" -le 1 ] && echo yes || echo no )"

echo
echo "=== 4. 列被自动补上：numeric(18,8)、not null、默认值 0 ==="
for col in input_per1_m output_per1_m cache_read_per1_m cache_write_per1_m; do
  chk "$col 已补回" "1" "$(P "SELECT count(*) FROM information_schema.columns WHERE table_name='channel_models' AND column_name='$col'")"
  chk "$col 的类型是 numeric(18,8)" "numeric|18|8" "$(P "SELECT data_type||'|'||COALESCE(numeric_precision::text,'-')||'|'||COALESCE(numeric_scale::text,'-') FROM information_schema.columns WHERE table_name='channel_models' AND column_name='$col'")"
done
# 四个单价列必须都是 numeric。这不是形式主义：cache_write_per1_m 的 gorm tag 曾把
# type: 写成 type=，GORM 解析不到类型就退回按 Go 类型推断 —— decimal.Decimal 的
# Value() 返回 string，于是列被建成 text。后果是 GET /api/admin/settings 直接 500
# （COALESCE(text, 0) 类型不匹配）、旧备份里的全局定价一条都应用不上。
chk "四个单价列都是 numeric 类型" "4" "$(P "SELECT count(*) FROM information_schema.columns WHERE table_name='channel_models' AND column_name IN ('input_per1_m','output_per1_m','cache_read_per1_m','cache_write_per1_m') AND data_type='numeric'")"
# 被删掉的单价列补回时要带默认值 0；倍率列正相反，默认值必须被撤掉（见下）。
#
# 这里刻意**不**断言单价列是 not null：迁移建列时确实写了 NOT NULL DEFAULT 0，
# 但紧接着的 AutoMigrate 会按实体把它撤掉（实体上是 decimal.Decimal，
# tag 里没有 not null），实测补回来的列是 nullable。nullability 撤不撤不影响
# 「老行按 0 兜底」，所以断言落在真正有意义的类型与默认值上。
for col in $DROPCOLS; do
  case "$col" in
    multiplier) ;; # 倍率列的默认值单独断言
    *) chk "$col 补回后默认值仍是 0" "0" "$(P "SELECT COALESCE(column_default,'(无)') FROM information_schema.columns WHERE table_name='channel_models' AND column_name='$col'")" ;;
  esac
done
chk "倍率列已补回" "1" "$(P "SELECT count(*) FROM information_schema.columns WHERE table_name='channel_models' AND column_name='multiplier'")"
chk "倍率列是 not null" "NO" "$(P "SELECT is_nullable FROM information_schema.columns WHERE table_name='channel_models' AND column_name='multiplier'")"
# 默认值必须是 0 且**要留着**：0 就是这一列想要的语义（没配倍率，引擎只在
# multiplier > 0 且 != 1 时才用它）。反过来，NOT NULL 且无默认要求每一条原生
# INSERT 都显式给值，漏写就直接违反约束 —— 实测踩过：级联删除的用例插白名单时
# 没写 multiplier，插入静默失败，断言却报「删除前绑定数 期望 1 实际 0」。
chk "倍率列保留 0 作为默认值" "0" "$(P "SELECT COALESCE(column_default,'') FROM information_schema.columns WHERE table_name='channel_models' AND column_name='multiplier'")"
chk "既有行的单价按 0 兜底（不是 null）" "$ROWS_BEFORE" "$(P "SELECT count(*) FROM channel_models WHERE input_per1_m = 0 AND cache_write_per1_m::text::numeric = 0")"
chk "既有行的倍率按 0 兜底（由代码归一到 1）" "$ROWS_BEFORE" "$(P "SELECT count(*) FROM channel_models WHERE multiplier = 0")"

echo
echo "=== 5. 接口可用（类型漂移修好后系统设置页不再 500）==="
chk "渠道接口 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' "$API/channels")"
chk "系统设置接口 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' "$API/settings")"
chk "settings.counts 里有 priced" "yes" "$(curl -s "$API/settings" | jqg "'yes' if 'priced' in (d.get('counts') or {}) else 'no'")"

echo
echo "=== 6. 把删列前的既有值按原样还原（测试不该动用户的价格）==="
for col in $DROPCOLS; do
  P "UPDATE channel_models cm SET $col = COALESCE(NULLIF(btrim(b.val),''),'0')::numeric FROM $BACKUP_TABLE b WHERE b.row_id = cm.id AND b.col = '$col'" >/dev/null
done
chk "没有丢掉任何一行" "$ROWS_BEFORE" "$(P "SELECT count(*) FROM channel_models")"
MISMATCH=0
for col in $DROPCOLS; do
  n=$(P "SELECT count(*) FROM channel_models cm JOIN $BACKUP_TABLE b ON b.row_id = cm.id AND b.col = '$col' WHERE cm.$col::text::numeric IS DISTINCT FROM COALESCE(NULLIF(btrim(b.val),''),'0')::numeric")
  MISMATCH=$((MISMATCH + n))
done
chk "每一行的原值都已还原（无差异）" "0" "$MISMATCH"
chk "探针行的价格也还原了" "1|2|0|0|1.5" "$(prow "$MODEL")"

echo
echo "=== 7. 清理探测数据 ==="
curl -s -X DELETE "$API/channels/$CID" >/dev/null
purge
P "DROP TABLE IF EXISTS $BACKUP_TABLE" >/dev/null
rm -f /tmp/__col-deploy.log
chk "探针渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探针分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
chk "探针白名单已删除" "0" "$(P "SELECT count(*) FROM channel_models WHERE public_name='$MODEL'")"
chk "临时备份表已删除" "0" "$(P "SELECT count(*) FROM information_schema.tables WHERE table_name='$BACKUP_TABLE'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || { echo "HAS_FAILURE"; exit 1; }
