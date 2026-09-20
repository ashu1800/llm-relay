#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证上游流中断时不会被伪装成「正常结束」。

做法：让上游发两个分片后直接掐断连接（模型名里带 truncate 即触发），
然后检查中继下发给客户端的是什么。
截断若走了正常收尾，客户端会看到 message_stop / [DONE]，
于是把少了一半的回复当成完整回复。

先启动慢速上游：
  docker rm -f slow-upstream
  docker run -d --name slow-upstream --network llm-relay_relay \
    -p 127.0.0.1:9997:9999 -v "$PWD/scripts/slow-upstream.js:/app/server.js:ro" \
    node:22-alpine node /app/server.js
"""
import json
import urllib.error
import urllib.request

ADMIN = "http://127.0.0.1:8888/api/admin"
BASE = "http://127.0.0.1:8888"
MODEL = "truncate-test"
CHANNEL = "truncate-upstream-test"

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


def call(method, path, body=None, base=ADMIN):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.status, json.loads(r.read().decode() or "{}")


def cleanup():
    _, chans = call("GET", "/channels")
    for c in chans.get("items", []):
        if c["name"] == CHANNEL:
            call("DELETE", "/channels/%d" % c["id"])
    _, ms = call("GET", "/models")
    for m in ms.get("items", []):
        if m["public_name"] == MODEL:
            call("DELETE", "/models/%d" % m["id"])
    _, ks = call("GET", "/keys")
    for k in ks.get("items", []):
        if k["name"] == "truncate-key":
            call("DELETE", "/keys/%d" % k["id"])


def raw_stream(path, payload, token):
    req = urllib.request.Request(BASE + path,
                                 data=json.dumps(payload).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return r.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.read().decode("utf-8", "replace")


cleanup()
print("=== 准备：渠道指向会掐断连接的上游 ===")
_, ch = call("POST", "/channels", {
    "name": CHANNEL, "protocol": "openai-chat",
    "base_url": "http://127.0.0.1:9997/v1",
    "api_key": "k", "group_id": 1, "weight": 100,
})
cid = ch["id"]
call("POST", "/channels/%d/models" % cid, {"public_name": MODEL, "upstream_name": MODEL})
_, key = call("POST", "/keys", {"name": "truncate-key"})
token = key["key"]
print("  渠道 id=%d" % cid)

print()
print("=== Anthropic 入站（需要逐事件改写）===")
body = raw_stream("/v1/messages", {
    "model": MODEL, "stream": True, "max_tokens": 64,
    "messages": [{"role": "user", "content": "hi"}],
}, token)
has_error = "event: error" in body
has_stop = "message_stop" in body
print("  收到的事件类型: %s" % sorted(set(
    l.split(":", 1)[1].strip() for l in body.splitlines() if l.startswith("event:"))))
chk("截断时下发 error 事件", has_error, "实际响应: %r" % body[-300:])
chk("截断时不得补发 message_stop", not has_stop,
    "客户端会把它当成完整回复")

print()
print("=== OpenAI 入站（同协议透传）===")
body2 = raw_stream("/v1/chat/completions", {
    "model": MODEL, "stream": True, "max_tokens": 64,
    "messages": [{"role": "user", "content": "hi"}],
}, token)
chk("截断时不得出现 [DONE]", "[DONE]" not in body2, "实际响应: %r" % body2[-300:])
chk("确实收到了部分内容（否则测试无效）", "tok" in body2, "实际响应: %r" % body2[-300:])

print()
print("=== 请求日志是否记为失败 ===")
_, logs = call("GET", "/logs?limit=5")
rows = logs.get("items", [])
row = None
for r in rows:
    if r.get("model_requested") == MODEL:
        row = r
        break
chk("找到了本次请求日志", row is not None)
if row:
    print("    状态码=%s 错误=%r" % (row.get("status_code"), row.get("error")))
    chk("状态码记为失败(502)", row.get("status_code") == 502,
        "实际 %s" % row.get("status_code"))
    chk("错误信息说明了中断", "中断" in (row.get("error") or ""),
        "实际 %r" % row.get("error"))

print()
print("=== 清理 ===")
cleanup()
print("  已清理")
print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
