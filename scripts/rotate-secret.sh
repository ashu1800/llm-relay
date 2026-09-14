#!/usr/bin/env bash
# 把运行中的实例从内置默认主密钥迁移到随机主密钥。
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

OLD_SECRET="llm-relay-dev-secret-change-me"
ENV_FILE=/opt/llm-relay/deploy/.env

echo "=== 1. 生成新的主密钥 ==="
if grep -q '^RELAY_SECRET=.\+' "$ENV_FILE" 2>/dev/null; then
  NEW_SECRET="$(grep '^RELAY_SECRET=' "$ENV_FILE" | head -1 | cut -d= -f2-)"
  echo "  .env 中已有主密钥，复用之"
else
  NEW_SECRET="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  echo "  已生成新主密钥（长度 ${#NEW_SECRET}），暂未写入 .env"
fi

echo
echo "=== 2. 编译并投放到容器 ==="
(cd backend && export GOPROXY=https://goproxy.cn,direct GOSUMDB=off CGO_ENABLED=0 && \
  GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/rotate-secret ./cmd/rotate-secret) || exit 1
docker cp /tmp/rotate-secret llm-relay:/tmp/rotate-secret >/dev/null && echo "  已投放"

echo
echo "=== 3. dry-run：确认旧主密钥能解开全部渠道 ==="
docker exec -e RELAY_SECRET="$NEW_SECRET" llm-relay /tmp/rotate-secret -old-secret "$OLD_SECRET" -dry-run

echo
echo "=== 4. 正式迁移 ==="
docker exec -e RELAY_SECRET="$NEW_SECRET" llm-relay /tmp/rotate-secret -old-secret "$OLD_SECRET"
RC=$?
if [ "$RC" != "0" ]; then echo "  迁移失败，中止（未改动 .env）"; exit 1; fi

echo
echo "=== 5. 写入 .env ==="
if grep -q '^RELAY_SECRET=.\+' "$ENV_FILE"; then
  sed -i "s|^RELAY_SECRET=.*|RELAY_SECRET=$NEW_SECRET|" "$ENV_FILE"
else
  printf 'RELAY_SECRET=%s\n' "$NEW_SECRET" >> "$ENV_FILE"
fi
chmod 600 "$ENV_FILE"
echo "  已写入（值不打印）"
grep -c '^RELAY_SECRET=.' "$ENV_FILE" | sed 's/^/  .env 中 RELAY_SECRET 行数: /'

echo
echo "=== 6. 清理容器内工具 ==="
docker exec llm-relay rm -f /tmp/rotate-secret && rm -f /tmp/rotate-secret && echo "  已清理"
echo "STAGE1_DONE"
