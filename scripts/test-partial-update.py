#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证模板与密钥的局部更新：没传的字段不得被清掉。"""
import json
import urllib.request

BASE = "http://127.0.0.1:8888/api/admin"
ok = 0
bad = 0

def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")

def chk(name, want, got):
    global ok, bad
    if want == got:
        ok += 1
        print("  [通过] %-40s %s" % (name, got))
    else:
        bad += 1
        print("  [失败] %-40s 期望 %s 实际 %s" % (name, want, got))

# 模板管理整个功能已下线（菜单、接口、表都删了），
# 原本这里的「模板部分更新」用例随之删除；同样的语义由下面的密钥用例覆盖。
# 需要渠道维度的部分更新用例时可参照 test-payload.sh 的写法另加。

print()
print("=== 密钥：白名单必须能改 ===")
_, kl = call("GET", "/keys")
for k in kl.get("items", []):
    if k["name"] == "audit-key-partial":
        call("DELETE", "/keys/%d" % k["id"])
code, key = call("POST", "/keys", {"name": "audit-key-partial"})
chk("创建密钥", 200, code)
kid = key["id"]
# 创建接口返回的是含明文密钥的特殊结构，白名单要从列表接口读
_, kl0 = call("GET", "/keys")
row0 = [k for k in kl0["items"] if k["id"] == kid][0]
chk("初始白名单为空", [], row0["allowed_models"])

code, _ = call("PUT", "/keys/%d" % kid, {"allowed_models": ["deepseek-v4-flash", "gpt-5.6-sol"]})
chk("设置白名单", 200, code)
_, kl2 = call("GET", "/keys")
row = [k for k in kl2["items"] if k["id"] == kid][0]
chk("白名单已写入", ["deepseek-v4-flash", "gpt-5.6-sol"], row["allowed_models"])

code, _ = call("PUT", "/keys/%d" % kid, {"name": "audit-key-partial-2"})
chk("只改名字", 200, code)
_, kl3 = call("GET", "/keys")
row3 = [k for k in kl3["items"] if k["id"] == kid][0]
chk("名字已改", "audit-key-partial-2", row3["name"])
chk("白名单未被清掉", ["deepseek-v4-flash", "gpt-5.6-sol"], row3["allowed_models"])

code, _ = call("PUT", "/keys/%d" % kid, {"allowed_models": []})
chk("清空白名单", 200, code)
_, kl4 = call("GET", "/keys")
row4 = [k for k in kl4["items"] if k["id"] == kid][0]
chk("白名单已清空", [], row4["allowed_models"])

call("DELETE", "/keys/%d" % kid)

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
