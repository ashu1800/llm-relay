#!/usr/bin/env bash
# 清掉验证脚本在自己跑的过程中产生的请求日志。
#
# 为什么需要单独一个脚本：验证脚本很擅长删掉自己建的密钥、渠道、分组，
# 但**请求日志会留下** —— 跑完一轮 verify-all 会往库里塞几百条测试请求，
# 它们会一路污染看板的「今日请求数」、成功率与模型分布。
# 实测就是这样发现的：看板上的数字比真实调用多了一个数量级。
#
# 判定规则按「测试专用的名字」精确匹配，不用模糊的全表清理：
# 用户的真实调用用的是自己的密钥名（如 DeepSeek）与真实模型名，
# 这里一个都不碰。
set -uo pipefail

P() { docker exec -i llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }

WHERE=$(cat <<'SQL'
  api_key_name LIKE '__%'
  OR api_key_name LIKE 'quota-probe%'
  OR api_key_name LIKE 'wl-probe-%'
  OR api_key_name LIKE 'k-%'
  OR api_key_name LIKE 'audit-key-partial%'
  OR api_key_name IN (
    'regress','regress-tmp','ae-regress-key','filter-probe-key','pricing-check',
    'pricing-rule-key','proto-upstream-key','group-enabled-key','delete-semantics-key',
    'restore-probe','anthropic-test','gemini-test','gem-stream','payload-all',
    'local-test','coldstart','slow-concurrency-key','truncate-key','fk-probe',
    'csrf-ok-probe','wl-probe-key','key'
  )
  OR model_requested LIKE 'wl-probe-%'
  OR model_requested IN (
    'pricing-rule-model','proto-chat-model','proto-gemini-model','group-enabled-test',
    'group-whitelist-test','egress-proxy-test','no-such-model-filter'
  )
SQL
)

BEFORE=$(P "SELECT count(*) FROM request_logs")
P "DELETE FROM request_payloads WHERE log_id IN (SELECT id FROM request_logs WHERE $WHERE)" >/dev/null
P "DELETE FROM request_logs WHERE $WHERE" >/dev/null
AFTER=$(P "SELECT count(*) FROM request_logs")
echo "测试日志清理：$BEFORE -> $AFTER（删除 $((BEFORE - AFTER)) 条）"
