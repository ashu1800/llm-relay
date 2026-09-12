#!/usr/bin/env bash
cd "/path/to/llm-relay" || exit 1
echo "=== 提交历史 ==="
git log --oneline | head -14
echo
echo "=== 工作区 ==="
git status --short && echo "  (空=干净)"
echo
echo "=== 测试 ==="
(cd backend && GOPROXY=https://goproxy.cn,direct GOSUMDB=off go test ./... 2>&1 | grep -vE "no test files|^go: down")
echo
echo "=== 管理接口回归 ==="
bash scripts/test-regression.sh 2>&1 | tail -9
echo
echo "=== 容器 ==="
docker ps --format "  {{.Names}}  {{.Status}}"
echo
echo "=== 服务 ==="
curl -s -m 5 http://127.0.0.1:8888/healthz
echo
echo "=== 数据规模 ==="
docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT '渠道 '||(SELECT count(*) FROM channels)||' | 模型 '||(SELECT count(*) FROM models)||' | 密钥 '||(SELECT count(*) FROM api_keys)||' | 日志 '||(SELECT count(*) FROM request_logs)||' | 定价 '||(SELECT count(*) FROM model_pricings)||' | 模板 '||(SELECT count(*) FROM channel_templates)"
echo
echo "=== 绑定地址 ==="
docker port llm-relay
echo
echo "=== 主密钥 ==="
docker exec llm-relay env | grep -c "^RELAY_SECRET=." | sed 's/^/  RELAY_SECRET 已注入: /'
echo
echo "=== README 阶段 ==="
grep "^- \[x\] Phase" README.md | sed 's/^/  /'
echo
echo FINAL_OK
