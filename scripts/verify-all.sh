#!/usr/bin/env bash
# 一次性跑完所有验证脚本。
#
# 慢速上游的启动位置很关键：有的脚本（test-legacy-column-add.sh）内部会跑
# install.sh 重启服务，若上游先起、服务后重启，重建期间的用例会以
# 「上游不可达」失败。所以上游必须在**所有会触发部署的脚本跑完之后**再起，
# 否则依赖它的用例会以「上游不可达」失败，看起来像代码坏了。
#
# 本脚本自己也要能可靠地判失败，所以这里**不加 -e**（很多命令的「失败」
# 是被显式检查的，中途退出会漏跑后面的用例），但也绝不能把「没输出」
# 当成通过 —— 判据见下面 pass_or_fail 的说明。
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

fail=0

# pass_or_fail <用例名> <输出>
#
# 判据要求**出现明确的通过标志**（ALL_PASS），而不是「没看到 HAS_FAILURE」。
#
# 原来的写法是 grep -q HAS_FAILURE，只要输出里没有这行就算过。
# 但脚本在打印标志之前就崩掉时（语法错误、set -e 提前退出、curl 卡死被
# 上层超时杀掉、磁盘写满），输出里同样没有 HAS_FAILURE ——
# 于是一个**根本没跑完**的用例被算作通过。这是最坏的一类假阳性：
# 判据本身偏向了「看起来绿」，整套验证因此不可信。
#
# 现在改成「必须证明自己跑到了最后」：
#   · ALL_PASS  → 通过
#   · 其它任何情况（含 HAS_FAILURE、空输出、中止）→ 失败
pass_or_fail() {
  local name="$1" out="$2"
  if printf '%s\n' "$out" | grep -q ALL_PASS; then
    return 0
  fi
  fail=1
  # 把最后几行原样打出来：崩溃类失败的现场在末尾，
  # 只打印匹配「失败」的行在那种情况下什么都匹配不到
  echo "  !! $name 未报告 ALL_PASS，判为失败。输出末尾："
  printf '%s\n' "$out" | tail -15 | sed 's/^/     /'
  return 1
}

# 开跑前先清一次：上一次跑到一半中断时会留下测试日志
bash scripts/purge-test-logs.sh
echo

# 单元测试（含 race）先跑：它们快且不依赖部署形态。
# store 的 PG 集成测试由 LLMRELAY_TEST_DSN 门控（未设置自动跳过），
# 想全量跑就先 export 它再调本脚本。
echo "########## go test（含 race）##########"
if (cd backend && go test -race ./...); then
  echo "GO_TEST ALL_PASS"
else
  fail=1
  echo "  !! go test -race 存在失败"
fi
echo

# test-regression.sh 是本清单里唯一没有 ALL_PASS 标志的脚本：它以 DONE 收尾。
# 原先这里还挂着一个 test-foreign-keys.sh，但它已随 Docker 部署路径退役
# （fe2da29）被删除 —— 保留死引用会让计数行 grep 永远匹配不到、fail 恒为 1，
# 于是本脚本无论结果如何都以「有验证未通过」结束，退出码彻底失去意义。
s=test-regression.sh
echo "########## $s ##########"
out=$(bash "scripts/$s" 2>&1)
printf '%s\n' "$out" | tail -3
# 正常结束时最后一行是 DONE；四个入站协议的探测结果在它之前
printf '%s\n' "$out" | grep -q '^DONE$' || {
  fail=1
  echo "  !! $s 没有跑到结尾（缺少 DONE 标志）"
}
echo

echo "########## 启动慢速上游（必须在上面那些会重新部署的脚本之后）##########"
# 以宿主 node 进程跑（ensure 脚本自己管 pidfile 与就绪探测）
bash scripts/ensure-mock-upstream.sh || {
  echo "慢速上游未能启动，依赖它的用例无法验证"
  exit 1
}
# 服务应当能连到它：服务与上游同在本机回环，这里只做一次连通性确认。
# 失败只警告不判失败 —— 偶发的预热失败会以假警报淹没真正的用例结果；
# 真的连不到时，依赖它的用例（如 test-live.sh）自己会失败并给出原因。
RESOLVED=no
for i in $(seq 1 5); do
  if curl -fsS -m 3 http://127.0.0.1:9997/stats >/dev/null 2>&1; then
    RESOLVED=yes
    break
  fi
  sleep 2
done
if [ "$RESOLVED" != "yes" ]; then
  echo "警告：服务连不到 slow-upstream，依赖上游的用例结果不可信"
fi
echo "慢速上游就绪"
echo

# python 用例：统一以 ALL_PASS 作为通过标志
# （test-group-update.py / test-key-group-refs.py 已随分组功能移除；
#   test-key-whitelist.py 已改名 test-key-routes.py；
#   test-stats-filters.py 已改为 key_id 口径）
for s in test-key-routes.py test-delete-semantics.py \
         test-accept-encoding.py test-log-filters.py test-stats-filters.py test-csrf.py \
         test-retry-delay.py; do
  echo "########## $s ##########"
  out=$(python3 "scripts/$s" 2>&1)
  printf '%s\n' "$out" | tail -1
  pass_or_fail "$s" "$out"
  echo
done

# bash 用例：同样以 ALL_PASS 为唯一通过标志。
# 依赖 mock 上游的用例自己会拉起它（test-pricing-rules.sh → slow-upstream，
# test-cost.sh → proto-upstream，test-retry-delay.py → flaky-upstream）。
#
# test-group-quota.sh 已随分组功能移除；test-default-model.sh 不在名单、
# 以退出码收尾（要真调 mock 上游），手动 `bash scripts/test-default-model.sh` 跑。
# test-legacy-column-add.sh 会删价格列并
# 跑 install.sh 重新部署（几分钟不可用），放在最后跑
# verify-update-config.sh 只读写更新设置（会临时存一个假 token 再清除），
# 不碰数据、不重启服务，所以位置不敏感
for s in test-model-whitelist.sh test-upstream-protocol.sh test-upstream-gemini.sh test-thinking-map.sh test-pricing.sh test-pricing-rules.sh test-pricing-backup.sh test-pricing-filter.sh \
         test-proxies.sh test-egress-proxy.sh test-live.sh test-purge-scope.sh test-cost.sh test-legacy-column-add.sh \
         verify-update-config.sh; do
  echo "########## $s ##########"
  out=$(bash "scripts/$s" 2>&1)
  printf '%s\n' "$out" | tail -1
  pass_or_fail "$s" "$out"
  echo
done

# 收尾清理：测试请求会污染看板的今日统计（实测跑一轮会多出上千条），
# 必须在这一轮结束时擦干净，而不是留给用户
echo "########## 清理测试日志 ##########"
bash scripts/purge-test-logs.sh
echo

if [ "$fail" -eq 0 ]; then echo "全部验证通过"; else echo "有验证未通过"; fi
exit "$fail"
