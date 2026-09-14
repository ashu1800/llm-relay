#!/usr/bin/env bash
# 默认分组被删掉之后，不能被启动时的「种子」再造回来。
#
# 背景：store.Seed 原来按**名字**做 FirstOrCreate("默认分组")，于是用户在界面上
# 把这个分组删掉之后，下次重启又会冒出一个同名的、并被设为默认 ——
# 现象就是「我明明删掉了，重启它又回来了」。
#
# 这个用例不碰线上库：单独建一个空库、用同一个镜像再起一个容器，走真实启动路径
# （Seed 在启动时执行），于是「删掉的分组在重启后有没有回来」可以被真实验证。
# 用完删容器、删库。
#
# 依赖：llm-relay:local 镜像（先跑 scripts/redeploy.sh）与运行中的 llm-relay
# 容器 —— 数据库账号密码直接从它的环境变量里取，不去读 deploy/.env。
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

TESTDB=llm_relay_seedtest
CT=llm-relay-seedtest
PG=llm-relay-postgres
APP=llm-relay
NET=llm-relay_relay
LIVE_DB=llm_relay
# 测试容器发布到宿主机的一个冷门端口：要调管理接口验证「没有默认分组时创建渠道」
PORT=18899

ok=0; bad=0
chk() { if [ "$2" = "$3" ]; then echo "  [通过] $1  $3"; ok=$((ok+1)); else echo "  [失败] $1  期望 $2 实际 $3"; bad=$((bad+1)); fi; }
now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }

docker image inspect llm-relay:local >/dev/null 2>&1 || { echo "没有 llm-relay:local 镜像，先跑 scripts/redeploy.sh"; echo HAS_FAILURE; exit 1; }
docker inspect "$APP" >/dev/null 2>&1 || { echo "找不到运行中的 $APP 容器"; echo HAS_FAILURE; exit 1; }

DB_USER=$(docker exec "$APP" printenv DB_USER)
DB_PASSWORD=$(docker exec "$APP" printenv DB_PASSWORD)

# 线上库的分组数：整轮跑完必须一个不差（这个用例全程只碰测试库）
LIVE_GROUPS_BEFORE=$(docker exec "$PG" psql -U "$DB_USER" -d "$LIVE_DB" -tAqc "SELECT count(*) FROM channel_groups")
LIVE_PROBE_BEFORE=$(docker exec "$PG" psql -U "$DB_USER" -d "$LIVE_DB" -tAqc "SELECT count(*) FROM channel_groups WHERE name IN ('默认分组','probe-keep')")

cleanup() {
  docker rm -f "$CT" >/dev/null 2>&1
  # 容器还连着库时 DROP 会被拒，WITH (FORCE) 用来断开残留连接
  docker exec "$PG" psql -U "$DB_USER" -d postgres -c "DROP DATABASE IF EXISTS $TESTDB WITH (FORCE)" >/dev/null 2>&1
}
trap cleanup EXIT
cleanup
docker exec "$PG" psql -U "$DB_USER" -d postgres -c "CREATE DATABASE $TESTDB" >/dev/null || { echo "建测试库失败"; echo HAS_FAILURE; exit 1; }

Q() { docker exec "$PG" psql -U "$DB_USER" -d "$TESTDB" -tAqc "$1" 2>&1; }
LIVE() { docker exec "$PG" psql -U "$DB_USER" -d "$LIVE_DB" -tAqc "$1" 2>&1; }

# 等这一次启动把 Seed 跑完：日志里出现「基线数据就绪」才算数。
# 必须带 --since，否则重启后看到的还是上一次启动的日志
wait_seed() {
  local since="$1"
  for _ in $(seq 1 60); do
    docker logs --since "$since" "$CT" 2>&1 | grep -q "基线数据就绪" && return 0
    sleep 0.5
  done
  echo "  容器没有在预期时间内完成初始化，日志尾部："
  docker logs "$CT" 2>&1 | tail -20
  return 1
}

start_app() {
  docker rm -f "$CT" >/dev/null 2>&1
  docker run -d --name "$CT" --network "$NET" -p "127.0.0.1:$PORT:8888" \
    -e SERVER_HOST=0.0.0.0 -e SERVER_PORT=8888 -e GIN_MODE=release \
    -e DB_HOST=postgres -e DB_PORT=5432 -e DB_USER="$DB_USER" -e DB_PASSWORD="$DB_PASSWORD" \
    -e DB_NAME="$TESTDB" -e DB_SSLMODE=disable -e LOG_FORMAT=text \
    -e RELAY_SECRET=seedtest-only-not-a-real-key \
    llm-relay:local >/dev/null
}

restart_app() {
  local since; since=$(now_utc)
  docker restart "$CT" >/dev/null
  wait_seed "$since"
}

echo "=== 1. 全新安装：应当建出一个默认分组 ==="
SINCE=$(now_utc)
start_app
wait_seed "$SINCE" || { echo HAS_FAILURE; exit 1; }
chk "分组总数" "1" "$(Q "SELECT count(*) FROM channel_groups")"
chk "默认分组存在" "1" "$(Q "SELECT count(*) FROM channel_groups WHERE name = '默认分组'")"
chk "被标为默认的分组数" "1" "$(Q "SELECT count(*) FROM channel_groups WHERE is_default")"

echo
echo "=== 2. 用户删掉默认分组（另留一个分组，避开「一个分组都没有」的自愈分支）==="
Q "INSERT INTO channel_groups (name, strategy, is_default, enabled, rpm, tpm, created_at, updated_at)
   VALUES ('probe-keep', 'failover', false, true, 0, 0, now(), now())" >/dev/null
chk "插入探针分组后总数" "2" "$(Q "SELECT count(*) FROM channel_groups")"
Q "DELETE FROM channel_groups WHERE name = '默认分组'" >/dev/null
chk "删除后总数" "1" "$(Q "SELECT count(*) FROM channel_groups")"

echo
echo "=== 3. 重启：删掉的默认分组不能自己回来（这就是被报的 bug）==="
restart_app || { echo HAS_FAILURE; exit 1; }
chk "重启后分组总数" "1" "$(Q "SELECT count(*) FROM channel_groups")"
chk "重启后没有「默认分组」" "0" "$(Q "SELECT count(*) FROM channel_groups WHERE name = '默认分组'")"
chk "重启后没有被标为默认的分组" "0" "$(Q "SELECT count(*) FROM channel_groups WHERE is_default")"

echo
echo "=== 4. 再重启一次：不能是「第一次不补、第二次补」==="
restart_app || { echo HAS_FAILURE; exit 1; }
chk "第二次重启后分组总数" "1" "$(Q "SELECT count(*) FROM channel_groups")"
chk "第二次重启后仍没有「默认分组」" "0" "$(Q "SELECT count(*) FROM channel_groups WHERE name = '默认分组'")"

echo
echo "=== 5. 没有默认分组时，创建渠道要给出明确原因（不能瞎指一个 id）==="
# 原实现是查不到默认分组就返回写死的 1：那个 id 在本机要么不存在
# （外键报错，报出来是一句 SQL 约束错误），要么是另一个分组（渠道静默进错组）。
API="http://127.0.0.1:$PORT/api/admin"
code=$(curl -s -o /tmp/dg-body-$$ -w '%{http_code}' -X POST "$API/channels" \
  -H 'Content-Type: application/json' \
  -d '{"name":"probe-no-group","protocol":"openai-chat","base_url":"http://x/v1","api_key":"k"}')
body=$(cat /tmp/dg-body-$$); rm -f /tmp/dg-body-$$
chk "不带 group_id 创建渠道的状态码" "400" "$code"
case "$body" in *没有默认分组*) chk "错误信息说明了原因" "yes" "yes";; *) chk "错误信息说明了原因" "yes" "no（实际：$body）";; esac

# 正对照：同一个请求带上显式分组就该成功 —— 说明 400 是因为缺默认分组，
# 而不是这份请求本身有问题
KEEP_ID=$(Q "SELECT id FROM channel_groups WHERE name = 'probe-keep'")
code2=$(curl -s -o /tmp/dg-body2-$$ -w '%{http_code}' -X POST "$API/channels" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"probe-with-group\",\"protocol\":\"openai-chat\",\"base_url\":\"http://x/v1\",\"api_key\":\"k\",\"group_id\":$KEEP_ID}")
body2=$(cat /tmp/dg-body2-$$); rm -f /tmp/dg-body2-$$
chk "带显式 group_id 创建渠道的状态码" "200" "$code2"
case "$body2" in *probe-with-group*) chk "渠道确实建出来了" "yes" "yes";; *) chk "渠道确实建出来了" "yes" "no（实际：$body2）";; esac

echo
echo "=== 6. 分组被删光（零个）时按全新安装自愈 ==="
# 这条是刻意的：零分组时渠道一个也建不出来（渠道必须挂在分组下），
# 库的状态与全新安装没有区别，所以会补一个默认分组回来
Q "DELETE FROM channels" >/dev/null
Q "DELETE FROM channel_groups" >/dev/null
restart_app || { echo HAS_FAILURE; exit 1; }
chk "删光后重启补回一个默认分组" "1" "$(Q "SELECT count(*) FROM channel_groups WHERE name = '默认分组'")"

echo
echo "=== 7. 线上库未被影响 ==="
chk "线上库分组数不变" "$LIVE_GROUPS_BEFORE" "$(LIVE "SELECT count(*) FROM channel_groups")"
chk "线上库里「默认分组 / probe-keep」的数量不变" "$LIVE_PROBE_BEFORE" "$(LIVE "SELECT count(*) FROM channel_groups WHERE name IN ('默认分组','probe-keep')")"
chk "线上库渠道数非零（确认连的是真库）" "yes" "$([ "$(LIVE "SELECT count(*) FROM channels")" -gt 0 ] && echo yes || echo no)"

echo
echo "=== 8. 清理 ==="
cleanup
chk "测试容器已删除" "0" "$(docker ps -a --filter "name=^${CT}$" --format '{{.Names}}' | wc -l | tr -d ' ')"
chk "测试库已删除" "0" "$(docker exec "$PG" psql -U "$DB_USER" -d postgres -tAqc "SELECT count(*) FROM pg_database WHERE datname = '$TESTDB'")"

echo
if [ "$bad" -eq 0 ]; then echo "通过 $ok 项"; echo ALL_PASS; else echo "通过 $ok 项，失败 $bad 项"; echo HAS_FAILURE; exit 1; fi
