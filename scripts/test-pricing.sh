#!/usr/bin/env bash
# 定价随渠道模型走（旧的「模型定价」表与 /api/admin/pricing 已下线）的回归。
#
# 为什么这么断言：价格从「模型名 → 单价」的独立表搬到了 channel_models 上，
# 归属变成「渠道 × 对外模型名」，写价的入口只剩渠道接口。所以这里验证的是一条
# 完整回路：建渠道时带上带价的白名单 → 列表能读回 → 改价能改到 →
# 旧定价接口确实全部 404（留着任何一个都会变成两处真相）。
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib/testdb.sh"

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888"
API="$BASE/api/admin"
P() { db_psql -t -A -c "$1"; }
jqg() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

ok=0; bad=0
# chk <说明> <期望> <实际>
chk() {
  if [ "$2" = "$3" ]; then echo "  [通过] $1 = $3"; ok=$((ok+1))
  else echo "  [失败] $1 期望 $2 实际 $3"; bad=$((bad+1)); fi
}

# 测试数据一律 __ 前缀，清理时按**精确名字**删。
# 绝不能用 LIKE '__%'：LIKE 里的下划线是「任意单个字符」的通配符，
# '__%' 实际等于「任意两个字符开头」，会把用户自己的密钥与日志一起删掉。
GNAME="__price-probe-group"
CNAME="__price-probe-ch"
MODEL="__price-probe-model"
MODEL2="__price-probe-model2"

purge() {
  for cid in $(P "SELECT id FROM channels WHERE name='$CNAME'"); do
    curl -s -X DELETE "$API/channels/$cid" >/dev/null
  done
  P "DELETE FROM channel_groups WHERE name='$GNAME'" >/dev/null
  P "DELETE FROM request_payloads WHERE log_id IN (SELECT id FROM request_logs WHERE model_requested='$MODEL' OR model_requested='$MODEL2')" >/dev/null
  P "DELETE FROM request_logs WHERE model_requested='$MODEL' OR model_requested='$MODEL2'" >/dev/null
}

# 单价按十进制字符串读回：numeric(18,8) 落库会把 "0.15" 变成 0.15000000，
# 直接比字符串会一直在「格式」上失败，所以统一 normalize 成最短形式再比。
prices() {
  curl -s "$API/channels/$1/models" | python3 -c "
import sys, json
from decimal import Decimal
rows = json.load(sys.stdin).get('items') or []
r = next((x for x in rows if x['public_name'] == '$2'), None)
if r is None:
    print('NONE'); raise SystemExit
d = lambda v: str(Decimal(str(v)).normalize()) if v not in (None, '') else '0'
print('%s|%s|%s|%s|%s|%s|%s' % (
    d(r['input_per_1m']), d(r['output_per_1m']), d(r['cache_read_per_1m']),
    d(r['cache_write_per_1m']), '%g' % float(r['multiplier']),
    len(r.get('peak_rules') or []), r['upstream_name']))
"
}

# 列表里的模型数与未配价数：渠道列表是「这条渠道配齐了没有」的唯一提示处
counts() {
  curl -s "$API/channels" | python3 -c "
import sys, json
d = json.load(sys.stdin)
it = next((x for x in d['items'] if x['id'] == $1), None)
print('NONE' if it is None else '%s|%s|%s' % (it['model_count'], it['unpriced_count'], ','.join(sorted(it['models']))))
"
}

purge

echo "=== 1. 建临时渠道：白名单与价格一次提交 ==="
GID=$(curl -s -X POST "$API/groups" -H 'Content-Type: application/json' -d "{\"name\":\"$GNAME\"}" | jqg "d['id']")
CH=$(curl -s -X POST "$API/channels" -H 'Content-Type: application/json' -d "{
  \"name\": \"$CNAME\",
  \"group_id\": $GID,
  \"protocol\": \"openai-chat\",
  \"base_url\": \"http://127.0.0.1:1\",
  \"api_key\": \"probe-key\",
  \"weight\": 1,
  \"models\": [{
    \"public_name\": \"$MODEL\",
    \"upstream_name\": \"$MODEL\",
    \"input_per_1m\": \"0.15\",
    \"output_per_1m\": \"0.6\",
    \"cache_read_per_1m\": \"0.003\",
    \"cache_write_per_1m\": \"0\",
    \"multiplier\": 1
  }]
}")
CID=$(echo "$CH" | jqg "d['id']")
echo "  渠道 id=$CID 分组=$GID"
case "$CID" in ''|*[!0-9]*) chk "渠道创建成功" "拿到数字 id" "$CH" ;; *) chk "渠道创建成功" "yes" "yes" ;; esac
# 价格是随白名单一起写进去的：分两步写会出现「模型加进去了、价格没保存」的半截状态
chk "建渠道时就带上了四个单价与倍率" "0.15|0.6|0.003|0|1|0|$MODEL" "$(prices "$CID" "$MODEL")"

echo
echo "=== 2. 渠道列表能读回价格归属（模型数 / 未配价数）==="
chk "列表里这条渠道：1 个模型、0 个未配价" "1|0|$MODEL" "$(counts "$CID")"

echo
echo "=== 3. 改价：PUT /channels/:id 整表替换白名单 ==="
# 白名单是整表提交的（与界面里的表格一致），所以改价也走整表，
# 不存在「改了一条、另一条被漏掉」的中间状态
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' -d "{
  \"models\": [{
    \"public_name\": \"$MODEL\",
    \"upstream_name\": \"$MODEL\",
    \"input_per_1m\": \"1.25\",
    \"output_per_1m\": \"2.5\",
    \"cache_read_per_1m\": \"0.125\",
    \"cache_write_per_1m\": \"1.25\",
    \"multiplier\": 1.5,
    \"peak_rules\": [{\"days\": [1, 2, 3], \"start\": \"09:00\", \"end\": \"12:00\", \"multiplier\": 2, \"label\": \"上午高峰\"}]
  }]
}")
chk "改价返回 200" "200" "$CODE"
chk "新价与时段规则都读得回来" "1.25|2.5|0.125|1.25|1.5|1|$MODEL" "$(prices "$CID" "$MODEL")"
# 固定倍率不填 = 原价 1（不是 0）：0 的语义被代码归一成 1，
# 这条守住的是「忘了填倍率不会把价格算成 0 元」
curl -s -X PUT "$API/channels/$CID" -H 'Content-Type: application/json' \
  -d "{\"models\":[{\"public_name\":\"$MODEL\",\"input_per_1m\":\"1.25\",\"output_per_1m\":\"2.5\",\"multiplier\":0}]}" >/dev/null
chk "倍率填 0 被归一成 1（0 不等于免费）" "1.25|2.5|0|0|1|0|$MODEL" "$(prices "$CID" "$MODEL")"

echo
echo "=== 4. 加一条绑定：POST /channels/:id/models ==="
curl -s -X POST "$API/channels/$CID/models" -H 'Content-Type: application/json' \
  -d "{\"public_name\":\"$MODEL2\",\"upstream_name\":\"$MODEL2\",\"input_per_1m\":\"3\",\"output_per_1m\":\"6\"}" >/dev/null
chk "新绑定落库且带价" "3|6|0|0|1|0|$MODEL2" "$(prices "$CID" "$MODEL2")"
# 新绑定是带价加进去的，所以未配价数保持 0；「未配价」本身由
# test-pricing-filter.sh 专门验，这里只确认模型数变了
chk "列表变成 2 个模型、0 个未配价" "2|0|$MODEL,$MODEL2" "$(counts "$CID")"
# 对已存在的绑定再 POST 只改上游名/启用状态，刻意不动价格：
# 载荷里不传价格时是空字符串，按「空即 0」的语义会把用户配好的价悄悄清零
curl -s -X POST "$API/channels/$CID/models" -H 'Content-Type: application/json' \
  -d "{\"public_name\":\"$MODEL\",\"upstream_name\":\"upstream-renamed\",\"input_per_1m\":\"99\"}" >/dev/null
chk "重复绑定只改上游名、不覆盖已有价格" "1.25|2.5|0|0|1|0|upstream-renamed" "$(prices "$CID" "$MODEL")"

echo
echo "=== 5. 旧定价接口必须全部 404（否则会出现两处真相）==="
chk "GET /api/admin/pricing" "404" "$(curl -s -o /dev/null -w '%{http_code}' "$API/pricing")"
chk "POST /api/admin/pricing" "404" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/pricing" -H 'Content-Type: application/json' -d '{}')"
chk "PUT /api/admin/pricing/1" "404" "$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$API/pricing/1" -H 'Content-Type: application/json' -d '{}')"
chk "DELETE /api/admin/pricing/1" "404" "$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$API/pricing/1")"
chk "POST /api/admin/pricing/resolve" "404" "$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/pricing/resolve" -H 'Content-Type: application/json' -d '{"model":"x"}')"
# 渠道接口还在（别把有用的路由一起删掉了）
chk "渠道接口仍可用" "200" "$(curl -s -o /dev/null -w '%{http_code}' "$API/channels")"

echo
echo "=== 6. 清理探测数据 ==="
curl -s -X DELETE "$API/channels/$CID" >/dev/null
purge
chk "探测渠道已删除" "0" "$(P "SELECT count(*) FROM channels WHERE name='$CNAME'")"
chk "探测分组已删除" "0" "$(P "SELECT count(*) FROM channel_groups WHERE name='$GNAME'")"
# 白名单随渠道一起删（没有归属的绑定行会让「这个模型还存在吗」变得说不清）
chk "探测白名单已删除" "0" "$(P "SELECT count(*) FROM channel_models WHERE public_name IN ('$MODEL','$MODEL2')")"

echo
echo "通过 $ok 项，失败 $bad 项"
[ "$bad" -eq 0 ] && echo "ALL_PASS" || { echo "HAS_FAILURE"; exit 1; }
