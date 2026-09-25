#!/usr/bin/env bash
# 把运行中的实例从内置默认主密钥迁移到随机主密钥。
#
# 直接部署（install.sh）首次安装就会生成随机 RELAY_SECRET，用不到本脚本；
# 它服务的是「内置默认密钥时期部署过、想换成随机密钥又不想重填上游密钥」的老实例。
set -uo pipefail

INSTALL_DIR="${INSTALL_DIR:-/opt/llm-relay}"
ENV_FILE="$INSTALL_DIR/deploy/.env"
OLD_SECRET="llm-relay-dev-secret-change-me"

[ -f "$ENV_FILE" ] || { echo "找不到 $ENV_FILE"; exit 1; }

echo "=== 1. 生成新的主密钥 ==="
if grep -q '^RELAY_SECRET=.\+' "$ENV_FILE" 2>/dev/null; then
  NEW_SECRET="$(grep '^RELAY_SECRET=' "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]")"
  echo "  .env 中已有主密钥，复用之"
else
  NEW_SECRET="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  echo "  已生成新主密钥（长度 ${#NEW_SECRET}），暂未写入 .env"
fi

echo
echo "=== 2. 定位 rotate-secret 工具 ==="
TOOL=""
if [ -x "$INSTALL_DIR/rotate-secret" ]; then
  TOOL="$INSTALL_DIR/rotate-secret"
  echo "  使用已安装的 $TOOL"
else
  # 老安装（发布归档还没有这个工具的时期）没有它：本地有 Go 就现编一个，
  # 否则给出明确指引，而不是让用户面对一句裸的 command not found
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  if command -v go >/dev/null 2>&1 && [ -f "$ROOT/backend/cmd/rotate-secret/main.go" ]; then
    echo "  本机编译 rotate-secret（一次性）"
    (cd "$ROOT/backend" && GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" GOSUMDB=off CGO_ENABLED=0 \
      go build -trimpath -ldflags="-s -w" -o /tmp/rotate-secret ./cmd/rotate-secret) || exit 1
    TOOL=/tmp/rotate-secret
  else
    echo "  未找到 rotate-secret 工具：升级到最新版（归档自带）后重跑，"
    echo "  或在装有 Go 的机器上进入 backend 执行 go build -o rotate-secret ./cmd/rotate-secret"
    exit 1
  fi
fi

# 数据库连接与运行参数交给工具自己读（config.Load 的优先级是环境变量最高），
# 与 systemd 跑主服务时用的是同一套键名、同一个 .env
set -a
# shellcheck disable=SC1090
source <(grep -E '^(DB_HOST|DB_PORT|DB_USER|DB_PASSWORD|DB_NAME|DB_SSLMODE)=' "$ENV_FILE")
set +a

# 两个密钥都走标准输入（-stdin 的第一行旧、第二行新），不进命令行参数。
# 命令行参数对同机其他用户是 /proc/<pid>/cmdline 可读的 —— 对一个
# 「怀疑旧密钥泄漏才要换」的场景，让它出现在进程列表里等于白换。
run_rotate() {
  printf '%s\n%s\n' "$OLD_SECRET" "$NEW_SECRET" | "$TOOL" -stdin "$@"
}

echo
echo "=== 3. dry-run：确认旧主密钥能解开全部渠道 ==="
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
echo "=== 6. 清理 ==="
[ "$TOOL" = "/tmp/rotate-secret" ] && rm -f "$TOOL"
echo "  完成。重启服务让主进程拿到新主密钥："
echo "    systemctl restart llm-relay"
echo "STAGE1_DONE"
