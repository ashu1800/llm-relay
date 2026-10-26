#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证模板与密钥的局部更新：没传的字段不得被清掉。

模板管理整个功能已下线（菜单、接口、表都删了）；密钥侧在 2026-10-26
分组下线后语义变了：白名单（allowed_models）与每模型渠道路由
（model_channels）是**一对**数据，且不允许清空 —— 没有模型的密钥什么都
调不了。所以这里的用例围绕新语义：
  1. 整份提交（白名单 + 路由）能改；
  2. 只改名字不动白名单；
  3. 只传一边时后端拿库里现有的另一边凑整份再校验（脚本友好）；
  4. 清空白名单被 400 当场拦下（旧行为是允许清空，新模型下是死钥匙）。
"""
import json
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8888/api/admin"
ok = 0
bad = 0

MODEL = "partial-update-model"
CHANNEL = "partial-update-channel"
KEYNAME = "audit-key-partial"


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
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


def cleanup():
    _, ks = call("GET", "/keys")
    for k in ks.get("items", []):
        if k["name"].startswith(KEYNAME):
            call("DELETE", "/keys/%d" % k["id"])
    _, cs = call("GET", "/channels")
    for c in cs.get("items", []):
        if c["name"] == CHANNEL:
            call("DELETE", "/channels/%d" % c["id"])


def key_row(kid):
    _, kl = call("GET", "/keys")
    return [k for k in kl["items"] if k["id"] == kid][0]


cleanup()

print()
print("=== 准备：探针渠道 + 密钥（白名单与路由整份提交）===")
_, ch = call("POST", "/channels", {
    "name": CHANNEL, "protocol": "openai-chat",
    "base_url": "http://127.0.0.1:9997/v1", "api_key": "k",
    "models": [{"public_name": MODEL, "upstream_name": MODEL}],
})
cid = ch["id"]
code, key = call("POST", "/keys", {"name": KEYNAME, "allowed_models": [MODEL],
                                   "model_channels": {MODEL: [cid]}})
chk("创建密钥（带白名单与路由）", 200, code)
kid = key["id"]
row0 = key_row(kid)
chk("初始白名单", [MODEL], row0["allowed_models"])

print()
print("=== 1. 白名单与路由整份更新 ===")
code, _ = call("PUT", "/keys/%d" % kid, {"allowed_models": [MODEL],
                                         "model_channels": {MODEL: [cid]}})
chk("整份提交被接受", 200, code)
chk("白名单不变", [MODEL], key_row(kid)["allowed_models"])
chk("路由不变", {MODEL: [cid]}, key_row(kid)["model_channels"])

print()
print("=== 2. 只改名字：白名单与路由不得被清掉 ===")
code, _ = call("PUT", "/keys/%d" % kid, {"name": KEYNAME + "-2"})
chk("只改名字", 200, code)
row3 = key_row(kid)
chk("名字已改", KEYNAME + "-2", row3["name"])
chk("白名单未被清掉", [MODEL], row3["allowed_models"])
chk("路由未被清掉", {MODEL: [cid]}, row3["model_channels"])

print()
print("=== 3. 只传一边：后端取库里现有的另一边凑整份 ===")
code, _ = call("PUT", "/keys/%d" % kid, {"allowed_models": [MODEL]})
chk("只传白名单", 200, code)
chk("路由从库里补齐", {MODEL: [cid]}, key_row(kid)["model_channels"])
code, _ = call("PUT", "/keys/%d" % kid, {"model_channels": {MODEL: [cid]}})
chk("只传路由", 200, code)
chk("白名单从库里补齐", [MODEL], key_row(kid)["allowed_models"])

print()
print("=== 4. 清空白名单：新模型下必须被 400 拦下 ===")
# 没有模型的密钥什么都调不了，允许清空只会制造「看起来正常、一发请求
# 就 502」的死钥匙 —— 创建与更新同一个出口（normalizeKeyRoutes）
code, body = call("PUT", "/keys/%d" % kid, {"allowed_models": [],
                                            "model_channels": {}})
chk("清空白名单被拒绝", 400, code)
print("       说明: %s" % body.get("error", {}).get("message", body))
chk("拒绝后白名单原样", [MODEL], key_row(kid)["allowed_models"])

call("DELETE", "/keys/%d" % kid)
call("DELETE", "/channels/%d" % cid)

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
