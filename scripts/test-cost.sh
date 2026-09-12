#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888"
SK=$(curl -s "$BASE/api/admin/keys" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['items'][0]['name'])" 2>/dev/null)
echo "使用密钥名: $SK"

# 模型名从渠道白名单现取：写死的话，用户一改白名单这个用例就变成
# 「请求 502、费用恒为 0」，看起来像计价坏了，其实只是模型没配
MODEL=$(docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A -c \
  "SELECT m.public_name FROM channel_models m
     JOIN channels c ON c.id = m.channel_id AND c.enabled = true
     JOIN channel_groups g ON g.id = c.group_id AND g.enabled = true
    WHERE m.enabled = true ORDER BY m.id LIMIT 1" | tr -d '[:space:]')
if [ -z "$MODEL" ]; then
  echo "渠道白名单里没有启用的模型，本用例无法验证"
  echo DONE
  exit 0
fi
echo "探针模型: $MODEL"

echo
echo "===== 1. 查 $MODEL 是否可解析 ====="
curl -s -X POST "$BASE/api/admin/pricing/resolve" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\"}" | python3 -c "
import sys,json;d=json.load(sys.stdin)
print('  可解析:', d.get('found'))
if d.get('found'):
    s=d['snapshot']; print('  输入 %s 输出 %s 缓存读 %s 倍率 %s' % (s['input_per_1m'],s['output_per_1m'],s['cache_read_per_1m'],s.get('multiplier')))
"
echo
echo "===== 2. 发一次真实请求 ====="
# 从数据库取回刚创建的密钥明文不可行（只存哈希），改用重新创建的密钥
NEWKEY=$(curl -s -X POST "$BASE/api/admin/keys" -H 'Content-Type: application/json' -d '{"name":"pricing-check"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")
curl -s -m 90 "$BASE/v1/chat/completions" -H "Authorization: Bearer $NEWKEY" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"只回复:OK\"}],\"max_tokens\":20}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin);u=d.get('usage',{});print('  上游返回用量: 输入',u.get('prompt_tokens'),'输出',u.get('completion_tokens'))"

echo
echo "===== 3. 检查日志里的费用与定价快照 ====="
sleep 2
curl -s "$BASE/api/admin/logs?page_size=1" | python3 -c "
import sys,json
it=json.load(sys.stdin)['items'][0]
print('  模型:', it['model_requested'])
print('  用量: 输入 %s / 输出 %s / 缓存命中 %s' % (it['prompt_tokens'], it['completion_tokens'], it['cached_tokens']))
print('  预估费用: \$%s' % it['estimated_cost'])
snap = it.get('pricing_snapshot') or {}
if snap:
    print('  定价快照:')
    for k in ['model_key','currency','input_per_1m','output_per_1m','cache_read_per_1m','multiplier','multiplier_source','fixed_multiplier','peak_applied','peak_label','resolved_at']:
        if k in snap: print('    %-18s %s' % (k, snap[k]))
else:
    print('  定价快照: 空（该模型无定价配置）')
"
echo
echo "===== 4. 手工核验一遍算数 ====="
python3 - "$MODEL" <<'PY'
import json,sys,urllib.request
model=sys.argv[1]
req=urllib.request.Request('http://127.0.0.1:8888/api/admin/logs?page_size=20')
items=json.load(urllib.request.urlopen(req))['items']
it=next((x for x in items if x.get('model_requested')==model), items[0])
s=it.get('pricing_snapshot') or {}
if s:
    inp=float(s['input_per_1m']); out=float(s['output_per_1m']); cr=float(s.get('cache_read_per_1m',0))
    cost=(inp*it['prompt_tokens'] + out*it['completion_tokens'] + cr*it['cached_tokens'])/1_000_000
    print('  手算: (%.4f*%d + %.4f*%d + %.4f*%d)/1e6 = %.10f' % (
        inp,it['prompt_tokens'],out,it['completion_tokens'],cr,it['cached_tokens'],cost))
    print('  库中: %.10f' % float(it['estimated_cost']))
    print('  一致:', abs(cost-float(it['estimated_cost'])) < 1e-12)
PY

# 探测密钥用完就删：留着会在「密钥信息」页里越堆越多，
# 而且它是一把能调通上游的真实密钥
echo
echo "===== 5. 清理探测密钥 ====="
for kid in $(docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A \
    -c "SELECT id FROM api_keys WHERE name='pricing-check'"); do
  curl -s -X DELETE "$BASE/api/admin/keys/$kid" >/dev/null
done
LEFT=$(docker exec llm-relay-postgres psql -U llmrelay -d llm_relay -t -A \
    -c "SELECT count(*) FROM api_keys WHERE name='pricing-check'")
echo "  剩余同名额密钥: $LEFT"
echo "DONE"
