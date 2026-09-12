#!/usr/bin/env bash
# 代理管理回归：CRUD、校验、连通性测试（成功与失败两条路径）、被引用时不可删。
#
# 需要一个小代理来验证「成功路径」：scripts/mini-proxy.py。
# 不引第三方代理镜像 —— 那会让这个用例依赖外部网络。
set -uo pipefail

API=${API:-http://127.0.0.1:8888/api/admin}
PG="docker exec -i llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c"
PROXY_PORT=18099

pass=0
fail=0
chk() { # chk 说明 期望 实际
  if [ "$2" = "$3" ]; then
    echo "  [通过] $1 = $3"; pass=$((pass + 1))
  else
    echo "  [失败] $1 期望 $2 实际 $3"; fail=$((fail + 1))
  fi
}
chk_has() { # chk_has 说明 期望子串 实际
  case "$3" in
    *"$2"*) echo "  [通过] $1"; pass=$((pass + 1)) ;;
    *) echo "  [失败] $1：期望包含「$2」，实际「$3」"; fail=$((fail + 1)) ;;
  esac
}

cleanup() {
  for id in $($PG "SELECT id FROM proxies WHERE name LIKE '__probe_%'"); do
    curl -s -o /dev/null -X DELETE "$API/proxies/$id"
  done
  # 渠道上挂过的代理引用要还原成直连（正常流程不会走到这里，兜底）
  $PG "UPDATE channels SET proxy_id = 0 WHERE proxy_id <> 0" >/dev/null 2>&1
  pkill -f mini-proxy.py 2>/dev/null
}
trap cleanup EXIT
cleanup

echo "=== 0. 起一个真实的 HTTP 代理（成功路径要用它）==="
nohup python3 "$(dirname "$0")/mini-proxy.py" >/tmp/mini-proxy.log 2>&1 &
sleep 1
if curl -s -o /dev/null --max-time 5 -x "http://127.0.0.1:$PROXY_PORT" http://127.0.0.1:8888/ ; then
  echo "  代理就绪"
else
  echo "  代理没起来，后面与代理相关的成功断言会失败：$(cat /tmp/mini-proxy.log)"
fi
# 中继跑在容器里，访问宿主机上的这个代理要走 docker 网桥网关
GW=$(docker network inspect llm-relay_relay -f '{{(index .IPAM.Config 0).Gateway}}')
echo "  容器侧地址 $GW:$PROXY_PORT"

echo
echo "=== 1. 新建（带密码）==="
BODY=$(curl -s -X POST "$API/proxies" -H 'Content-Type: application/json' \
  -d "{\"name\":\"__probe_proxy__\",\"protocol\":\"http\",\"host\":\"$GW\",\"port\":$PROXY_PORT,\"username\":\"u\",\"password\":\"p\"}")
PID=$(echo "$BODY" | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
chk "创建返回 has_password" "True" "$(echo "$BODY" | python3 -c 'import sys,json;print(json.load(sys.stdin)["has_password"])')"
chk "响应体里不出现 password_enc" "0" "$(echo "$BODY" | grep -c 'password_enc')"
chk "库里的密码是密文（不是明文 p）" "t" "$($PG "SELECT password_enc <> 'p' AND password_enc <> '' FROM proxies WHERE id=$PID")"

echo
echo "=== 2. 连通性测试：成功路径 ==="
R=$(curl -s -X POST "$API/proxies/$PID/test" -H 'Content-Type: application/json' -d '{}')
chk "测试通过" "True" "$(echo "$R" | python3 -c 'import sys,json;print(json.load(sys.stdin)["ok"])')"
chk "拿到 204" "204" "$(echo "$R" | python3 -c 'import sys,json;print(json.load(sys.stdin)["status_code"])')"
chk "有耗时" "True" "$(echo "$R" | python3 -c 'import sys,json;print(json.load(sys.stdin)["latency_ms"] > 0)')"
chk "结果写回了记录" "ok" "$($PG "SELECT last_status FROM proxies WHERE id=$PID")"

echo
echo "=== 3. 连通性测试：失败路径要说清原因 ==="
BAD=$(curl -s -X POST "$API/proxies" -H 'Content-Type: application/json' \
  -d '{"name":"__probe_bad__","protocol":"http","host":"127.0.0.1","port":9}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
R=$(curl -s -X POST "$API/proxies/$BAD/test" -H 'Content-Type: application/json' -d '{}')
chk "判定为不通" "False" "$(echo "$R" | python3 -c 'import sys,json;print(json.load(sys.stdin)["ok"])')"
chk_has "错误信息是中文且指出原因" "连接被拒绝" "$(echo "$R" | python3 -c 'import sys,json;print(json.load(sys.stdin)["error"])')"
chk "失败也写回记录" "fail" "$($PG "SELECT last_status FROM proxies WHERE id=$BAD")"

echo
echo "=== 4. 测试未保存的配置（不落库）==="
chk "非法地址被拒" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/proxies/test" \
  -H 'Content-Type: application/json' -d '{"protocol":"http","host":"http://x","port":1}')"
chk "合法配置能测通" "True" "$(curl -s -X POST "$API/proxies/test" -H 'Content-Type: application/json' \
  -d "{\"protocol\":\"http\",\"host\":\"$GW\",\"port\":$PROXY_PORT}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["ok"])')"

echo
echo "=== 5. 校验 ==="
chk "未知协议被拒" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/proxies" -H 'Content-Type: application/json' -d '{"name":"__probe_x__","protocol":"ftp","host":"h","port":1}')"
chk "端口越界被拒" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/proxies" -H 'Content-Type: application/json' -d '{"name":"__probe_x__","protocol":"http","host":"h","port":70000}')"
chk "空名字被拒" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/proxies" -H 'Content-Type: application/json' -d '{"name":"  ","protocol":"http","host":"h","port":1}')"
chk "重名被拒" "409" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/proxies" -H 'Content-Type: application/json' -d "{\"name\":\"__probe_proxy__\",\"protocol\":\"http\",\"host\":\"$GW\",\"port\":$PROXY_PORT}")"

echo
echo "=== 6. 只改端口也要带上旧地址一起校验 ==="
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d '{"port":8080}'
chk "端口已更新" "8080" "$($PG "SELECT port FROM proxies WHERE id=$PID")"
chk "改连接参数后状态回到未测试" "unknown" "$($PG "SELECT last_status FROM proxies WHERE id=$PID")"
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d "{\"port\":$PROXY_PORT}"

echo
echo "=== 7. 密码三态 ==="
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d '{"name":"__probe_proxy__"}'
BEFORE=$($PG "SELECT password_enc FROM proxies WHERE id=$PID")
chk "不传密码 -> 保持原密文" "$BEFORE" "$($PG "SELECT password_enc FROM proxies WHERE id=$PID")"
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d '{"password":""}'
chk "传空串 -> 清空密码" "" "$($PG "SELECT password_enc FROM proxies WHERE id=$PID")"
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d '{"password":"new-secret"}'
chk "传新密码 -> 换成新密文" "t" "$($PG "SELECT password_enc <> '' AND password_enc <> 'new-secret' FROM proxies WHERE id=$PID")"

echo
echo "=== 8. 渠道在用时不能删 ==="
CH=$($PG "SELECT id FROM channels ORDER BY id LIMIT 1")
OLD=$($PG "SELECT proxy_id FROM channels WHERE id=$CH")
curl -s -o /dev/null -X PUT "$API/channels/$CH" -H 'Content-Type: application/json' -d "{\"proxy_id\":$PID}"
chk "渠道已指向该代理" "$PID" "$($PG "SELECT proxy_id FROM channels WHERE id=$CH")"
chk "被引用时删除被拒" "409" "$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$API/proxies/$PID")"
chk "引用不存在的代理被拒" "400" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/channels/$CH" -H 'Content-Type: application/json' -d '{"proxy_id":999999}')"
curl -s -o /dev/null -X PUT "$API/channels/$CH" -H 'Content-Type: application/json' -d "{\"proxy_id\":$OLD}"
# 改回直连要真的写进去：proxy_id 用值类型时，0 会被当成「没传」而忽略，
# 界面显示已保存、库里还指着代理 —— 这条断言就是为了钉住那个坑
chk "改回直连已落库" "$OLD" "$($PG "SELECT proxy_id FROM channels WHERE id=$CH")"

echo
echo "=== 9. 清理 ==="
chk "还原后可以删" "200" "$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$API/proxies/$PID")"
chk "失败那个也能删" "200" "$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$API/proxies/$BAD")"
chk "没有残留探针" "0" "$($PG "SELECT count(*) FROM proxies WHERE name LIKE '__probe_%'")"
chk "渠道引用已还原" "0" "$($PG "SELECT count(*) FROM channels WHERE proxy_id <> 0")"

echo
if [ "$fail" -eq 0 ]; then
  echo "通过 $pass 项，失败 0 项"
  echo ALL_PASS
else
  echo "通过 $pass 项，失败 $fail 项"
  echo HAS_FAILURE
  exit 1
fi
