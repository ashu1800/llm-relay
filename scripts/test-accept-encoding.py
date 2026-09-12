#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""回归：客户端带 Accept-Encoding 时，统计与费用不能被算错。

Go 的 http.Transport 只在请求里没有 Accept-Encoding 时才自动解压。
透传客户端的这个头会让中继收到压缩字节，解析不出 usage，
退化成按报文长度估算 —— token 数与费用都会严重偏高。
"""
import json
import time
import urllib.error
import urllib.request

ADMIN = "http://127.0.0.1:8888/api/admin"
BASE = "http://127.0.0.1:8888"
ok = 0
bad = 0


def chk(name, cond, detail=""):
    global ok, bad
    if cond:
        ok += 1
        print("  [通过] %s" % name)
    else:
        bad += 1
        print("  [失败] %s  %s" % (name, detail))


def call(m, p, b=None):
    d = json.dumps(b).encode() if b is not None else None
    req = urllib.request.Request(ADMIN + p, data=d, method=m)
    if d:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=30) as x:
        return x.status, json.loads(x.read().decode() or "{}")


_, k = call("POST", "/keys", {"name": "ae-regress-key"})
tok = k["key"]


def chat(accept_encoding=None):
    payload = json.dumps({
        "model": "deepseek-v4-flash", "max_tokens": 16,
        "messages": [{"role": "user", "content": "说三个字"}],
    }).encode()
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=payload, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + tok)
    if accept_encoding:
        req.add_header("Accept-Encoding", accept_encoding)
    with urllib.request.urlopen(req, timeout=90) as r:
        return dict(r.headers), r.read()


def last_log():
    time.sleep(1.5)
    _, logs = call("GET", "/logs?limit=3")
    for r in logs.get("items", []):
        if r.get("model_requested") == "deepseek-v4-flash":
            return r
    return {}


print("=== 不带 Accept-Encoding 作为基准 ===")
_, raw0 = chat()
d0 = json.loads(raw0.decode())
u0 = d0.get("usage", {})
lg0 = last_log()
print("  usage =", u0.get("prompt_tokens"), "/", u0.get("completion_tokens"),
      " 日志 =", lg0.get("prompt_tokens"), "/", lg0.get("completion_tokens"))
chk("正文可解析", True)

for ae in ["gzip", "br, gzip", "deflate"]:
    print()
    print("=== Accept-Encoding: %s ===" % ae)
    hdr, raw = chat(ae)
    try:
        d = json.loads(raw.decode())
        chk("正文可解析为 JSON", True)
    except Exception as e:
        d = {}
        chk("正文可解析为 JSON", False, str(e)[:70])
    chk("响应未被压缩", hdr.get("Content-Encoding") in (None, ""),
        "Content-Encoding=%s" % hdr.get("Content-Encoding"))
    lg = last_log()
    up, uc = lg.get("prompt_tokens"), lg.get("completion_tokens")
    print("  日志 = 输入 %s 输出 %s 费用 %s" % (up, uc, lg.get("estimated_cost")))
    chk("输入 token 与基准一致", up == lg0.get("prompt_tokens"),
        "%s vs %s" % (up, lg0.get("prompt_tokens")))
    chk("输出 token 与基准一致（差值在 2 以内）",
        uc is not None and abs(uc - (lg0.get("completion_tokens") or 0)) <= 2,
        "%s vs %s" % (uc, lg0.get("completion_tokens")))

_, ks = call("GET", "/keys")
for x in ks.get("items", []):
    if x["name"] == "ae-regress-key":
        call("DELETE", "/keys/%d" % x["id"])

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
