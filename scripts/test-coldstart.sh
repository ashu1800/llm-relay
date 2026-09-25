#!/usr/bin/env bash
# 重装演练：install.sh 重跑一遍，验证「重装/升级不丢数据」。
#
# 裸二进制形态下，数据在 PostgreSQL 里、密钥在 .env 里，二进制只是
# 一个可替换的文件 —— 重装后渠道、密钥、日志必须原样保留。
# 原先这个脚本验的是 Docker 形态的「拆容器删镜像 → 从零重建」，裸二进制
# 形态的对应物就是「重跑 install.sh」：它会沿用 .env 里的三把密钥、
# 备份数据库、替换二进制并重启服务。任何一环丢了，这里都要响。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${INSTALL_DIR:-/opt/llm-relay}/deploy/.env"
P() { db_psql -t -A -c "$1"; }

echo "=== 重装前的数据基线 ==="
LOGS_BEFORE=$(P "SELECT 'logs='||count(*) FROM request_logs" 2>/dev/null || echo "logs=?")
CHANNELS_BEFORE=$(P "SELECT 'channels='||count(*) FROM channels" 2>/dev/null || echo "channels=?")
KEYS_BEFORE=$(P "SELECT 'keys='||count(*) FROM api_keys" 2>/dev/null || echo "keys=?")
echo "  $LOGS_BEFORE $CHANNELS_BEFORE $KEYS_BEFORE"
SECRET_BEFORE="$(grep '^RELAY_SECRET=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]")"

echo
echo "=== 重装（install.sh，沿用 .env 密钥与库）==="
# 钉住当前已装版本：install.sh 默认拉 latest，而本机可能跑着比 latest 新的
# 自发布版本，误降级会把「重装不丢数据」与「版本回退」混在一起
CUR_VER="$(curl -fsS -m 5 "$API/system/info" 2>/dev/null | python3 -c "import sys,json;print(json.load(sys.stdin).get('version',''))" || true)"
CUR_VER="${CUR_VER:-$(tr -d '\r\n' < "$ROOT/backend/internal/version/VERSION" 2>/dev/null || true)}"
if [ -n "$CUR_VER" ]; then
  echo "  当前版本 $CUR_VER，本次重装钉住它"
  VER_ENV=("VERSION=$CUR_VER")
else
  echo "  取不到当前版本，按 latest 重装"
  VER_ENV=()
fi
START=$(date +%s)
env "${VER_ENV[@]}" bash "$ROOT/deploy/install.sh" > /tmp/cold.log 2>&1
RC=$?
END=$(date +%s)
echo "  install.sh 退出码: $RC"
echo "  重装总耗时: $((END-START)) 秒"
tail -3 /tmp/cold.log

# install.sh 失败时必须让本脚本也失败。
# 原来只把退出码打印出来继续往下跑，最后无条件 echo DONE ——
# 于是「重装彻底失败」在调用方（verify-all 之类）看来是一次成功。
if [ "$RC" != "0" ]; then
  echo "  !! install.sh 退出码非 0，重装失败"
  echo "  ---- /tmp/cold.log 末尾 ----"
  tail -25 /tmp/cold.log
  echo "COLDSTART_FAILED"
  exit 1
fi

echo
echo "=== 启动后的健康检查 ==="
for _ in $(seq 1 60); do
  curl -fsS -m 3 http://127.0.0.1:8888/healthz >/dev/null 2>&1 && break
  sleep 2
done
echo "  /healthz: $(curl -s -m 5 http://127.0.0.1:8888/healthz)"

echo
echo "=== 数据是否完好 ==="
LOGS_AFTER=$(P "SELECT 'logs='||count(*) FROM request_logs")
CHANNELS_AFTER=$(P "SELECT 'channels='||count(*) FROM channels")
KEYS_AFTER=$(P "SELECT 'keys='||count(*) FROM api_keys")
echo "  $LOGS_AFTER $CHANNELS_AFTER $KEYS_AFTER"
[ "$LOGS_BEFORE" = "$LOGS_AFTER" ] || { echo "  !! 日志数量变了"; echo "COLDSTART_FAILED"; exit 1; }
[ "$CHANNELS_BEFORE" = "$CHANNELS_AFTER" ] || { echo "  !! 渠道数量变了"; echo "COLDSTART_FAILED"; exit 1; }
[ "$KEYS_BEFORE" = "$KEYS_AFTER" ] || { echo "  !! 密钥数量变了"; echo "COLDSTART_FAILED"; exit 1; }

echo
echo "=== 主密钥仍在（重装沿用）==="
SECRET_AFTER="$(grep '^RELAY_SECRET=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "\"'[[:space:]]")"
if [ -n "$SECRET_BEFORE" ]; then
  [ "$SECRET_BEFORE" = "$SECRET_AFTER" ] && echo "  RELAY_SECRET 已沿用" \
    || { echo "  !! RELAY_SECRET 变了，渠道密钥会解不开"; echo "COLDSTART_FAILED"; exit 1; }
else
  echo "  重装前没有 RELAY_SECRET（首次安装场景），已新生成: $([ -n "$SECRET_AFTER" ] && echo 是 || echo 否)"
fi

echo
echo "=== 渠道仍可调用 ==="
SK=$(curl -s -X POST http://127.0.0.1:8888/api/admin/keys -H 'Content-Type: application/json' \
  -d '{"name":"coldstart","rate_limit_rpm":-1}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
MODEL=$(P "SELECT m.public_name FROM channel_models m JOIN channels c ON c.id=m.channel_id AND c.enabled=true WHERE m.enabled=true ORDER BY m.id LIMIT 1")
if [ -z "$MODEL" ]; then
  echo "  库里没有可用渠道模型，跳过真实调用（数据完好性已在上面验证）"
else
  code=$(curl -s -o /tmp/cs.json -w "%{http_code}" -m 60 http://127.0.0.1:8888/v1/chat/completions \
    -H "Authorization: Bearer $SK" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$MODEL\",\"max_tokens\":5,\"messages\":[{\"role\":\"user\",\"content\":\"说一个字\"}]}")
  echo "  真实调用（$MODEL）: HTTP $code"
  if [ "$code" != "200" ]; then
    echo "  !! 真实调用返回 HTTP $code，响应体："
    head -c 400 /tmp/cs.json; echo
    echo "COLDSTART_FAILED"
    exit 1
  fi
fi
KID=$(curl -s http://127.0.0.1:8888/api/admin/keys | python3 -c "
import sys,json
for k in json.load(sys.stdin)['items']:
    if k['name']=='coldstart': print(k['id'])
")
# 删除失败要报出来：残留的测试密钥会一直占用配额，也让下次跑这个脚本时
# 「同名密钥」的状态不确定
if [ -n "$KID" ]; then
  if curl -s -o /dev/null -X DELETE "http://127.0.0.1:8888/api/admin/keys/$KID"; then
    echo "  测试密钥已清理"
  else
    echo "  !! 测试密钥 #$KID 删除失败，请手工清理"
    echo "COLDSTART_PARTIAL"
    exit 1
  fi
else
  echo "  !! 没有找到名为 coldstart 的测试密钥（创建可能失败了）"
  echo "COLDSTART_PARTIAL"
  exit 1
fi

echo COLDSTART_DONE
