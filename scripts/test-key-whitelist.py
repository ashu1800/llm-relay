#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证密钥白名单（分组 + 模型）真正影响路由。

此前 allowed_models / allowed_groups 只有存储与回显：创建和更新会写库、
列表接口会显示，但路由侧完全不看 —— 界面上配了限制却毫无效果，
属于「看起来有访问控制、实际没有」，比没有这两个字段更危险。

判据是请求日志里实际命中的渠道：把同一个模型绑到两个分组的渠道上，
再用只允许其中一个分组的密钥去调，命中哪个渠道是确定的。

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
MODEL = "group-whitelist-test"
GROUP_B = "分组B-测试"
CHAN_A = "group-a-channel"
CHAN_B = "group-b-channel"

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


def cleanup():
    for name in ["k-default", "k-groupb", "k-bad", "k-models", "k-goodmodel"]:
        _, ks = call("GET", "/keys")
        for k in ks.get("items", []):
            if k["name"] == name:
                call("DELETE", "/keys/%d" % k["id"])
    _, cs = call("GET", "/channels")
    for c in cs.get("items", []):
        if c["name"] in (CHAN_A, CHAN_B):
            call("DELETE", "/channels/%d" % c["id"])
    # 模型不再有独立接口：删掉渠道时白名单会跟着级联清除
    _, gs = call("GET", "/groups")
    for g in gs.get("items", []):
        if g["name"] in (GROUP_A, GROUP_B):
            call("DELETE", "/groups/%d" % g["id"])


# 分组A 提到 cleanup 之前定义：cleanup 一上来就要按名字清上一轮的残留
GROUP_A = "分组A-测试"


cleanup()

print("=== 准备：两个分组各一个渠道，同一模型都绑上 ===")
# 「分组A」也自建（GROUP_A 已在 cleanup 前定义）：库里可能根本没有
# is_default 的分组 —— 用户删掉默认或取消标记之后，旧写法假设它
# 一定存在，直接 IndexError 中断。
# 白名单条目仍写名字 —— 保存时后端会归一化成分组 ID，正好顺路验证那条路径
_, ga = call("POST", "/groups", {"name": GROUP_A})
default_gid = ga["id"]
default_name = GROUP_A
_, gb = call("POST", "/groups", {"name": GROUP_B, "strategy": "weighted"})
gb_id = gb["id"]
# 两个分组各建一个测试渠道，都指向同一个可控上游。
# 不借用已有的真实渠道：那些上游未必接受这个测试模型，
# 失败会与白名单无关地混进来，让结论不可信。
_, ca = call("POST", "/channels", {
    "name": CHAN_A, "protocol": "openai-chat",
    "base_url": "http://127.0.0.1:9997/v1", "api_key": "k",
    "group_id": default_gid, "weight": 100,
})
_, cb = call("POST", "/channels", {
    "name": CHAN_B, "protocol": "openai-chat",
    "base_url": "http://127.0.0.1:9997/v1", "api_key": "k",
    "group_id": gb_id, "weight": 100,
})
ca_id, cb_id = ca["id"], cb["id"]
print("  默认分组 %s(%d) -> 渠道 %s(id=%d)" % (default_name, default_gid, CHAN_A, ca_id))
print("  分组B   %d -> 渠道 %s(id=%d)" % (gb_id, CHAN_B, cb_id))

for cid in (ca_id, cb_id):
    call("POST", "/channels/%d/models" % cid,
         {"public_name": MODEL, "upstream_name": MODEL})

_, a = call("POST", "/keys", {"name": "k-default", "allowed_groups": [default_name]})
tok_default = a["key"]
_, b = call("POST", "/keys", {"name": "k-groupb", "allowed_groups": [GROUP_B]})
tok_groupb = b["key"]

print()
print("=== 1. 分组白名单必须决定命中哪个渠道 ===")
code, _ = chat(tok_default)
chk("限定默认分组的密钥能调通", 200, code)
chk("命中默认分组的渠道", CHAN_A, last_channel())

code, _ = chat(tok_groupb)
chk("限定分组B的密钥能调通", 200, code)
chk("命中分组B的渠道", CHAN_B, last_channel())

print()
print("=== 2. 白名单解析不出来时必须在创建时就拒绝 ===")
# 写入侧归一化（api.normalizeGroupRefs）之后，不存在的分组在保存瞬间
# 就被拦下 —— 以前是先收下、调用时才 403，打错的名字静默入库
_, c = call("POST", "/keys", {"name": "k-bad", "allowed_groups": ["这个分组不存在"]})
chk("分组不存在时创建被拒", 400, c.get("error", {}).get("code", c))
print("       说明: %s" % c.get("error", {}).get("message", c))

print()
print("=== 3. 模型白名单 ===")
_, d = call("POST", "/keys", {"name": "k-models", "allowed_models": ["别的模型"]})
code, body = chat(d["key"])
chk("模型不在白名单时拒绝", 403, code)
print("       说明: %s" % body.get("error", {}).get("message", body))

_, e = call("POST", "/keys", {"name": "k-goodmodel", "allowed_models": [MODEL]})
code, _ = chat(e["key"])
chk("模型在白名单时放行", 200, code)

print()
print("=== 4. 不带白名单的密钥不受影响（默认放行）===")
_, f = call("POST", "/keys", {"name": "k-nolimit"})
_, ks = call("GET", "/keys")
row = [k for k in ks["items"] if k["name"] == "k-nolimit"][0]
chk("白名单为空", [], row["allowed_groups"])
code, _ = chat(f["key"])
chk("空白名单的密钥仍可调用", 200, code)
call("DELETE", "/keys/%d" % row["id"])

print()
print("=== 清理 ===")
cleanup()
print("  已清理")
print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
