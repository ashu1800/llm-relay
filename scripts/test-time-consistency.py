#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证同一时刻在日志页与路由页表示一致。

原来路由页的时间走 to_char(created_at, 'YYYY-MM-DD HH24:MI:SS')：
to_char 抹掉时区，且按数据库会话时区（UTC）渲染，
而普通接口返回的是 Go 序列化的 +08:00。
同一个请求在两页相差 8 小时，且没有任何线索指向时区。
"""
import datetime
import json
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
    try:
        with urllib.request.urlopen(req, timeout=30) as x:
            return x.status, json.loads(x.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "{}")
        except Exception:
            return e.code, {}


def parse(ts):
    """解析时间串，拒绝没有时区信息的格式。"""
    return datetime.datetime.fromisoformat(ts.replace("Z", "+00:00"))


print("=== 造一条会进路由页「失败记录」的请求 ===")
_, k = call("POST", "/keys", {"name": "tz-probe-key"})
tok = k["key"]
payload = json.dumps({"model": "no-such-model-tz", "max_tokens": 4,
                      "messages": [{"role": "user", "content": "hi"}]}).encode()
req = urllib.request.Request(BASE + "/v1/chat/completions", data=payload, method="POST")
req.add_header("Content-Type", "application/json")
req.add_header("Authorization", "Bearer " + tok)
try:
    urllib.request.urlopen(req, timeout=60)
except urllib.error.HTTPError as e:
    print("  调用返回 HTTP", e.code, "（预期失败，正是为了进失败记录）")

import time
time.sleep(2)

print()
print("=== 日志接口的时间 ===")
_, logs = call("GET", "/logs?limit=20")
row = None
for r in logs.get("items", []):
    if r.get("model_requested") == "no-such-model-tz":
        row = r
        break
chk("找到该请求的日志", row is not None)
if row:
    print("  created_at = %r" % row.get("created_at"))
    print("  trace_id   = %s" % row.get("trace_id"))

print()
print("=== 路由接口的时间 ===")
_, ra = call("GET", "/routing/analysis?range=1d")
inc = None
for x in ra.get("incidents", []):
    if x.get("model") == "no-such-model-tz":
        inc = x
        break
chk("找到该请求的失败记录", inc is not None)
if inc:
    print("  created_at = %r" % inc.get("created_at"))

print()
print("=== 两边必须指同一时刻 ===")
if row and inc:
    a, b = row.get("created_at", ""), inc.get("created_at", "")
    chk("路由页时间带时区信息", ("+" in b[10:] or b.endswith("Z")),
        "实际 %r —— 没有时区信息就无法判断是哪一刻" % b)
    chk("路由页时间不是空格分隔的裸格式", " " not in b[:19],
        "实际 %r" % b)
    try:
        da, db_ = parse(a), parse(b)
        diff = abs((da - db_).total_seconds())
        print("  日志 %s  vs  路由 %s   相差 %.0f 秒" % (da.isoformat(), db_.isoformat(), diff))
        chk("两者相差在 5 秒内（同一时刻）", diff <= 5,
            "相差 %.0f 秒 —— 时区没对齐" % diff)
    except Exception as e:
        chk("两边都能解析出时刻", False, str(e)[:80])

_, ks = call("GET", "/keys")
for x in ks.get("items", []):
    if x["name"] == "tz-probe-key":
        call("DELETE", "/keys/%d" % x["id"])

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
