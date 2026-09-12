#!/usr/bin/env bash
set -uo pipefail
BASE="http://127.0.0.1:8888/api/admin"

echo "=== 1. 内置模板 ==="
curl -s -m 10 "$BASE/channel-templates" -o /tmp/t1.json -w "  HTTP %{http_code}\n"
python3 - <<'PY'
import json
d = json.load(open('/tmp/t1.json'))
print('  共 %d 个' % d['total'])
for t in d['items']:
    print('    %-22s %-24s %s' % (t['name'], t['protocol'], t['base_url']))
PY

echo
echo "=== 2. 新建自定义模板 ==="
NEW=$(curl -s -X POST "$BASE/channel-templates" -H 'Content-Type: application/json' \
  -d '{"name":"测试中转站","protocol":"openai-chat","base_url":"https://example.com/v1/"}' -w "\n%{http_code}")
CODE=$(echo "$NEW" | tail -1); BODY=$(echo "$NEW" | head -n -1)
echo "  HTTP $CODE"
TID=$(echo "$BODY" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
echo "  已创建 id=$TID，base_url 末尾斜杠应被去掉："
echo "$BODY" | python3 -c "import sys,json;print('   ', json.load(sys.stdin)['base_url'])"

echo
echo "=== 3. 重名应 409 ==="
curl -s -o /tmp/t2.json -w "  HTTP %{http_code}\n" -X POST "$BASE/channel-templates" \
  -H 'Content-Type: application/json' -d '{"name":"测试中转站","protocol":"openai-chat"}'
python3 -c "import json;print('  ', json.load(open('/tmp/t2.json'))['error']['message'])"

echo
echo "=== 4. 用模板建渠道（缺密钥应 400）==="
curl -s -o /tmp/t3.json -w "  HTTP %{http_code}\n" -X POST "$BASE/channel-templates/$TID/apply" \
  -H 'Content-Type: application/json' -d '{"name":"模板块测试渠道"}'
python3 -c "import json;print('  ', json.load(open('/tmp/t3.json'))['error']['message'])"

echo
echo "=== 5. 用模板建渠道（正常）==="
curl -s -o /tmp/t4.json -w "  HTTP %{http_code}\n" -X POST "$BASE/channel-templates/$TID/apply" \
  -H 'Content-Type: application/json' \
  -d '{"name":"模板块测试渠道","api_key":"sk-test-not-real","base_url":"","group_id":0,"weight":3}'
python3 - <<'PY'
import json
d = json.load(open('/tmp/t4.json'))
if 'error' in d:
    print('  错误:', d['error']['message'])
else:
    ch = d['channel']
    print('  渠道: %s | 协议 %s | 地址 %s | 权重 %s | 密钥掩码 %s' % (
        ch['name'], ch['protocol'], ch['base_url'], ch['weight'], ch['api_key_hint']))
    print('  模板默认值是否带入: base_url=%s' % ('是' if ch['base_url']=='https://example.com/v1' else '否'))
PY
CHID=$(python3 -c "import json;d=json.load(open('/tmp/t4.json'));print(d.get('channel',{}).get('id',''))")

echo
echo "=== 6. 同名渠道应 409 ==="
curl -s -o /tmp/t5.json -w "  HTTP %{http_code}\n" -X POST "$BASE/channel-templates/$TID/apply" \
  -H 'Content-Type: application/json' -d '{"name":"模板块测试渠道","api_key":"sk-x"}'
python3 -c "import json;print('  ', json.load(open('/tmp/t5.json'))['error']['message'])"

echo
echo "=== 7. 编辑与删除模板 ==="
curl -s -o /dev/null -w "  PUT  HTTP %{http_code}\n" -X PUT "$BASE/channel-templates/$TID" \
  -H 'Content-Type: application/json' -d '{"name":"测试中转站改","protocol":"openai-chat","base_url":"https://example.org/v1"}'
curl -s -o /dev/null -w "  DEL  HTTP %{http_code}\n" -X DELETE "$BASE/channel-templates/$TID"

echo
echo "=== 8. 清理测试渠道 ==="
if [ -n "$CHID" ]; then
  curl -s -o /dev/null -w "  DEL 渠道 HTTP %{http_code}\n" -X DELETE "$BASE/channels/$CHID"
fi
echo "  剩余模板数: $(curl -s "$BASE/channel-templates" | python3 -c "import sys,json;print(json.load(sys.stdin)['total'])")"
echo "  剩余渠道数: $(curl -s "$BASE/channels" | python3 -c "import sys,json;print(json.load(sys.stdin)['total'])")"
echo DONE
