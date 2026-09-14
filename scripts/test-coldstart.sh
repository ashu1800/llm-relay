#!/usr/bin/env bash
set -uo pipefail
cd /opt/llm-relay/deploy || exit 1

echo "=== 冷启动前的数据基线 ==="
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT 'logs='||count(*) FROM request_logs" 2>/dev/null || echo "  (服务未运行)"
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT 'channels='||count(*) FROM channels" 2>/dev/null || true

echo
echo "=== 拆除容器与镜像（保留数据卷与 .env）==="
docker compose down --rmi local --remove-orphans >/dev/null 2>&1
docker rmi llm-relay:local >/dev/null 2>&1 || true
echo "  剩余容器: $(docker ps -a --filter name=llm-relay --format '{{.Names}}' | tr '\n' ' ')"
echo "  数据卷目录仍在: $(ls -d data/postgres >/dev/null 2>&1 && echo 是 || echo 否)"
echo "  .env 仍在: $(test -f .env && echo 是 || echo 否)"

echo
echo "=== 从零重建（计时）==="
START=$(date +%s)
bash "$(dirname "${BASH_SOURCE[0]}")/../deploy/install.sh" > /tmp/cold.log 2>&1
RC=$?
END=$(date +%s)
echo "  install.sh 退出码: $RC"
echo "  冷启动总耗时: $((END-START)) 秒"
tail -3 /tmp/cold.log

echo
echo "=== 启动后的健康检查 ==="
for _ in $(seq 1 60); do
  curl -fsS -m 3 http://127.0.0.1:8888/healthz >/dev/null 2>&1 && break
  sleep 2
done
echo "  /healthz: $(curl -s -m 5 http://127.0.0.1:8888/healthz)"

echo
echo "=== 数据是否完好 ==="
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT 'logs='||count(*) FROM request_logs"
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT 'channels='||count(*) FROM channels"
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT 'keys='||count(*) FROM api_keys"

echo
echo "=== 容器清单 ==="
docker ps --format "  {{.Names}}  {{.Status}}"

echo
echo "=== 主密钥仍在 ==="
docker exec llm-relay env | grep -c "^RELAY_SECRET=." | sed 's/^/  RELAY_SECRET 已注入: /'

echo
echo "=== 渠道仍可调用 ==="
SK=$(curl -s -X POST http://127.0.0.1:8888/api/admin/keys -H 'Content-Type: application/json' \
  -d '{"name":"coldstart","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
code=$(curl -s -o /tmp/cs.json -w "%{http_code}" -m 60 http://127.0.0.1:8888/v1/chat/completions \
  -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","max_tokens":5,"messages":[{"role":"user","content":"说一个字"}]}')
echo "  真实调用: HTTP $code"
KID=$(curl -s http://127.0.0.1:8888/api/admin/keys | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='coldstart': print(k['id'])
")
[ -n "$KID" ] && curl -s -o /dev/null -X DELETE "http://127.0.0.1:8888/api/admin/keys/$KID"
echo "  测试密钥已清理"
echo COLDSTART_DONE
