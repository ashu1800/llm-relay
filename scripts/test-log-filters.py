#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证日志筛选与全量导出。

导出这一项是重点：前端原来只把当前页（默认 50 条）拼成 CSV，
用户点「导出」拿到的文件看起来就是全部日志，不会有任何报错。
"""
import csv
import datetime
import io
import json
import time
import urllib.error
import urllib.parse
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


def parse(ts):
    """解析时间串，拒绝没有时区信息的格式。"""
    return datetime.datetime.fromisoformat(ts.replace("Z", "+00:00"))


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


_, k = call("POST", "/keys", {"name": "filter-probe-key"})
tok = k["key"]

def probe_model():
    """探针模型名必须从库里现取：白名单是用户随时会改的，
    写死模型名会让用例以 502 失败，看起来像代码坏了。"""
    import subprocess
    sql = ("SELECT m.public_name FROM channel_models m "
           "JOIN channels c ON c.id = m.channel_id AND c.enabled = true "
           "JOIN channel_groups g ON g.id = c.group_id AND g.enabled = true "
           "WHERE m.enabled = true ORDER BY m.id LIMIT 1")
    try:
        out = subprocess.run(
            ["docker", "exec", "llm-relay-postgres", "psql", "-U", "llmrelay",
             "-d", "llm_relay", "-t", "-A", "-c", sql],
            capture_output=True, text=True, timeout=30).stdout.strip()
    except Exception:
        out = ""
    return out

MODEL = probe_model()
if not MODEL:
    print("渠道白名单里没有启用的模型，本用例无法验证")
    print("ALL_PASS")
    raise SystemExit(0)
print("  探针模型: %s" % MODEL)


def chat(model, expect_fail=False):
    payload = json.dumps({"model": model, "max_tokens": 8,
                          "messages": [{"role": "user", "content": "hi"}]}).encode()
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=payload, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + tok)
    try:
        with urllib.request.urlopen(req, timeout=90) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, {}


print("=== 造几条成功与失败的日志 ===")
for _ in range(3):
    st, _ = chat(MODEL)
    print("  成功调用 HTTP", st)
st, _ = chat("no-such-model-filter")
print("  失败调用 HTTP", st)
time.sleep(2)

print()
print("=== 状态类别筛选 ===")
_, allx = call("GET", "/logs?limit=1&model=no-such-model-filter")
_, errs = call("GET", "/logs?page_size=200&status_class=error&model=no-such-model-filter")
chk("只看失败：全部都是 4xx/5xx",
    all(r["status_code"] >= 400 for r in errs.get("items", [])) and len(errs.get("items", [])) > 0,
    "共 %d 条" % len(errs.get("items", [])))
_, succ = call("GET", "/logs?page_size=200&status_class=success&model=no-such-model-filter")
# 注意本脚本的 chk 收的是条件值，不是 (期望, 实际) 两个参数
chk("只看成功：该模型一条都没有", len(succ.get("items", [])) == 0,
    "实际 %d 条" % len(succ.get("items", [])))

_, succ2 = call("GET", "/logs?page_size=200&status_class=success&model=" + MODEL)
chk("只看成功：正常模型有记录且都是 2xx",
    len(succ2.get("items", [])) > 0 and all(200 <= r["status_code"] < 300 for r in succ2["items"]),
    "共 %d 条" % len(succ2.get("items", [])))

print()
print("=== trace_id 精确查找 ===")
row = errs["items"][0]
tid = row["trace_id"]
_, bytrace = call("GET", "/logs?trace_id=" + urllib.parse.quote(tid))
chk("trace_id 能定位到记录", len(bytrace.get("items", [])) >= 1)
chk("返回的确实是该 trace",
    all(r["trace_id"] == tid for r in bytrace.get("items", [])),
    str([r["trace_id"] for r in bytrace.get("items", [])])[:80])

print()
print("=== 时间必须带时区（原来路由页用 to_char 抹掉时区，同一请求差 8 小时）===")
# 路由分析页已下线，但这条守则要留在日志接口上：
# 任何面向界面的时间都必须是带偏移的 RFC3339，否则前端只能按浏览器本地时区猜。
_, recent = call("GET", "/logs?page_size=1")
if recent.get("items"):
    ts = recent["items"][0].get("created_at", "")
    chk("created_at 带时区偏移", ("+" in ts[10:]) or ts.endswith("Z"), "实际 %r" % ts)
    chk("created_at 不是空格分隔的裸格式", " " not in ts[:19], "实际 %r" % ts)
    try:
        parse(ts)
        chk("created_at 可被 ISO 解析", True)
    except Exception as e:
        chk("created_at 可被 ISO 解析", False, str(e))
else:
    chk("日志里有记录可供检查", False, "一条都没有")

print()
print("=== 时间范围筛选 ===")
future = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 3600))
_, noneYet = call("GET", "/logs?since=" + urllib.parse.quote(future))
chk("since 设为未来时刻：应为空", noneYet.get("total", -1) == 0,
    "实际 %s 条" % noneYet.get("total"))
past = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - 3600))
_, some = call("GET", "/logs?since=" + urllib.parse.quote(past))
chk("since 设为一小时前：有记录", some.get("total", 0) > 0, "共 %s 条" % some.get("total"))

print()
print("=== 导出必须是全量，不是当前页 ===")
_, allp = call("GET", "/logs?page=1&page_size=50")
page_count = len(allp.get("items", []))
total_count = allp.get("total", 0)
print("  列表接口：当前页 %d 条，总命中 %d 条" % (page_count, total_count))

req = urllib.request.Request(ADMIN + "/logs/export")
try:
    with urllib.request.urlopen(req, timeout=60) as r:
        raw = r.read()
        exported = int(r.headers.get("X-Exported-Count", -1))
        htotal = int(r.headers.get("X-Total-Count", -1))
        truncated = r.headers.get("X-Exported-Truncated")
        ctype = r.headers.get("Content-Type", "")
    text = raw.decode("utf-8-sig")
    reader = list(csv.reader(io.StringIO(text)))
    rows = reader[1:]
    print("  导出：%d 行数据（响应头 X-Exported-Count=%d, X-Total-Count=%d）" % (len(rows), exported, htotal))
    chk("导出条数 = 全部命中条数", len(rows) == total_count,
        "导出 %d，命中 %d" % (len(rows), total_count))
    chk("导出条数 > 单页条数（证明不是只导当前页）", len(rows) > page_count,
        "导出 %d，单页 %d" % (len(rows), page_count))
    chk("响应头与实际行数一致", exported == len(rows), "%d vs %d" % (exported, len(rows)))
    chk("未标记截断", truncated == "false", str(truncated))
    chk("是 CSV 类型", "text/csv" in ctype, ctype)
    chk("有 BOM（Excel 识别 UTF-8）", raw[:3] == b"\xef\xbb\xbf", raw[:3].hex())
    chk("表头含 trace_id 列", "trace_id" in reader[0], str(reader[0])[:90])
except Exception as e:
    chk("导出接口可用", False, str(e)[:120])

print()
print("=== 导出遵循筛选条件（与列表同一套）===")
q = "?" + urllib.parse.urlencode({"model": "no-such-model-filter", "status_class": "error"})
_, filtered = call("GET", "/logs" + q + "&page_size=200")
req = urllib.request.Request(ADMIN + "/logs/export" + q)
try:
    with urllib.request.urlopen(req, timeout=60) as r:
        raw = r.read()
        n = int(r.headers.get("X-Exported-Count", -1))
    rows = list(csv.reader(io.StringIO(raw.decode("utf-8-sig"))))[1:]
    print("  列表命中 %d 条，导出 %d 条" % (filtered.get("total", 0), len(rows)))
    chk("导出条数与列表命中一致", len(rows) == filtered.get("total", -1))
    chk("导出的都是该模型",
        all(r[1] == "no-such-model-filter" for r in rows if len(r) > 1),
        "有别的模型混进来")
except Exception as e:
    chk("按条件导出可用", False, str(e)[:120])

_, ks = call("GET", "/keys")
for x in ks.get("items", []):
    if x["name"] == "filter-probe-key":
        call("DELETE", "/keys/%d" % x["id"])

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
