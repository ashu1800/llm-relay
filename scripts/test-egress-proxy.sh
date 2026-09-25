#!/usr/bin/env bash
# 出站代理在转发路径上真的生效了吗？
#
# 判据不是「能调通」—— 直连也能调通，那证明不了任何事。这里把渠道的上游
# 地址写成一个**解析不出来**的域名（xxx.invalid），再让测试代理把它改写到
# mock 上游：只有走了代理的请求才可能成功，直连必然 DNS 失败。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup


API=${API:-http://127.0.0.1:8888/api/admin}
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PG="db_psql -t -A -c"
PROXY_PORT=18098
FAKE_HOST=egress-probe.invalid
MODEL=egress-proxy-test

pass=0; fail=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; pass=$((pass + 1));
  else echo "  [失败] $1 期望 $2 实际 $3"; fail=$((fail + 1)); fi
}
chk_has() {
  case "$3" in *"$2"*) echo "  [通过] $1"; pass=$((pass + 1));;
    *) echo "  [失败] $1：期望包含「$2」，实际「$3」"; fail=$((fail + 1));; esac
}

cleanup() {
  for id in $($PG "SELECT id FROM api_keys WHERE starts_with(name, '__egress_')"); do
    curl -s -o /dev/null -X DELETE "$API/keys/$id"
  done
  for id in $($PG "SELECT id FROM channels WHERE starts_with(name, '__egress_')"); do
    curl -s -o /dev/null -X DELETE "$API/channels/$id"
  done
  for id in $($PG "SELECT id FROM proxies WHERE starts_with(name, '__egress_')"); do
    curl -s -o /dev/null -X DELETE "$API/proxies/$id"
  done
  for id in $($PG "SELECT id FROM channel_groups WHERE starts_with(name, '__egress_')"); do
    curl -s -o /dev/null -X DELETE "$API/groups/$id"
  done
  pkill -f mini-proxy.py 2>/dev/null
}
trap cleanup EXIT
cleanup

bash "$ROOT/scripts/ensure-mock-upstream.sh" || exit 1
# mock 上游以宿主 node 进程跑在 127.0.0.1:9997，服务也在宿主回环上，
# 所以代理与上游改写目标统一是 127.0.0.1（原先走 docker 网桥网关 +
# 容器 IP 的两段地址已不需要）
echo "mock 上游 127.0.0.1:9997，代理地址 127.0.0.1:$PROXY_PORT"

MINI_PROXY_PORT=$PROXY_PORT MINI_PROXY_REWRITE="$FAKE_HOST=127.0.0.1" \
  nohup python3 "$ROOT/scripts/mini-proxy.py" >/tmp/mini-proxy-egress.log 2>&1 &
sleep 1

echo
echo "=== 准备：渠道指向假域名 + 一个可用代理 ==="
# 分组也自建并显式挂上：库里可能没有默认分组（用户删掉或取消标记后，
# 后端对「不带 group_id 且无默认分组」的创建回 400）—— 那是另一个
# 用例的事（test-default-group.sh 第 5 节），别让它混进来
EGID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' \
  -d '{"name":"__egress_group__"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
PID=$(curl -s -X POST "$API/proxies" -H 'Content-Type: application/json' \
  -d "{\"name\":\"__egress_proxy__\",\"protocol\":\"http\",\"host\":\"127.0.0.1\",\"port\":$PROXY_PORT}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
CID=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' \
  -d "{\"name\":\"__egress_channel__\",\"protocol\":\"openai-chat\",\"base_url\":\"http://$FAKE_HOST:9997/v1\",\"api_key\":\"k\",\"group_id\":$EGID,\"proxy_id\":$PID,\"models\":[{\"public_name\":\"$MODEL\",\"upstream_name\":\"$MODEL\"}]}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
KEY=$(curl -s -X POST "$API/keys" -H 'Content-Type: application/json' -d '{"name":"__egress_key__"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["key"])')
echo "  代理 $PID 渠道 $CID"

call() {
  curl -s -o /tmp/egress.json -w '%{http_code}' -X POST http://127.0.0.1:8888/v1/chat/completions \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
}

echo
echo "=== 1. 渠道级代理：走代理才能通 ==="
chk "经代理调用成功" "200" "$(call)"

echo
echo "=== 2. 同一渠道改成直连：必然失败（证明上面那次真的是代理的功劳）==="
curl -s -o /dev/null -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d '{"proxy_id":0}'
CODE=$(call)
chk "直连时失败（假域名解析不了）" "502" "$CODE"
chk_has "错误里能看到解析失败" "no such host" "$(cat /tmp/egress.json)"

echo
echo "=== 3. 模型级代理：渠道直连，但白名单里这一个模型走代理 ==="
curl -s -o /dev/null -X PUT "$API/channels/$CID/models" -H 'Content-Type: application/json' \
  -d "{\"items\":[{\"public_name\":\"$MODEL\",\"upstream_name\":\"$MODEL\",\"proxy_id\":$PID}]}"
chk "模型级代理生效" "200" "$(call)"

echo
echo "=== 4. 停用代理后必须失败，绝不回退直连 ==="
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d '{"enabled":false}'
CODE=$(call)
chk "停用后请求失败" "502" "$CODE"
chk_has "错误指出是代理不可用" "代理" "$(cat /tmp/egress.json)"
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d '{"enabled":true}'
chk "重新启用后恢复" "200" "$(call)"

echo
echo "=== 5. 改了代理地址要立刻生效（缓存的客户端必须失效）==="
# 第 2 节把渠道级代理撤成了直连，这里先设回代理，否则下面测到的是「直连失败」
curl -s -o /dev/null -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d "{\"proxy_id\":$PID}"
chk "渠道级代理恢复后可用" "200" "$(call)"
# 再撤掉模型级代理，回到渠道级
curl -s -o /dev/null -X PUT "$API/channels/$CID/models" -H 'Content-Type: application/json' \
  -d "{\"items\":[{\"public_name\":\"$MODEL\",\"upstream_name\":\"$MODEL\"}]}"
chk "撤掉模型级代理后仍走渠道级代理" "200" "$(call)"
# 把代理地址改到一个没人监听的端口：请求必须立刻失败。
# 转发器按代理缓存 http.Client（连着连接池），不失效缓存的话
# 旧连接会继续用老地址拨下去，表现为「配置改了却不生效」
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d '{"port":9}'
CODE=$(call)
chk "改地址后立刻按新地址失败" "502" "$CODE"
chk_has "错误指出连不上代理" "连接被拒绝" "$(cat /tmp/egress.json)"
curl -s -o /dev/null -X PUT "$API/proxies/$PID" -H 'Content-Type: application/json' -d "{\"port\":$PROXY_PORT}"
chk "改回可用地址后恢复" "200" "$(call)"

echo
if [ "$fail" -eq 0 ]; then echo "通过 $pass 项，失败 0 项"; echo ALL_PASS
else echo "通过 $pass 项，失败 $fail 项"; echo HAS_FAILURE; exit 1; fi
