#!/usr/bin/env bash
# 升级路径回归：老库（表里已有数据、还没有某个 not null 列）能不能自动补列并起来。
#
# 为什么单独测这一条：GORM AutoMigrate 给已有数据的表加 not null 且无 default 的列时，
# 生成的是 ALTER TABLE ... ADD COLUMN m numeric(10,4) NOT NULL，Postgres 直接报
# "column of relation contains null values"，AutoMigrate 失败 → 应用起不来 →
# 容器反复重启（实测在 3552 行定价上就是这样）。修法是让迁移代码在 AutoMigrate
# 之前自己建列（带 DEFAULT 再 DROP DEFAULT），这个脚本就是那条路径的守门人。
#
# 注意：它会删列并重启应用（约 10 秒不可用），跑之前确认没有正在进行的调用。
set -uo pipefail
BASE="http://127.0.0.1:8888"
P() { docker exec -i llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

ok=0; bad=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}

echo "=== 1. 退回旧结构：删掉 multiplier 列，并留一行数据 ==="
P "DELETE FROM model_pricings WHERE model_key='__upgrade_probe__'" >/dev/null
P "ALTER TABLE model_pricings DROP COLUMN IF EXISTS multiplier" >/dev/null
P "INSERT INTO model_pricings (model_key, active, created_at, updated_at) VALUES ('__upgrade_probe__', true, now(), now())" >/dev/null
chk "列已删掉" "0" "$(P "SELECT count(*) FROM information_schema.columns WHERE table_name='model_pricings' AND column_name='multiplier'")"
chk "表里有数据（升级场景）" "1" "$(P "SELECT count(*) FROM model_pricings WHERE model_key='__upgrade_probe__'")"

echo
echo "=== 2. 重启应用 ==="
docker restart llm-relay >/dev/null
HEALTH=""
for i in $(seq 1 30); do
  sleep 2
  HEALTH=$(docker inspect -f '{{.State.Health.Status}}' llm-relay 2>/dev/null || echo none)
  [ "$HEALTH" = "healthy" ] && break
done
chk "应用恢复健康（不是重启循环）" "healthy" "$HEALTH"
chk "没有反复重启" "0" "$(docker inspect -f '{{.RestartCount}}' llm-relay)"

echo
echo "=== 3. 列被自动补上，且既有行按原价（1 倍）兜底 ==="
chk "列已补回" "1" "$(P "SELECT count(*) FROM information_schema.columns WHERE table_name='model_pricings' AND column_name='multiplier'")"
chk "列是 not null" "NO" "$(P "SELECT is_nullable FROM information_schema.columns WHERE table_name='model_pricings' AND column_name='multiplier'")"
# 默认值必须被去掉：留着它会让「忘记填倍率」被列默认值静默补上
chk "没有留下列默认值" "" "$(P "SELECT COALESCE(column_default,'') FROM information_schema.columns WHERE table_name='model_pricings' AND column_name='multiplier'")"
chk "既有行的倍率是 1" "1.0000" "$(P "SELECT multiplier FROM model_pricings WHERE model_key='__upgrade_probe__'")"

echo
echo "=== 4. 接口可用 ==="
chk "定价接口 200" "200" "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/admin/pricing")"

echo
echo "=== 5. 清理探针数据 ==="
P "DELETE FROM model_pricings WHERE model_key='__upgrade_probe__'" >/dev/null
chk "探针行已删除" "0" "$(P "SELECT count(*) FROM model_pricings WHERE model_key='__upgrade_probe__'")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
