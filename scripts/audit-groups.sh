#!/usr/bin/env bash
P() { docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -F'|' -c "$1"; }
echo "=== 分组 ==="
P "SELECT id, name, strategy, is_default, enabled FROM channel_groups ORDER BY id"
echo
echo "=== 渠道->分组 ==="
P "SELECT id, name, group_id, enabled FROM channels ORDER BY id"
echo
echo "=== 密钥的白名单字段现状 ==="
P "SELECT id, name, allowed_models::text, allowed_groups::text FROM api_keys ORDER BY id LIMIT 8"
echo
echo "=== 密钥总数 ==="
P "SELECT count(*) FROM api_keys"
echo
echo "=== 占位页内容 ==="
grep -c . "/path/to/llm-relay/frontend/src/views/PlaceholderView.vue"
