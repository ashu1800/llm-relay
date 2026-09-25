#!/usr/bin/env bash
# 从 install.sh 生成的备份恢复数据库。
#
# 为什么需要这个脚本：install.sh 每次升级前都会自动备份到
# <INSTALL_DIR>/backups/pre-upgrade-*.sql.gz，但仓库里**没有任何恢复入口**。
# 一个只能创建、不能使用的备份等于没有备份 —— 真出事时人要在压力下
# 现查 psql 的参数，而那时最容易犯的错是往错误的库里灌。
#
# 用法：
#   bash deploy/restore.sh --list                 # 看有哪些备份
#   bash deploy/restore.sh <备份文件>             # 恢复到默认库
#   bash deploy/restore.sh <备份文件> --yes       # 跳过确认
#
# 恢复是**破坏性**操作：它会先清空目标库的现有对象再导入，
# 因此脚本会打印影响范围并要求确认（除非显式 --yes）。
set -euo pipefail

APP_NAME="llm-relay"
INSTALL_DIR="${INSTALL_DIR:-/opt/llm-relay}"
BACKUP_DIR="${INSTALL_DIR}/backups"

log()  { printf '\033[1;36m[%s]\033[0m %s\n' "$APP_NAME" "$*"; }
ok()   { printf '\033[1;32m[%s]\033[0m %s\n' "$APP_NAME" "$*"; }
warn() { printf '\033[1;33m[%s]\033[0m %s\n' "$APP_NAME" "$*" >&2; }
die()  { printf '\033[1;31m[%s]\033[0m %s\n' "$APP_NAME" "$*" >&2; exit 1; }

# 从 .env 取连接参数，而不是再抄一份默认值：
# 用户改过 DB_NAME 时，写死的默认值会把数据导进一个不存在的库
ENV_FILE="$INSTALL_DIR/deploy/.env"
DB_USER="llmrelay"
DB_NAME="llm_relay"
DB_HOST="127.0.0.1"
DB_PORT="5432"
if [[ -f "$ENV_FILE" ]]; then
  # 只读需要的几个键。不要 source 整个文件：它含 RELAY_SECRET，
  # 而 `set -a; . file` 会把主密钥导出到本进程及其所有子进程的环境里
  DB_USER="$(grep -E '^DB_USER=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
  DB_NAME="$(grep -E '^DB_NAME=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
  DB_HOST="$(grep -E '^DB_HOST=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
  DB_PORT="$(grep -E '^DB_PORT=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)"
  DB_PASSWORD="$(grep -E '^DB_PASSWORD=' "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]" || true)"
  DB_USER="${DB_USER:-llmrelay}"
  DB_NAME="${DB_NAME:-llm_relay}"
  DB_HOST="${DB_HOST:-127.0.0.1}"
  DB_PORT="${DB_PORT:-5432}"
else
  warn "找不到 $ENV_FILE，使用默认连接 $DB_HOST:$DB_PORT / 库 $DB_NAME / 用户 $DB_USER"
fi

# 密码通过环境变量交给 libpq：不进 argv（ps aux 看不见），也不落盘
export PGPASSWORD="${DB_PASSWORD:-}"

psql_cmd() { psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" "$@"; }

# 先确认数据库真的可达：连接串错了的话，「恢复成功」只是没灌进去的假象
if ! psql_cmd -tAc 'SELECT 1' >/dev/null 2>&1; then
  die "连不上数据库 $DB_HOST:$DB_PORT/$DB_NAME（用户 $DB_USER）。请确认 PostgreSQL 在运行、.env 里的连接参数正确，且本机装有 psql 客户端"
fi

if [[ "${1:-}" == "--list" || -z "${1:-}" ]]; then
  log "可用备份（$BACKUP_DIR）："
  if [[ -d "$BACKUP_DIR" ]]; then
    # shellcheck disable=SC2012
    ls -lht "$BACKUP_DIR"/pre-upgrade-*.sql.gz "$BACKUP_DIR"/pre-restore-*.sql.gz 2>/dev/null | awk '{printf "  %s  %s  %s\n", $5, $6" "$7" "$8, $9}' \
      || echo "  （没有找到任何备份）"
  else
    echo "  （备份目录还不存在，说明没跑过升级前的自动备份）"
  fi
  echo
  echo "恢复：bash deploy/restore.sh <上面的某个文件>"
  exit 0
fi

BACKUP="$1"
shift
ASSUME_YES=no
for a in "$@"; do [[ "$a" == "--yes" || "$a" == "-y" ]] && ASSUME_YES=yes; done

[[ -f "$BACKUP" ]] || die "备份文件不存在：$BACKUP"

log "备份文件 : $BACKUP（$(du -h "$BACKUP" | cut -f1)）"
log "目标库   : $DB_HOST:$DB_PORT/$DB_NAME（用户 $DB_USER）"

# 恢复前再备份一次当前状态。否则「恢复到一个坏备份」会让本来还行的库也没了 ——
# 这是恢复操作最常见的事故形态。
SAFETY="$BACKUP_DIR/pre-restore-$(date +%Y%m%d-%H%M%S).sql.gz"
log "先把当前库另存为 $SAFETY（恢复失败时的退路）"
mkdir -p "$BACKUP_DIR"
if pg_dump -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" 2>/dev/null | gzip > "$SAFETY"; then
  chmod 600 "$SAFETY"
  ok "已生成回退点：$SAFETY"
else
  rm -f "$SAFETY"
  die "当前库备份失败，已中止（不敢在没有退路的情况下覆盖）"
fi

if [[ "$ASSUME_YES" != "yes" ]]; then
  echo
  warn "接下来会清空 $DB_NAME 里的现有对象，然后导入 $BACKUP"
  warn "这一步不可撤销（唯一退路是上面的 $SAFETY）"
  read -r -p "确认继续？输入 yes： " answer
  [[ "$answer" == "yes" ]] || die "已取消"
fi

# 解压到临时文件：psql 不能直接从管道读 gzip，
# 而用 zcat | psql 会让「宿主机上没有 zcat」变成失败原因
TMP_SQL="$(mktemp)"
trap 'rm -f "$TMP_SQL"' EXIT
case "$BACKUP" in
  *.gz) gzip -dc "$BACKUP" > "$TMP_SQL" ;;
  *)    cp "$BACKUP" "$TMP_SQL" ;;
esac
[[ -s "$TMP_SQL" ]] || die "解压后是空文件，备份可能已损坏"

log "导入中（库越大越慢）"
# --clean --if-exists 让脚本可以重复执行；失败不立即中止是因为
# pg_dump 的纯文本输出里 DROP 之类的语句对空库会报「对象不存在」，
# 那属于预期内的噪音。真正的失败由下面的行数比对来发现。
psql_cmd -v ON_ERROR_STOP=0 --quiet < "$TMP_SQL" 2>&1 \
  | grep -vE '^(NOTICE|SET|DROP|CREATE|ALTER|COPY|GRANT|REVOKE)' | head -20 || true

echo
log "校验导入结果"
# 用几张核心表的行数判断是否真的导进去了：只看 psql 的退出码是不够的
# （上面刻意容忍了部分错误），而「恢复成功但表是空的」是最坏的结果
for t in channels channel_groups api_keys; do
  n=$(psql_cmd -t -A -c "SELECT count(*) FROM $t" 2>/dev/null || echo "?")
  echo "  $t: $n"
done

echo
ok "恢复完成"
warn "渠道密钥依赖 RELAY_SECRET：若备份来自另一台机器或换过主密钥，"
warn "渠道详情里的密钥解不开，需要在管理界面重新填写上游密钥。"
echo
echo "回退到恢复前的状态："
echo "  bash deploy/restore.sh $SAFETY --yes"
