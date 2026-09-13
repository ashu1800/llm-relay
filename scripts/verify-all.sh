#!/usr/bin/env bash
# 一次性跑完所有验证脚本。
#
# 慢速上游的启动位置很关键：有的脚本（test-foreign-keys.sh）内部会跑 install.sh，
# 而 install.sh 重建 compose 项目时会把挂在同一网络上的临时容器一并清掉。
# 所以上游必须在**所有会触发部署的脚本跑完之后**再起，
# 否则依赖它的用例会以「上游不可达」失败，看起来像代码坏了。
set -uo pipefail
cd "/path/to/llm-relay" || exit 1

fail=0

# 开跑前先清一次：上一次跑到一半中断时会留下测试日志
bash scripts/purge-test-logs.sh
echo

for s in test-regression.sh test-foreign-keys.sh; do
  echo "########## $s ##########"
  bash "scripts/$s" 2>&1 | tail -3 || fail=1
  echo
done

echo "########## 启动慢速上游（必须在上面那些会重新部署的脚本之后）##########"
docker rm -f slow-upstream >/dev/null 2>&1
docker run -d --name slow-upstream --network llm-relay_relay \
  -p 127.0.0.1:9997:9999 \
  -v "$PWD/scripts/slow-upstream.js:/app/server.js:ro" \
  node:22-alpine node /app/server.js >/dev/null
for i in $(seq 1 30); do
  curl -fsS -m 2 http://127.0.0.1:9997/stats >/dev/null 2>&1 && break
  sleep 1
done
if ! curl -fsS -m 3 http://127.0.0.1:9997/stats >/dev/null 2>&1; then
  echo "慢速上游未能启动，依赖它的用例无法验证"
  exit 1
fi
# 中继容器应当能按名字解析到它，否则失败原因会与用例本身无关。
#
# 这里只警告、不再直接判失败：容器刚起来时 Docker 的内嵌 DNS 还在预热，
# 实测会在一切正常的情况下偶发解析失败，于是整套验证报「有验证未通过」，
# 而每个用例其实都是 ALL_PASS —— 那种假警报会让人开始忽略这个总判据。
# 真的解析不到时，依赖它的用例（如 test-live.sh）自己会失败并给出原因。
RESOLVED=no
for i in $(seq 1 5); do
  if docker exec llm-relay sh -c 'wget -qO- -T 3 http://slow-upstream:9999/stats' >/dev/null 2>&1; then
    RESOLVED=yes
    break
  fi
  sleep 2
done
if [ "$RESOLVED" != "yes" ]; then
  echo "警告：中继容器解析不到 slow-upstream，依赖上游的用例结果不可信"
fi
echo "慢速上游就绪"
echo

# python 用例：统一以 ALL_PASS 作为通过标志
for s in test-group-update.py test-key-whitelist.py test-delete-semantics.py \
         test-accept-encoding.py test-log-filters.py test-csrf.py; do
  echo "########## $s ##########"
  out=$(python3 "scripts/$s" 2>&1)
  echo "$out" | tail -1
  echo "$out" | grep -q ALL_PASS || { fail=1; echo "$out" | grep "失败\]" | head -5; }
  echo
done
# bash 用例：没有统一的通过标志，按「没有 HAS_FAILURE」判定
# 依赖 mock 上游的用例自己会拉起它（test-pricing-rules.sh → slow-upstream，
# test-cost.sh → proto-upstream），所以上面那次网络重建不会让它们变 502。
# test-legacy-column-add.sh 会删价格列并跑 install.sh 重新部署（几分钟不可用），放在最后跑
for s in test-model-whitelist.sh test-upstream-protocol.sh test-upstream-gemini.sh test-pricing.sh test-pricing-rules.sh test-pricing-backup.sh test-pricing-filter.sh \
         test-group-quota.sh test-proxies.sh test-egress-proxy.sh test-live.sh test-purge-scope.sh test-cost.sh test-legacy-column-add.sh; do
  echo "########## $s ##########"
  out=$(bash "scripts/$s" 2>&1)
  echo "$out" | tail -1
  if echo "$out" | grep -q HAS_FAILURE; then
    fail=1
    echo "$out" | grep -E "失败" | head -5
  fi
  echo
done

# 收尾清理：测试请求会污染看板的今日统计（实测跑一轮会多出上千条），
# 必须在这一轮结束时擦干净，而不是留给用户
echo "########## 清理测试日志 ##########"
bash scripts/purge-test-logs.sh
echo

if [ "$fail" -eq 0 ]; then echo "全部验证通过"; else echo "有验证未通过"; fi
exit "$fail"
