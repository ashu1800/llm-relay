#!/usr/bin/env bash
# 部署后跑分组相关验证。
#
# 上游容器必须在部署**之后**起：install.sh 会重建 compose 项目，
# 顺带把挂在同一网络上的临时容器清掉。先起后部署的话，
# 测试会因为「上游不存在」而失败 —— 更糟的是，
# 「停用分组后调不通」这种用例会因为同样的原因**假通过**。
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

bash scripts/redeploy.sh || { echo "部署失败"; exit 1; }

docker rm -f slow-upstream >/dev/null 2>&1
docker run -d --name slow-upstream --network llm-relay_relay \
  -p 127.0.0.1:9997:9999 \
  -v "$PWD/scripts/slow-upstream.js:/app/server.js:ro" \
  node:22-alpine node /app/server.js >/dev/null

# 等到上游真的可用为止，而不是靠 sleep 猜
for i in $(seq 1 30); do
  if curl -fsS -m 2 http://127.0.0.1:9997/stats >/dev/null 2>&1; then break; fi
  sleep 1
done
if ! curl -fsS -m 3 http://127.0.0.1:9997/stats >/dev/null 2>&1; then
  echo "慢速上游未能启动，测试无法进行"
  exit 1
fi
echo "慢速上游就绪: $(curl -s -m 3 http://127.0.0.1:9997/stats)"
echo

python3 scripts/test-group-update.py
