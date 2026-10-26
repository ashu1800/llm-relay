#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证密钥的每模型渠道路由真正影响转发。

2026-10-26 分组移除后，密钥的访问控制 = allowed_models（能调哪些模型）
+ model_channels（每个模型按序尝试哪些渠道）。创建时后端统一校验：
模型非空、渠道存在且白名单里有该模型 —— 校验不过当场 400。

判据是请求日志里实际命中的渠道：把同一个模型绑到两条渠道上，
用「只路由到其中一条」的密钥去调，命中哪条是确定的。
（原「分组白名单」用例的接位者：旧版靠 allowed_groups 限定渠道集，
新版每把密钥直接持有自己的有序候选渠道。）

先启动慢速上游（脚本会自己确保它在跑：bash scripts/ensure-mock-upstream.sh，
以宿主 node 进程跑在 127.0.0.1:9997）：
    -p 127.0.0.1:9997:9999 -v "$PWD/scripts/slow-upstream.js:/app/server.js:ro" \
    node:22-alpine node /app/server.js
"""
import json
import time
import urllib.error
import urllib.request

import os
import subprocess

# 依赖 mock 上游（slow-upstream）：它可能不在场，
# 这里先确保它在跑，避免把「上游不在」误判成产品问题
subprocess.run(
    ["bash", os.path.join(os.path.dirname(os.path.abspath(__file__)), "ensure-mock-upstream.sh")],
    check=True,
)

ADMIN = "http://127.0.0.1:8888/api/admin"
BASE = "http://127.0.0.1:8888"
MODEL = "key-route-test"
CHAN_A = "key-route-channel-a"
CHAN_B = "key-route-channel-b"

ok = 0
bad = 0


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(ADMIN + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
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
        print("  [通过] %-44s %s" % (name, got))
    else:
        bad += 1
        print("  [失败] %-44s 期望 %s 实际 %s" % (name, want, got))


def chat(token, model=MODEL):
    payload = json.dumps({"model": model, "max_tokens": 8,
                          "messages": [{"role": "user", "content": "hi"}]}).encode()
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=payload, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=90) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:
            return e.code, {}


def last_channel(model=MODEL):
    """取最近一条该模型的请求日志，返回命中的渠道名。"""
    time.sleep(1.2)
    _, logs = call("GET", "/logs?limit=10")
    for r in logs.get("items", []):
        if r.get("model_requested") == model:
            return r.get("channel_name")
    return None


def v1_models(token):
    req = urllib.request.Request(BASE + "/v1/models")
    req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:
            return e.code, {}


def cleanup():
    for name in ["k-route-a", "k-route-b", "k-badchan", "k-badmodel", "k-noroute"]:
        _, ks = call("GET", "/keys")
        for k in ks.get("items", []):
            if k["name"] == name:
                call("DELETE", "/keys/%d" % k["id"])
    _, cs = call("GET", "/channels")
    for c in cs.get("items", []):
        if c["name"] in (CHAN_A, CHAN_B):
            call("DELETE", "/channels/%d" % c["id"])


cleanup()

print("=== 准备：两条渠道，同一模型都绑上 ===")
# 两条测试渠道都指向同一个可控上游。不借用已有的真实渠道：那些上游
# 未必接受这个测试模型，失败会与路由无关地混进来，让结论不可信。
_, ca = call("POST", "/channels", {
    "name": CHAN_A, "protocol": "openai-chat",
    "base_url": "http://127.0.0.1:9997/v1", "api_key": "k",
})
_, cb = call("POST", "/channels", {
    "name": CHAN_B, "protocol": "openai-chat",
    "base_url": "http://127.0.0.1:9997/v1", "api_key": "k",
})
ca_id, cb_id = ca["id"], cb["id"]
print("  渠道 %s(id=%d)  渠道 %s(id=%d)" % (CHAN_A, ca_id, CHAN_B, cb_id))

for cid in (ca_id, cb_id):
    call("POST", "/channels/%d/models" % cid,
         {"public_name": MODEL, "upstream_name": MODEL})

# 各建一把只路由到自己渠道的密钥
_, a = call("POST", "/keys", {"name": "k-route-a",
                              "allowed_models": [MODEL],
                              "model_channels": {MODEL: [ca_id]}})
tok_a = a["key"]
_, b = call("POST", "/keys", {"name": "k-route-b",
                              "allowed_models": [MODEL],
                              "model_channels": {MODEL: [cb_id]}})
tok_b = b["key"]

print()
print("=== 1. 每模型渠道路由必须决定命中哪条渠道 ===")
code, _ = chat(tok_a)
chk("路由到渠道A的密钥能调通", 200, code)
chk("命中渠道A", CHAN_A, last_channel())

code, _ = chat(tok_b)
chk("路由到渠道B的密钥能调通", 200, code)
chk("命中渠道B", CHAN_B, last_channel())

print()
print("=== 2. 路由配不出来的密钥必须在创建时就拒绝 ===")
# 渠道不存在 / 渠道白名单里没有该模型 / 键集合与 allowed_models 不一致 /
# 一个模型都没选 —— 四种都该 400，以前是先收下、调用时才 403/502
_, c = call("POST", "/keys", {"name": "k-badchan",
                              "allowed_models": [MODEL],
                              "model_channels": {MODEL: [99999999]}})
chk("渠道不存在时创建被拒", 400, c.get("error", {}).get("code", c))
print("       说明: %s" % c.get("error", {}).get("message", c))

_, cm = call("POST", "/keys", {"name": "k-badmodel",
                               "allowed_models": ["这个模型不存在"],
                               "model_channels": {"这个模型不存在": [ca_id]}})
chk("渠道没有该模型时创建被拒", 400, cm.get("error", {}).get("code", cm))

_, cn = call("POST", "/keys", {"name": "k-noroute"})
chk("不选模型不允许创建密钥", 400, cn.get("error", {}).get("code", cn))

print()
print("=== 3. /v1/models 按密钥真正能调的模型过滤 ===")
_, body = v1_models(tok_a)
names = sorted(m.get("id") for m in body.get("data", []) if m.get("id") == MODEL)
chk("探测结果包含路由里的模型", [MODEL], names)

print()
print("=== 4. 路由形状回显 ===")
_, ks = call("GET", "/keys")
row = [k for k in ks["items"] if k["name"] == "k-route-a"][0]
chk("model_channels 回显", {MODEL: [ca_id]}, row.get("model_channels"))

print()
print("=== 清理 ===")
cleanup()
print("  已清理")
print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
