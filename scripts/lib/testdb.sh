# shellcheck shell=bash
# 测试脚本共用的数据库访问层（原生 psql over TCP，无 Docker）。
#
# 用法（在每个测试脚本里 source 一次）：
#   source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"
# 之后用 db_psql 查库：
#   db_psql -t -A -c "SELECT count(*) FROM channels"
#
# 连接参数的解析顺序（取第一个非空值）：
#   1. 已在环境里的 RELAY_TEST_DB_HOST / RELAY_TEST_DB_PORT / RELAY_TEST_DB_USER /
#      RELAY_TEST_DB_NAME / RELAY_TEST_DB_PASSWORD
#   2. 部署配置 ${INSTALL_DIR:-/opt/llm-relay}/deploy/.env 的
#      DB_HOST / DB_PORT / DB_USER / DB_NAME / DB_PASSWORD
#   3. 默认 127.0.0.1:5432。.env 没有 DB_PORT 键时（旧部署的 .env 没有），
#      按 5432 → 15432 逐个探测 —— 兼容「Docker postgres 发布在 15432」的
#      旧部署与「原生 postgres 5432」的新部署，迁移前后都能用。

testdb_env_val() {
  grep -E "^$1=" "$2" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]"
}

testdb_load() {
  [ -n "${TESTDB_LOADED:-}" ] && return 0
  TESTDB_LOADED=1

  local env_file="${INSTALL_DIR:-/opt/llm-relay}/deploy/.env"
  : "${RELAY_TEST_DB_HOST:=$(testdb_env_val DB_HOST "$env_file")}"
  : "${RELAY_TEST_DB_USER:=$(testdb_env_val DB_USER "$env_file")}"
  : "${RELAY_TEST_DB_NAME:=$(testdb_env_val DB_NAME "$env_file")}"
  : "${RELAY_TEST_DB_PASSWORD:=$(testdb_env_val DB_PASSWORD "$env_file")}"
  RELAY_TEST_DB_HOST="${RELAY_TEST_DB_HOST:-127.0.0.1}"
  RELAY_TEST_DB_USER="${RELAY_TEST_DB_USER:-llmrelay}"
  RELAY_TEST_DB_NAME="${RELAY_TEST_DB_NAME:-llm_relay}"

  if [ -z "${RELAY_TEST_DB_PORT:-}" ]; then
    RELAY_TEST_DB_PORT="$(testdb_env_val DB_PORT "$env_file")"
  fi
  if [ -z "$RELAY_TEST_DB_PORT" ]; then
    local p
    for p in 5432 15432; do
      if PGPASSWORD="$RELAY_TEST_DB_PASSWORD" pg_isready -h "$RELAY_TEST_DB_HOST" -p "$p" -t 2 >/dev/null 2>&1; then
        RELAY_TEST_DB_PORT="$p"
        break
      fi
    done
  fi
  RELAY_TEST_DB_PORT="${RELAY_TEST_DB_PORT:-5432}"

  export RELAY_TEST_DB_HOST RELAY_TEST_DB_PORT RELAY_TEST_DB_USER RELAY_TEST_DB_NAME
  # 密码经环境变量交给 libpq：不进 argv（ps aux 看不见）
  PGPASSWORD="$RELAY_TEST_DB_PASSWORD"
  export PGPASSWORD
}
testdb_load

# db_psql 的参数就是 psql 的参数（-c / -f / -t -A 等），stdin 原样透传
db_psql() {
  psql -h "$RELAY_TEST_DB_HOST" -p "$RELAY_TEST_DB_PORT" -U "$RELAY_TEST_DB_USER" -d "$RELAY_TEST_DB_NAME" "$@"
}
