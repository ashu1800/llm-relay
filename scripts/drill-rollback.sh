#!/usr/bin/env bash
# 端到端演练**回滚接口**：管理台里那个「回滚到上一版本」按钮走的就是它。
#
# 场景：
#   1. 当前跑着版本 A，并已有回滚标签（指向上一次更新前的镜像）
#   2. 构建并切到版本 B（模拟「更新成功了，但新版有问题」）
#   3. 通过 unix socket 调 POST /rollback（更新器不做参数化，只认固定动作）
#   4. 验证服务回到了标签所指的那个镜像
#
# 这条路径同时验证三件事，每一件之前都只是「看起来对」：
#   · findRollbackTarget 能找到回滚标签（不依赖更新器进程内存）
#   · updateImageAndRecreate 能用 RELAY_IMAGE 覆盖重建（不改 compose 文件）
#   · waitHealthy 在回滚后能确认服务真的起来了
set -uo pipefail
cd "${INSTALL_DIR:-/opt/llm-relay}/deploy"
SOCK="${RELAY_UPDATER_SOCKET:-/run/llm-relay-updater.sock}"

echo "### 0. 前置：容器必须在本地真实存在的镜像上"
CUR=$(docker inspect -f '{{.Image}}' llm-relay)
if ! docker image inspect "$CUR" >/dev/null 2>&1; then
  # 容器停在一个已被回收的镜像 ID 上（Docker 29 + containerd snapshotter
  # 的回收行为，见 README 的「回滚」一节）。这时任何「用旧镜像回滚」的
  # 实验都会失败，得到的结论是假的 —— 先重建一个真实存在的基线。
  echo "  容器所在的镜像已被回收，先重建基线…"
  docker compose -f docker-compose.yml build app >/dev/null 2>&1
  docker compose -f docker-compose.yml up -d --no-deps --force-recreate app >/dev/null 2>&1
  for i in $(seq 1 40); do
    curl -fsS -m 2 http://127.0.0.1:8888/readyz >/dev/null 2>&1 && break
    sleep 1
  done
  CUR=$(docker inspect -f '{{.Image}}' llm-relay)
fi
docker image inspect "$CUR" >/dev/null 2>&1 || { echo "  !! 仍无法建立可用的基线镜像"; exit 1; }
echo "  当前镜像: $CUR"

echo
echo "### 1. 把当前版本设为「回滚目标」（这正是更新器在更新前做的事）"
docker rmi -f llm-relay:rollback >/dev/null 2>&1 || true
docker tag "$CUR" llm-relay:rollback
ROLLBACK_VER=$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.version"}}' llm-relay:rollback)
echo "  llm-relay:rollback -> $CUR（版本 ${ROLLBACK_VER:-无标签}）"

echo
echo "### 2. 构建并切到一个不同的版本 B（模拟一次成功但有问题的新版）"
VERSION=v9.9.9-bad-release docker compose -f docker-compose.yml build app >/dev/null 2>&1
B_ID=$(docker image inspect -f '{{.Id}}' llm-relay:local)
RELAY_IMAGE="$B_ID" docker compose -f docker-compose.yml up -d --no-deps --force-recreate app >/dev/null 2>&1
for i in $(seq 1 40); do curl -fsS -m 2 http://127.0.0.1:8888/readyz >/dev/null 2>&1 && break; sleep 1; done
B_VER=$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.version"}}' "$B_ID")
NOW=$(docker inspect -f '{{.Image}}' llm-relay)
echo "  已切到 B: $NOW（版本 $B_VER）"
[ "$NOW" = "$B_ID" ] || { echo "  !! 没有切到 B，演练无意义"; exit 1; }
[ "$NOW" != "$CUR" ] || { echo "  !! B 与 A 相同，演练无意义"; exit 1; }

echo
echo "### 3. 经 unix socket 调用回滚接口（管理台按钮走的就是这条）"
curl -sS --unix-socket "$SOCK" -X POST http://updater/rollback \
  -H 'Content-Type: application/json' -d '{}' -o /tmp/rb-start.json -w '  HTTP %{http_code}\n'
python3 -m json.tool < /tmp/rb-start.json 2>/dev/null | sed 's/^/  /' | head -12

echo
echo "### 4. 跟踪回滚进度（最多 4 分钟）"
DONE=no
for i in $(seq 1 80); do
  sleep 3
  curl -sS --unix-socket "$SOCK" http://updater/progress -o /tmp/rb-prog.json 2>/dev/null || continue
  if [ "$(python3 -c 'import json;print(json.load(open("/tmp/rb-prog.json")).get("done",False))' 2>/dev/null)" = "True" ]; then
    DONE=yes; break
  fi
done

if [ "$DONE" = "yes" ]; then
  python3 - <<'PY'
import json
d = json.load(open('/tmp/rb-prog.json'))
print(f"  阶段: {d.get('phase')}  失败: {d.get('failed')}")
print(f"  消息: {d.get('message')}")
print("  --- 日志末尾 ---")
for line in (d.get('logs') or [])[-12:]:
    print("   ", line)
PY
else
  echo "  !! 回滚在 4 分钟内未结束"
fi

echo
echo "### 5. 判定"
rc=0
FINAL=$(docker inspect -f '{{.Image}}' llm-relay)
FINAL_VER=$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.version"}}' "$FINAL" 2>/dev/null)
echo "  回滚后镜像: $FINAL"
echo "  回滚后版本: ${FINAL_VER:-（无标签）}"

if [ "$FINAL" = "$CUR" ]; then
  echo "  ✓ 容器回到了回滚标签所指的镜像"
else
  echo "  ✗ 容器没有回到目标镜像（期望 $CUR）"; rc=1
fi
if [ "$FINAL_VER" = "$ROLLBACK_VER" ]; then
  echo "  ✓ 版本与回滚目标一致（$ROLLBACK_VER）"
else
  echo "  ✗ 版本不一致（期望 $ROLLBACK_VER）"; rc=1
fi
if curl -fsS -m 3 http://127.0.0.1:8888/readyz >/dev/null 2>&1; then
  echo "  ✓ 服务健康"
else
  echo "  ✗ 服务不健康"; rc=1
fi

echo
echo "### 6. 收尾：把 llm-relay:local 恢复成真实版本"
# 第 2 步用 VERSION=v9.9.9-bad-release 构建，把 llm-relay:local 指到了一个
# 假版本上。不收尾的话，下次真实更新时更新器会读出 v9.9.9-bad-release 这个
# 版本号，于是「已是最新」判断错乱、面板上显示一个不存在的版本。
# 重建一次（不覆盖 VERSION，让它取 .env 里的真实值）即可复原。
if docker compose -f docker-compose.yml build app >/dev/null 2>&1; then
  REAL_VER=$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.version"}}' llm-relay:local)
  echo "  llm-relay:local 已恢复，版本: ${REAL_VER:-（无标签）}"
else
  echo "  ✗ 恢复 llm-relay:local 失败 —— 请手动执行: cd $PWD && docker compose build app"
  rc=1
fi

echo
[ $rc -eq 0 ] && echo "ALL_PASS" || echo "HAS_FAILURE"
exit $rc
