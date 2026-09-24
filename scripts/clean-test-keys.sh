#!/usr/bin/env bash
# 清掉历史遗留的 regress 测试密钥。
set -uo pipefail

# 管理接口已上登录鉴权：自动登录并给后续 curl 注入会话 Cookie（鉴权关闭时静默跳过）
source "$(dirname "${BASH_SOURCE[0]}")/admin-auth.sh" && admin_auth_setup

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1
python3 - <<'PY'
import json, urllib.request
ADMIN = "http://127.0.0.1:8888/api/admin"
def call(m, p):
    r = urllib.request.Request(ADMIN + p, method=m)
    with urllib.request.urlopen(r, timeout=30) as x:
        return json.loads(x.read().decode() or "{}")
keys = call("GET", "/keys").get("items", [])
victims = [k for k in keys if k["name"] in ("regress", "regress-tmp")]
print("待清理 %d 把" % len(victims))
for k in victims:
    call("DELETE", "/keys/%d" % k["id"])
    print("  已删除 id=%d %s" % (k["id"], k["name"]))
left = call("GET", "/keys").get("items", [])
print("剩余 %d 把：%s" % (len(left), [k["name"] for k in left]))
PY
