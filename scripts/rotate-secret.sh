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
# 两个密钥都走标准输入（-stdin 的第一行旧、第二行新），不进命令行参数。
#
# 为什么不用 docker exec -e：-e KEY=VALUE 里的值虽然没进**容器内**进程的
# argv，但它是 docker **客户端**自己的命令行参数 —— 同机其他用户
# ps aux 就能看到。轮换主密钥的动机通常正是「怀疑旧密钥泄漏」，
# 让它出现在进程列表里等于白换。管道输入不会留下任何可读痕迹。
run_rotate() {
  printf '%s\n%s\n' "$OLD_SECRET" "$NEW_SECRET" | \
    docker exec -i llm-relay /tmp/rotate-secret -stdin "$@"
}
run_rotate -dry-run

echo
echo "=== 4. 正式迁移 ==="
run_rotate
RC=$?
if [ "$RC" != "0" ]; then echo "  迁移失败，中止（未改动 .env）"; exit 1; fi

echo
echo "=== 5. 写入 .env ==="
# 不用 sed -i "s|...|RELAY_SECRET=$NEW_SECRET|"：那会把新密钥放进
# sed 自己的命令行参数里，同样能被 ps 看到。改用 awk 从环境变量读。
# 先写临时文件再原子替换，避免写到一半失败留下半截 .env（数据库里
# 已经用新密钥重加密过了，此时 .env 若损坏就再也解不开）。
if grep -q '^RELAY_SECRET=.\+' "$ENV_FILE"; then
  NEW_SECRET="$NEW_SECRET" awk '
    /^RELAY_SECRET=/ { print "RELAY_SECRET=" ENVIRON["NEW_SECRET"]; next }
    { print }
  ' "$ENV_FILE" > "$ENV_FILE.tmp" || { rm -f "$ENV_FILE.tmp"; echo "  写入失败"; exit 1; }
else
  cp "$ENV_FILE" "$ENV_FILE.tmp" 2>/dev/null || : > "$ENV_FILE.tmp"
  printf 'RELAY_SECRET=%s\n' "$NEW_SECRET" >> "$ENV_FILE.tmp"
fi
chmod 600 "$ENV_FILE.tmp"
mv "$ENV_FILE.tmp" "$ENV_FILE"
echo "  已写入（值不打印）"
grep -c '^RELAY_SECRET=.' "$ENV_FILE" | sed 's/^/  .env 中 RELAY_SECRET 行数: /'

echo
echo "=== 6. 清理容器内工具 ==="
docker exec llm-relay rm -f /tmp/rotate-secret && rm -f /tmp/rotate-secret && echo "  已清理"
echo "STAGE1_DONE"
