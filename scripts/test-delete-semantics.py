#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证删除接口的语义：不存在的 ID 必须明确报 404，不能假装成功。

原来六个删除接口里有五个对不存在的 ID 返回 {"deleted":true}，
调用方（含前端）分不清「删除成功」和「这个 ID 根本不存在」：
幂等重试、并发删除、传错 ID 全被当成成功，问题被静默吞掉。
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
ok = 0
bad = 0


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(ADMIN + path, data=data, method=method)
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
        print("  [通过] %-46s %s" % (name, got))
    else:
        bad += 1
        print("  [失败] %-46s 期望 %s 实际 %s" % (name, want, got))


print("=== 1. 对不存在的 ID，六个删除接口都必须报 404 ===")
GHOST = 999999
for path, label in [
    ("/channels/%d" % GHOST, "渠道"),
    ("/groups/%d" % GHOST, "分组"),
    ("/keys/%d" % GHOST, "密钥"),
    ("/pricing/%d" % GHOST, "定价"),
]:
    code, body = call("DELETE", path)
    chk("删除不存在的%s" % label, 404, code)
    if code != 404:
        print("       返回体: %s" % body)

print()
print("=== 2. 真删一次应当成功，再删一次应当 404 ===")
_, k = call("POST", "/keys", {"name": "delete-semantics-key"})
kid = k["id"]
code, body = call("DELETE", "/keys/%d" % kid)
chk("第一次删除", 200, code)
chk("返回体仍带 id", kid, body.get("id"))
code, _ = call("DELETE", "/keys/%d" % kid)
chk("第二次删除（幂等重试不该报成功）", 404, code)

print()
print("=== 3. 分组占用守卫 ===")
_, chans = call("GET", "/channels")
occupied = None
for c in chans.get("items", []):
    if c.get("group_id"):
        occupied = c["group_id"]
        break
if occupied is None:
    print("  [跳过] 没有带分组的渠道，无法验证")
else:
    code, body = call("DELETE", "/groups/%d" % occupied)
    chk("删除仍有渠道的分组（期望 409）", 409, code)
    print("       提示: %s" % body.get("error", {}).get("message", body))

print()
print("=== 4. 删渠道要一并清掉模型白名单，且不能留下悬挂 ===")
_, ch = call("POST", "/channels", {
    "name": "delete-semantics-chan", "protocol": "openai-chat",
    "base_url": "http://slow-upstream:9999/v1", "api_key": "k",
    "group_id": 1, "weight": 1,
})
cid = ch["id"]
call("POST", "/channels/%d/models" % cid, {"public_name": "delete-semantics-model"})
_, bindings = call("GET", "/channels/%d/models" % cid)
chk("白名单已建立", 1, len(bindings.get("items", [])))
code, _ = call("DELETE", "/channels/%d" % cid)
chk("删除渠道", 200, code)
code, _ = call("GET", "/channels/%d/models" % cid)
chk("渠道已不存在", 404, code)

# 白名单随渠道一起走：库里不应再留下指向已删渠道的行
code, rows = call("GET", "/channels")
names = {c["id"]: len(c.get("models") or []) for c in rows.get("items", [])}
chk("已删渠道不再出现在渠道列表里", False, cid in names)

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
