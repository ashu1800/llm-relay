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
# 两条：渠道->分组、白名单->渠道。
# 白名单->模型 的外键随模型表消失，模板->分组 的外键随模板管理下线消失，
# 每删一个功能都要回来改这个数字，否则会以「外键数量不对」误报
chk "外键数量" "2" "$(P "SELECT count(*) FROM pg_constraint WHERE contype='f'")"
chk "channel_templates 表已删除" "0" "$(P "SELECT count(*) FROM information_schema.tables WHERE table_name='channel_templates'")"

echo
echo "=== 3. 外键必须拦住孤儿数据 ==="
chkcontains "插入指向不存在渠道的白名单会被拒" "foreign key" \
  "$(P "INSERT INTO channel_models (channel_id, public_name, upstream_name, enabled, created_at, updated_at) VALUES (999999, 'ghost', 'ghost', true, now(), now())")"
# 复制一条已有白名单（渠道 id 必须一起复制，否则先撞上外键而不是唯一索引）
chkcontains "同一渠道内重复的对外名会被唯一索引拒绝" "duplicate key" \
  "$(P "INSERT INTO channel_models (channel_id, public_name, upstream_name, enabled, created_at, updated_at)
        SELECT channel_id, public_name, upstream_name, enabled, now(), now() FROM channel_models LIMIT 1")"
EXISTING_CH=$(P "SELECT id FROM channels ORDER BY id LIMIT 1")
chkcontains "把渠道改到不存在的分组会被拒" "foreign key" \
  "$(P "UPDATE channels SET group_id=999999 WHERE id=$EXISTING_CH")"

echo
echo "=== 4. 仍有渠道的分组删不掉 ==="
GID=$(P "SELECT group_id FROM channels LIMIT 1")
chkcontains "直接删有渠道的分组会被外键拒绝" "foreign key" \
  "$(P "DELETE FROM channel_groups WHERE id=$GID")"

echo
echo "=== 5. 删渠道时绑定级联清除（用临时渠道，不动真实数据）==="
# 分组 id 必须现取：写死 1 曾经能过，但分组是可以被删掉重建的
# （备份恢复、分组用例都会重排 id），某天它就不存在了，
# 报出来的是「syntax error at end of input」这种完全指错方向的错误 ——
# 因为插入失败后 TMPCH 是空的，后面的 SQL 就成了 WHERE channel_id=
GID_FOR_PROBE=$(P "SELECT id FROM channel_groups ORDER BY id LIMIT 1")
# 不依赖 RETURNING：psql 的命令标签会和结果混在一起，取起来容易出错。
# 插进去再按名字查回来，简单且确定。
P "INSERT INTO channels (name, group_id, protocol, base_url, api_key_enc, weight, enabled, created_at, updated_at) VALUES ('fk-cascade-probe', $GID_FOR_PROBE, 'openai-chat', 'http://x', '', 1, false, now(), now())" >/dev/null
TMPCH=$(P "SELECT id FROM channels WHERE name = 'fk-cascade-probe'")
P "INSERT INTO channel_models (channel_id, public_name, upstream_name, enabled, created_at, updated_at) VALUES ($TMPCH, 'fk-probe', 'fk-probe', true, now(), now())" >/dev/null
BEFORE=$(P "SELECT count(*) FROM channel_models WHERE channel_id=$TMPCH")
P "DELETE FROM channels WHERE id=$TMPCH" >/dev/null
AFTER=$(P "SELECT count(*) FROM channel_models WHERE channel_id=$TMPCH")
chk "删除前绑定数" "1" "$BEFORE"
chk "删除后绑定数（级联清掉）" "0" "$AFTER"

echo
echo "=== 6. 真实数据未被影响 ==="
# 具体数字随实际配置变化，这里只确认探测行没留下、表结构还在
chk "探测渠道已清理" "0" "$(P "SELECT count(*) FROM channels WHERE name = 'fk-cascade-probe'")"
chk "探测白名单已清理" "0" "$(P "SELECT count(*) FROM channel_models WHERE public_name = 'fk-probe'")"
chk "models 表已删除" "0" "$(P "SELECT count(*) FROM information_schema.tables WHERE table_name = 'models'")"
chk "providers 表已删除" "0" "$(P "SELECT count(*) FROM information_schema.tables WHERE table_name = 'providers'")"

echo
echo "通过 $ok 项，失败 $bad 项"
