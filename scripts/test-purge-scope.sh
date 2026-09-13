#!/usr/bin/env bash
# 清理规则的**作用范围**回归：只删测试产生的日志，绝不碰用户自己的调用。
#
# 为什么单独一个用例：清理脚本写错一个字符就会静默删掉真实数据，
# 而"删多了"这件事在跑完之后完全看不出来。实测踩过 ——
# 判定双下划线前缀时写了 LIKE '__%'，而下划线是 LIKE 的单字符通配符，
# 于是"__%"等价于"任意两个字符开头"，把用户全部真实日志一并删光，
# 连用户的密钥记录也被同类写法误伤。
set -uo pipefail

P() { docker exec -i llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c "$1"; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

pass=0; fail=0
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; pass=$((pass + 1));
  else echo "  [失败] $1 期望 $2 实际 $3"; fail=$((fail + 1)); fi
}

cleanup() {
  P "DELETE FROM request_logs WHERE trace_id LIKE 'purge-scope-%'" >/dev/null
}
trap cleanup EXIT
cleanup

echo "=== 造两条日志：一条像用户的，一条像测试的 ==="
P "INSERT INTO request_logs (trace_id, api_key_name, model_requested, status_code, is_stream, retry_count, created_at) VALUES
   ('purge-scope-real', 'DeepSeek', 'deepseek-v4.1-flash', 200, false, 0, now()),
   ('purge-scope-test', '__probe_key__', 'probe-model', 200, false, 0, now())" >/dev/null
chk "真实风格日志已插入" "1" "$(P "SELECT count(*) FROM request_logs WHERE trace_id='purge-scope-real'")"
chk "测试风格日志已插入" "1" "$(P "SELECT count(*) FROM request_logs WHERE trace_id='purge-scope-test'")"

echo
echo "=== 跑清理 ==="
bash "$ROOT/scripts/purge-test-logs.sh" | sed 's/^/  /'

echo
echo "=== 清理后：真实的必须还在，测试的必须没了 ==="
chk "用户风格日志保留" "1" "$(P "SELECT count(*) FROM request_logs WHERE trace_id='purge-scope-real'")"
chk "测试日志被清掉" "0" "$(P "SELECT count(*) FROM request_logs WHERE trace_id='purge-scope-test'")"

echo
if [ "$fail" -eq 0 ]; then echo "通过 $pass 项，失败 0 项"; echo ALL_PASS
else echo "通过 $pass 项，失败 $fail 项"; echo HAS_FAILURE; exit 1; fi
