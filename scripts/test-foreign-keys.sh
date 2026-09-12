#!/usr/bin/env bash
set -uo pipefail
cd "/path/to/llm-relay" || exit 1
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1" 2>&1; }
ok=0; bad=0
chk() { if [ "$2" = "$3" ]; then echo "  [通过] $1  $3"; ok=$((ok+1)); else echo "  [失败] $1  期望 $2 实际 $3"; bad=$((bad+1)); fi; }
chkcontains() { case "$3" in *"$2"*) echo "  [通过] $1"; ok=$((ok+1));; *) echo "  [失败] $1  期望包含「$2」实际 $3"; bad=$((bad+1));; esac; }

bash scripts/redeploy.sh || { echo "部署失败"; exit 1; }

# 防御性清理：脚本中途失败时不要留下探测数据
P "DELETE FROM channel_models WHERE channel_id IN (SELECT id FROM channels WHERE name = 'fk-cascade-probe')" >/dev/null
P "DELETE FROM channels WHERE name = 'fk-cascade-probe'" >/dev/null
echo
echo "=== 1. 外键是否建起来了 ==="
P "SELECT conname FROM pg_constraint WHERE contype='f' ORDER BY conname"
chk "外键数量" "4" "$(P "SELECT count(*) FROM pg_constraint WHERE contype='f'")"

echo
echo "=== 2. 模板分组是否已回填（不应再有 0 或悬挂）==="
chk "group_id=0 的模板数" "0" "$(P 'SELECT count(*) FROM channel_templates WHERE group_id=0')"
chk "指向不存在分组的模板数" "0" "$(P 'SELECT count(*) FROM channel_templates t LEFT JOIN channel_groups g ON g.id=t.group_id WHERE g.id IS NULL')"

echo
echo "=== 3. 外键必须拦住孤儿数据 ==="
chkcontains "插入指向不存在渠道的绑定会被拒" "foreign key" \
  "$(P "INSERT INTO channel_models (channel_id, model_id, upstream_name, enabled, created_at, updated_at) VALUES (999999, 1, 'ghost', true, now(), now())")"
chkcontains "插入指向不存在模型的绑定会被拒" "foreign key" \
  "$(P "INSERT INTO channel_models (channel_id, model_id, upstream_name, enabled, created_at, updated_at) VALUES (1, 999999, 'ghost', true, now(), now())")"
chkcontains "把渠道改到不存在的分组会被拒" "foreign key" \
  "$(P "UPDATE channels SET group_id=999999 WHERE id=1")"

echo
echo "=== 4. 仍有渠道的分组删不掉 ==="
GID=$(P "SELECT group_id FROM channels LIMIT 1")
chkcontains "直接删有渠道的分组会被外键拒绝" "foreign key" \
  "$(P "DELETE FROM channel_groups WHERE id=$GID")"

echo
echo "=== 5. 删渠道时绑定级联清除（用临时渠道，不动真实数据）==="
# 不依赖 RETURNING：psql 的命令标签会和结果混在一起，取起来容易出错。
# 插进去再按名字查回来，简单且确定。
P "INSERT INTO channels (name, group_id, protocol, base_url, api_key_enc, weight, enabled, created_at, updated_at) VALUES ('fk-cascade-probe', 1, 'openai-chat', 'http://x', '', 1, false, now(), now())" >/dev/null
TMPCH=$(P "SELECT id FROM channels WHERE name = 'fk-cascade-probe'")
P "INSERT INTO channel_models (channel_id, model_id, upstream_name, enabled, created_at, updated_at) VALUES ($TMPCH, 1, 'probe', true, now(), now())" >/dev/null
BEFORE=$(P "SELECT count(*) FROM channel_models WHERE channel_id=$TMPCH")
P "DELETE FROM channels WHERE id=$TMPCH" >/dev/null
AFTER=$(P "SELECT count(*) FROM channel_models WHERE channel_id=$TMPCH")
chk "删除前绑定数" "1" "$BEFORE"
chk "删除后绑定数（级联清掉）" "0" "$AFTER"

echo
echo "=== 6. 真实数据未被影响 ==="
chk "渠道数（探测渠道已清理）" "2" "$(P 'SELECT count(*) FROM channels')"
chk "模型数" "2" "$(P 'SELECT count(*) FROM models')"
chk "分组数" "1" "$(P 'SELECT count(*) FROM channel_groups')"

echo
echo "通过 $ok 项，失败 $bad 项"
