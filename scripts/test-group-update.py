#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证分组编辑真正落库，以及前端页面可访问。

updateGroup 原来直接绑定实体再按「非零值」挑字段，布尔字段没有非零值可判，
于是 is_default / enabled 永远进不了 updates —— 传了返回 200 却不落库，
界面上开关拨过去又弹回来，看起来像「保存失败但没报错」。
"""
import json
import urllib.error
import urllib.request

import os
import subprocess

# 依赖 mock 上游（slow-upstream）：它会随 docker 网络重建被带走，
# 这里先确保它在跑，避免把「上游不在」误判成产品问题
subprocess.run(
    ["bash", os.path.join(os.path.dirname(os.path.abspath(__file__)), "ensure-mock-upstream.sh")],
    check=True,
)

ADMIN = "http://127.0.0.1:8888/api/admin"
BASE = "http://127.0.0.1:8888"
ok = 0
bad = 0


def call(method, path, body=None, base=ADMIN):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:
            return e.code, {}


def chk(name, want, got):
    global ok, bad
    if want == got:
        ok += 1
        print("  [通过] %-40s %s" % (name, got))
    else:
        bad += 1
        print("  [失败] %-40s 期望 %s 实际 %s" % (name, want, got))


print("=== 1. 前端页面可访问且指向新产物 ===")
try:
    with urllib.request.urlopen(BASE + "/", timeout=15) as r:
        html = r.read().decode("utf-8", "replace")
    chk("首页 HTTP", 200, r.status)
    chk("首页含入口脚本", "assets/index-" in html, True)
except Exception as e:
    chk("首页可访问", "可访问", str(e))

print()
print("=== 2. 分组编辑的布尔字段必须落库 ===")
_, gs = call("GET", "/groups")
g = [x for x in gs["items"] if x.get("is_default")][0]
gid = g["id"]
orig_strategy = g["strategy"]
print("  默认分组 id=%d 原策略=%s 原启用=%s" % (gid, orig_strategy, g["enabled"]))

code, _ = call("PUT", "/groups/%d" % gid, {"strategy": "round_robin", "enabled": False})
chk("更新请求被接受", 200, code)
_, gs2 = call("GET", "/groups")
row = [x for x in gs2["items"] if x["id"] == gid][0]
chk("strategy 已落库", "round_robin", row["strategy"])
chk("enabled=false 已落库（原来会被静默忽略）", False, row["enabled"])

# 显式传 true 也要能改回来 —— 只判断「非零值」的实现改不回 true 是不可能的，
# 但反过来只判断「传了没」的实现才两种情况都对
code, _ = call("PUT", "/groups/%d" % gid, {"strategy": orig_strategy, "enabled": True})
chk("改回请求被接受", 200, code)
_, gs3 = call("GET", "/groups")
row3 = [x for x in gs3["items"] if x["id"] == gid][0]
chk("strategy 已改回", orig_strategy, row3["strategy"])
chk("enabled=true 已落库", True, row3["enabled"])

print()
print("=== 3. 备注可以清空（原来空串被当成「没传」）===")
call("PUT", "/groups/%d" % gid, {"remark": "临时备注"})
_, gs4 = call("GET", "/groups")
row4 = [x for x in gs4["items"] if x["id"] == gid][0]
chk("备注已写入", "临时备注", row4["remark"])
call("PUT", "/groups/%d" % gid, {"remark": ""})
_, gs5 = call("GET", "/groups")
row5 = [x for x in gs5["items"] if x["id"] == gid][0]
chk("备注已清空", "", row5["remark"])

print()
print("=== 4. name 传空串应被拒（不能把分组改成无名）===")
code, _ = call("PUT", "/groups/%d" % gid, {"name": "   "})
chk("空名字被拒", 400, code)
_, gs6 = call("GET", "/groups")
chk("名字未被改坏", g["name"], [x for x in gs6["items"] if x["id"] == gid][0]["name"])

print()
print("=== 5. 默认分组必须唯一 ===")
_, ng = call("POST", "/groups", {"name": "默认分组唯一性测试"})
ngid = ng["id"]
call("PUT", "/groups/%d" % ngid, {"is_default": True})
_, gs7 = call("GET", "/groups")
defaults = [x for x in gs7["items"] if x.get("is_default")]
chk("设为默认后只有一个默认分组", 1, len(defaults))
chk("默认分组就是刚设的那个", ngid, defaults[0]["id"] if defaults else None)
# 恢复原状
call("PUT", "/groups/%d" % ngid, {"is_default": False})
call("PUT", "/groups/%d" % gid, {"is_default": True})
_, gs8 = call("GET", "/groups")
chk("恢复后仍只有一个默认分组", 1, len([x for x in gs8["items"] if x.get("is_default")]))
call("DELETE", "/groups/%d" % ngid)

print()
print("=== 6. 停用的分组不参与路由 ===")
MODEL = "group-enabled-test"
_, gb = call("POST", "/groups", {"name": "停用测试分组"})
gbid = gb["id"]
_, ch = call("POST", "/channels", {
    "name": "group-enabled-chan", "protocol": "openai-chat",
    "base_url": "http://slow-upstream:9999/v1", "api_key": "k",
    "group_id": gbid, "weight": 100,
})
call("POST", "/channels/%d/models" % ch["id"], {"public_name": MODEL, "upstream_name": MODEL})
_, k = call("POST", "/keys", {"name": "group-enabled-key"})
tok = k["key"]


def chat_model(model):
    payload = json.dumps({"model": model, "max_tokens": 8,
                          "messages": [{"role": "user", "content": "hi"}]}).encode()
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=payload, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + tok)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:
            return e.code, {}


code, _ = chat_model(MODEL)
chk("分组启用时可以调通", 200, code)
call("PUT", "/groups/%d" % gbid, {"enabled": False})
code, body = chat_model(MODEL)
chk("分组停用后调不通", 502, code)
msg = body.get("error", {}).get("message", "")
print("       说明: %s" % msg)
chk("报错点明是该模型没有可用渠道", MODEL in msg, True)
call("PUT", "/groups/%d" % gbid, {"enabled": True})
code, _ = chat_model(MODEL)
chk("重新启用后又能调通", 200, code)

# 清理
call("DELETE", "/keys/%d" % k["id"])
call("DELETE", "/channels/%d" % ch["id"])
_, ms = call("GET", "/models")
for m in ms.get("items", []):
    if m["public_name"] == MODEL:
        call("DELETE", "/models/%d" % m["id"])
call("DELETE", "/groups/%d" % gbid)

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
