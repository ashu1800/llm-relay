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

print("=== 模板：只改名字，其它字段必须原样保留 ===")
_, gs = call("GET", "/groups")
gid = gs["items"][0]["id"]
name = "audit-tpl-partial"
# 先清掉可能的残留
_, lst = call("GET", "/channel-templates")
for t in lst.get("items", []):
    if t["name"] == name:
        call("DELETE", "/channel-templates/%d" % t["id"])

code, tpl = call("POST", "/channel-templates", {
    "name": name, "protocol": "custom", "base_url": "https://example.test/v1",
    "group_id": gid,
    "extra_config": {"headers": {"X-Custom": "1"}, "max_concurrency": 3},
    "custom_mapping": {"auth_header": "X-Key", "auth_prefix": ""},
})
chk("创建模板", 200, code)
tid = tpl["id"]
print("    group_id=%s extra_config=%s custom_mapping=%s" % (
    tpl["group_id"], tpl["extra_config"], tpl["custom_mapping"]))

code, _ = call("PUT", "/channel-templates/%d" % tid, {"name": name + "-renamed"})
chk("只改名字", 200, code)
_, after = call("GET", "/channel-templates")
row = [t for t in after["items"] if t["id"] == tid][0]
chk("名字已改", name + "-renamed", row["name"])
chk("group_id 未被清成 0", gid, row["group_id"])
chk("extra_config 未丢", {"headers": {"X-Custom": "1"}, "max_concurrency": 3}, row["extra_config"])
chk("custom_mapping 未丢", {"auth_header": "X-Key", "auth_prefix": ""}, row["custom_mapping"])

print()
print("=== 模板：显式传空对象应当能清空（区分没传与传空）===")
code, _ = call("PUT", "/channel-templates/%d" % tid, {"extra_config": {}})
chk("显式清空 extra_config", 200, code)
_, after2 = call("GET", "/channel-templates")
row2 = [t for t in after2["items"] if t["id"] == tid][0]
chk("extra_config 已清空", {}, row2["extra_config"])
chk("custom_mapping 仍未受影响", {"auth_header": "X-Key", "auth_prefix": ""}, row2["custom_mapping"])

print()
print("=== 模板：空载荷应被拒 ===")
code, _ = call("PUT", "/channel-templates/%d" % tid, {})
chk("空载荷", 400, code)
call("DELETE", "/channel-templates/%d" % tid)

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
