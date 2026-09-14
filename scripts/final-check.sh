#!/usr/bin/env bash
# 收尾自查：把「这次改动之后系统到底是什么状态」一次性摊开给人看。
#
# 这个脚本必须是**失败会失败**的。原来它只按顺序打印一堆信息，
# 最后无条件 echo FINAL_OK —— 即使 go test 挂了、迁移失败、容器没起来，
# 末尾照样是 FINAL_OK，而且因为最后一条命令是 echo，退出码恒为 0。
# 一个永远说 OK 的检查比没有检查更糟：它会被当成「已验证」的证据。
#
# 所以这里收集各项结果，结尾统一判定并给出真实退出码。
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

fail=0
note() { echo "  !! $*"; fail=1; }

echo "=== 提交历史 ==="
git log --oneline | head -14
echo

echo "=== 工作区 ==="
# 不判失败：这里常有预期内的未提交改动（正在开发）
git status --short && echo "  (空=干净)"
echo

echo "=== 测试 ==="
# 先把输出收进变量再过滤，而不是直接接管道：
# 管道会把 go test 的退出码替换成 grep 的（grep 没匹配到行时返回 1，
# 反而让「全部通过」看起来像失败），所以下面用输出内容自己判断。
test_out=$(cd backend && GOPROXY=https://goproxy.cn,direct GOSUMDB=off go test ./... 2>&1)
printf '%s\n' "$test_out" | grep -vE "no test files|^go: down"
if printf '%s\n' "$test_out" | grep -qE '^(FAIL|--- FAIL)'; then
  note "go test 有失败用例"
fi
if ! printf '%s\n' "$test_out" | grep -qE '^ok\s'; then
  note "go test 没有任何包报告 ok（可能整包编译失败）"
fi
echo

echo "=== 管理接口回归 ==="
reg=$(bash scripts/test-regression.sh 2>&1)
printf '%s\n' "$reg" | tail -9
# test-regression.sh 正常跑完的最后一行是 DONE
printf '%s\n' "$reg" | grep -q '^DONE$' || note "管理接口回归没有跑到结尾（缺少 DONE）"
echo

echo "=== 容器 ==="
docker ps --format "  {{.Names}}  {{.Status}}"
# 两个容器都要在跑，否则下面的查询会以各种难懂的方式失败
for c in llm-relay llm-relay-postgres; do
  docker ps --format '{{.Names}}' | grep -qx "$c" || note "容器 $c 不在运行"
done
echo

echo "=== 服务 ==="
health=$(curl -s -m 5 http://127.0.0.1:8888/healthz)
echo "  $health"
# /healthz 的语义是「进程活着」，返回体不是 ok 就说明连进程都有问题
printf '%s' "$health" | grep -qi 'ok' || note "健康检查未返回 ok"
echo

echo "=== 数据规模 ==="
# 这里曾经查 model_pricings 与 channel_templates 两张表，
# 而它们已在 store.go 的迁移里 DROP TABLE IF EXISTS 掉了（定价并入 channel_models、
# 模板管理整体下线）。这条查询因此必然报 relation does not exist，
# 但因为脚本没有判失败，错误信息被淹没在输出里，末尾照样 FINAL_OK。
# 改为查真正存在的表。
counts=$(docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT '渠道 '||(SELECT count(*) FROM channels)||' | 对外模型 '||(SELECT count(DISTINCT public_name) FROM channel_models)||' | 密钥 '||(SELECT count(*) FROM api_keys)||' | 日志 '||(SELECT count(*) FROM request_logs)||' | 代理 '||(SELECT count(*) FROM proxies)" 2>&1)
if printf '%s' "$counts" | grep -q 'ERROR'; then
  echo "  $counts"
  note "数据规模查询失败"
else
  echo "  $counts"
fi
echo

echo "=== 已下线的表不应存在 ==="
# 迁移删掉的表如果还在，说明这条库没跑过新迁移 —— 值得单独看一眼
gone=$(docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('model_pricings','channel_templates','models','providers')" 2>&1)
if [ "$gone" = "0" ]; then
  echo "  0（符合预期）"
else
  echo "  $gone"
  note "有已下线的表仍存在（上方计数应为 0）"
fi
echo

echo "=== 绑定地址 ==="
docker port llm-relay
echo

echo "=== 主密钥 ==="
secret_n=$(docker exec llm-relay env | grep -c "^RELAY_SECRET=.")
echo "  RELAY_SECRET 已注入: $secret_n"
[ "$secret_n" -eq 1 ] || note "RELAY_SECRET 未注入或重复注入"
echo

echo "=== README 阶段 ==="
grep "^- \[x\] Phase" README.md | sed 's/^/  /'
echo

if [ "$fail" -eq 0 ]; then
  echo FINAL_OK
else
  echo "FINAL_FAILED（上面有 !! 标记的项未通过）"
fi
exit "$fail"
