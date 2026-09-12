#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证渠道并发名额持有到响应体转发结束。

判据是上游侧的并发峰值，不是耗时：
把渠道 max_concurrency 设为 1，并发发两条流式请求，
上游记录到的同时进行数峰值必须是 1。
若名额在收到响应头就被释放（旧行为），峰值会是 2。

用耗时推断是不可靠的：耗时里混着连接建立、容器调度等噪声，
实测出现过「4.28s 落在串行与并行之间」这种无法下结论的结果。
并发峰值是直接证据。

先启动慢速上游（会随项目一起提交，不需要额外依赖）：
  docker rm -f slow-upstream
  docker run -d --name slow-upstream --network llm-relay_relay \
    -p 127.0.0.1:9997:9999 -e HOLD_MS=2000 \
    -v "$PWD/scripts/slow-upstream.js:/app/server.js:ro" \
    node:22-alpine node /app/server.js
"""
import json
import threading
import time
import urllib.request

ADMIN = "http://127.0.0.1:8888/api/admin"
RELAY = "http://127.0.0.1:8888/v1/chat/completions"
STATS = "http://127.0.0.1:9997/stats"
RESET = "http://127.0.0.1:9997/reset"
MODEL = "slow-concurrency-test"
CHANNEL = "slow-upstream-test"


def call(method, path, body=None, base=ADMIN, token=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=120) as r:
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
        if k["name"] == "slow-concurrency-key":
            call("DELETE", "/keys/%d" % k["id"])


cleanup()

print("=== 准备：渠道 max_concurrency=1，绑定测试模型 ===")
_, ch = call("POST", "/channels", {
    "name": CHANNEL, "protocol": "openai-chat",
    "base_url": "http://slow-upstream:9999/v1",
    "api_key": "test-key", "group_id": 1, "weight": 100,
    "extra_config": {"max_concurrency": 1},
})
cid = ch["id"]
call("POST", "/channels/%d/models" % cid, {"public_name": MODEL, "upstream_name": MODEL})
_, key = call("POST", "/keys", {"name": "slow-concurrency-key"})
token = key["key"]
print("  渠道 id=%d" % cid)

urllib.request.urlopen(RESET, timeout=10).read()

results = {}


def stream_once(idx):
    payload = json.dumps({
        "model": MODEL, "stream": True, "max_tokens": 16,
        "messages": [{"role": "user", "content": "hi"}],
    }).encode()
    req = urllib.request.Request(RELAY, data=payload, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            body = r.read().decode()
        results[idx] = (time.time() - t0, body.count("data:"), None)
    except Exception as e:
        results[idx] = (time.time() - t0, 0, str(e))


print()
print("=== 并发两条流式请求 ===")
t0 = time.time()
a = threading.Thread(target=stream_once, args=(0,))
b = threading.Thread(target=stream_once, args=(1,))
a.start()
time.sleep(0.3)
b.start()
a.join()
b.join()
wall = time.time() - t0

for i in sorted(results):
    d, frames, err = results[i]
    print("  请求 %d: 耗时 %.2fs  帧数 %d  %s" % (i + 1, d, frames, ("错误: " + err) if err else ""))

stats = json.loads(urllib.request.urlopen(STATS, timeout=10).read().decode())
print("  上游观测：峰值并发 %d，累计流 %d 条，总墙钟 %.2fs" % (
    stats["peak_concurrent"], stats["total_streams"], wall))
print()

verdict = True
if stats["peak_concurrent"] == 1:
    print("  [通过] 上游并发峰值为 1 —— 名额确实持有到流转发结束")
elif stats["peak_concurrent"] >= 2:
    print("  [失败] 上游并发峰值为 %d —— 两条同时打到了上游，名额被提前释放" % stats["peak_concurrent"])
    verdict = False
else:
    print("  [失败] 上游没有观测到流式请求，测试无效")
    verdict = False

if stats["total_streams"] != 2:
    print("  [失败] 上游只收到 %d 条流，期望 2 条" % stats["total_streams"])
    verdict = False
if stats["current"] != 0:
    print("  [失败] 测试结束后上游仍有 %d 条流未结束（名额泄漏）" % stats["current"])
    verdict = False

print()
print("=== 清理 ===")
cleanup()
print("  已清理")
print()
print("ALL_PASS" if verdict else "HAS_FAILURE")
