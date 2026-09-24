#!/usr/bin/env bash
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

BASE="http://127.0.0.1:8888/api/admin"
OUT=/tmp/backup.json

echo "=== 1. 导出配置 ==="
curl -s -m 20 "$BASE/backup/export" -o "$OUT" -w "  HTTP %{http_code}  大小 %{size_download} 字节\n"
python3 - <<'PY'
import json
b = json.load(open('/tmp/backup.json'))
print('  版本:', b['version'], '| 应用:', b['app'])
print('  主密钥指纹:', b['secret_fingerprint'])
for k in ['channel_groups','channels','channel_models',
          'api_keys','pricings']:
    print('    %-18s %d' % (k, len(b.get(k) or [])))
PY

echo
echo "=== 2. 安全性：备份里不得出现任何明文密钥 ==="
python3 - <<'PY'
import json, re
raw = open('/tmp/backup.json').read()
b = json.loads(raw)
# 任何长度像真实密钥的串都不该出现
leaks = re.findall(r'sk-[A-Za-z0-9_-]{20,}', raw)
print('  形似完整密钥的串:', len(leaks), leaks[:3] if leaks else '(无)')
# 渠道密钥必须是密文
for ch in b['channels']:
    enc = ch.get('api_key_enc') or ''
    print('    渠道 %-14s 密文长度 %-4d 掩码 %s' % (ch['name'], len(enc), ch.get('api_key_hint')))
# 密钥表只应有哈希
for k in b['api_keys']:
    print('    密钥 %-18s hash长度 %d' % (k['name'], len(k.get('key_hash') or '')))
PY

echo
echo "=== 3. 原样重新导入：应全部跳过，不产生重复 ==="
curl -s -X POST "$BASE/backup/import" -H 'Content-Type: application/json' \
  --data-binary @"$OUT" -o /tmp/imp1.json -w "  HTTP %{http_code}\n"
python3 - <<'PY'
import json
d = json.load(open('/tmp/imp1.json'))
print('  新增合计:', sum(d['created'].values()), d['created'])
print('  跳过合计:', sum(d['skipped'].values()))
print('  告警:', d['warnings'] or '(无)')
PY
echo "  渠道数: $(curl -s "$BASE/channels" | python3 -c "import sys,json;print(json.load(sys.stdin)['total'])")"

echo
echo "=== 4. 备份里不应再有已下线功能的残留 ==="
python3 -c "
import json
b = json.load(open('/tmp/backup.json'))
gone = [k for k in ('channel_templates',) if k in b]
print('  已下线字段仍在备份里:', gone or '(无)')
"

echo
echo "=== 5. 非本应用的文件应被拒绝 ==="
curl -s -o /tmp/imp3.json -w "  HTTP %{http_code}\n" -X POST "$BASE/backup/import" \
  -H 'Content-Type: application/json' -d '{"app":"other","version":1}'
python3 -c "import json;print('  ', json.load(open('/tmp/imp3.json'))['error']['message'])"
echo DONE
