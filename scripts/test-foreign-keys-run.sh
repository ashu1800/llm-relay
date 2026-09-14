#!/usr/bin/env bash
# 清理外键验证可能留下的探测数据，然后跑验证。
# 写成文件而不是内联命令：嵌套引号里的 SQL 太难拼对，
# 今天已经在这上面栽过三次，每次都是肉眼看不出来的转义问题。
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

cat > /tmp/cleanup-probe.sql <<'SQL'
DELETE FROM channel_models WHERE channel_id IN (SELECT id FROM channels WHERE name = 'fk-cascade-probe');
DELETE FROM channels WHERE name = 'fk-cascade-probe';
SQL

docker cp /tmp/cleanup-probe.sql llm-relay-postgres:/tmp/cleanup-probe.sql >/dev/null
echo "=== 清理探测数据 ==="
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -f /tmp/cleanup-probe.sql 2>&1 | tail -2

echo
bash scripts/test-foreign-keys.sh
