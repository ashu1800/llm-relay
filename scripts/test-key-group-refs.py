#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证密钥分组白名单的强一致化改造（2026-09-21）。

覆盖四件事：
  1. 创建密钥时白名单里的分组名被归一化成 ID 存库（改名不再打断密钥）
  2. 引用不存在的分组 ID 在创建瞬间被拒（400），而不是调用时才 403
  3. /v1/models 按密钥的模型白名单过滤（客户端探测不再看到调不了的模型）
  4. 删除分组前检查密钥引用：仍被引用时 409 并点名密钥；清掉引用后可删

依赖 mock 上游（slow-upstream）作为渠道目标，只求渠道可建、路由能命中，
不需要上游真的回话 —— 本用例不发聊天请求，全部走管理接口与 /v1/models。
"""
import json
import subprocess
import urllib.error
import urllib.request

import os

subprocess.run(
    ["bash", os.path.join(os.path.dirname(os.path.abspath(__file__)), "ensure-mock-upstream.sh")],
    check=True,
)

ADMIN = "http://127.0.0.1:8888/api/admin"
BASE = "http://127.0.0.1:8888"
MODEL = "group-ref-test-model"
GROUP_X = "分组X-引用测试"
CHAN_X = "group-ref-channel"
KEY_NAME = "k-groupref"

ok = 0
bad = 0


def call(method, path, body=None, base=ADMIN, token=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
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
        print("  [通过] %-46s %s" % (name, got))
    else:
        bad += 1
        print("  [失败] %-46s 期望 %s 实际 %s" % (name, want, got))


def cleanup():
    _, ks = call("GET", "/keys")
    for k in ks.get("items", []):
        if k["name"].startswith(KEY_NAME):
            call("DELETE", "/keys/%d" % k["id"])
    _, cs = call("GET", "/channels")
    for c in cs.get("items", []):
        if c["name"] == CHAN_X:
            call("DELETE", "/channels/%d" % c["id"])
    _, gs = call("GET", "/groups")
    for g in gs.get("items", []):
        if g["name"] == GROUP_X:
            call("DELETE", "/groups/%d" % g["id"])


cleanup()

print("=== 准备：一个分组、一个渠道、一个模型 ===")
_, gx = call("POST", "/groups", {"name": GROUP_X})
gx_id = gx["id"]
_, cx = call("POST", "/channels", {
    "name": CHAN_X, "protocol": "openai-chat",
    "base_url": "http://127.0.0.1:9997/v1", "api_key": "k",
    "group_id": gx_id,
})
call("POST", "/channels/%d/models" % cx["id"],
     {"public_name": MODEL, "upstream_name": MODEL})
# 再绑第二个模型，给 /v1/models 的过滤用（一个在白名单内、一个在外）
call("POST", "/channels/%d/models" % cx["id"],
     {"public_name": MODEL + "-other", "upstream_name": MODEL + "-other"})

print()
print("=== 1. 白名单里的分组名归一化成 ID ===")
_, k = call("POST", "/keys", {"name": KEY_NAME, "allowed_groups": [GROUP_X]})
chk("用分组名创建密钥成功", 200, _)
key_id = k["id"]
token = k["key"]
_, ks = call("GET", "/keys")
row = [x for x in ks["items"] if x["id"] == key_id][0]
chk("库里存的是分组 ID", [str(gx_id)], row["allowed_groups"])

print()
print("=== 2. 不存在的分组 ID 在创建时被拒 ===")
code, body = call("POST", "/keys", {"name": KEY_NAME + "-bad", "allowed_groups": ["99999"]})
chk("不存在的 ID 创建被拒", 400, code)
print("       说明: %s" % body.get("error", {}).get("message", body))

print()
print("=== 3. /v1/models 按密钥模型白名单过滤 ===")
# 把这把密钥的模型白名单限成 MODEL：列表必须只剩它
_, _ = call("PUT", "/keys/%d" % key_id, {"allowed_models": [MODEL]})
code, models = call("GET", "/v1/models", base=BASE, token=token)
names = sorted(m["id"] for m in models.get("data", []))
chk("列表只含白名单内的模型", [MODEL], [n for n in names if n.startswith(MODEL)])
# 对照：不带白名单时两个测试模型都在
_, k2 = call("POST", "/keys", {"name": KEY_NAME + "-open"})
code, models2 = call("GET", "/v1/models", base=BASE, token=k2["key"])
names2 = sorted(m["id"] for m in models2.get("data", []))
got2 = [n for n in names2 if n.startswith(MODEL)]
chk("空白名单时两个模型都可见", sorted([MODEL, MODEL + "-other"]), got2)

print()
print("=== 4. 删除分组前的密钥引用检查 ===")
# 渠道占用是更早的一道守卫：先把渠道删掉让路，这次 409 才能归因于密钥引用
call("DELETE", "/channels/%d" % cx["id"])
code, body = call("DELETE", "/groups/%d" % gx_id)
chk("仍被密钥引用时删除被拒", 409, code)
msg = body.get("error", {}).get("message", "")
chk("拒绝消息点名密钥", True, KEY_NAME in msg)
print("       说明: %s" % msg)
# 清掉引用（空数组 = 清空白名单，这正是指针语义要保住的场景），分组就允许删了
call("PUT", "/keys/%d" % key_id, {"allowed_groups": []})
code, _ = call("DELETE", "/groups/%d" % gx_id)
chk("清掉引用后可删除", 200, code)

print()
print("=== 清理 ===")
cleanup()
print("  已清理")
print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
