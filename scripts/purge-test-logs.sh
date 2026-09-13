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

# 判定「名字以双下划线开头」必须用 left/starts_with，**不能**写 LIKE '__%'：
# LIKE 里的下划线是「任意单个字符」的通配符，'__%' 的真实含义是
# 「任意两个字符开头的任意字符串」—— 它会命中用户自己的密钥名（如 DeepSeek），
# 把真实调用日志一并删光。实测踩过：一次清理删掉了全部真实日志。
WHERE=$(cat <<'SQL'
  starts_with(api_key_name, '__')
  OR api_key_name LIKE 'quota-probe%'
  OR starts_with(api_key_name, 'wl-probe-')
  OR api_key_name LIKE 'k-%'
  OR api_key_name LIKE 'audit-key-partial%'
  OR api_key_name IN (
    'regress','regress-tmp','ae-regress-key','filter-probe-key','pricing-check',
    'pricing-rule-key','proto-upstream-key','proto-gemini-key','proto-gemini-error',
    'group-enabled-key','delete-semantics-key',
    'restore-probe','anthropic-test','gemini-test','gem-stream','payload-all',
    'local-test','coldstart','slow-concurrency-key','truncate-key','fk-probe',
    'csrf-ok-probe','wl-probe-key','key'
  )
  OR starts_with(model_requested, 'wl-probe-')
  -- 定价套件（test-pricing*.sh / test-cost.sh）建的模型名一律 __ 前缀：
  -- 跑到一半被打断时，日志由这里兜底清掉。starts_with 是字面匹配，
  -- 不会像 LIKE '__%' 那样把真实模型名一起命中
  OR starts_with(model_requested, '__')
  OR model_requested IN (
    'pricing-rule-model','proto-chat-model','proto-gemini-model','proto-gemini-error',
    'group-enabled-test',
    'group-whitelist-test','egress-proxy-test','no-such-model-filter'
  )
SQL
)

BEFORE=$(P "SELECT count(*) FROM request_logs")
P "DELETE FROM request_payloads WHERE log_id IN (SELECT id FROM request_logs WHERE $WHERE)" >/dev/null
P "DELETE FROM request_logs WHERE $WHERE" >/dev/null
AFTER=$(P "SELECT count(*) FROM request_logs")
echo "测试日志清理：$BEFORE -> $AFTER（删除 $((BEFORE - AFTER)) 条）"
