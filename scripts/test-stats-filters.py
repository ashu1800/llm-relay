#!/usr/bin/env python3
# 看板的筛选：/stats/* 的 group_id / channel_id。
#
# 为什么要有这个用例：看板上的数字是「按筛选后的一部分日志」算出来的，
# 而这类聚合最容易出的错不是报错，是**静默算错一部分**：
#   - 只给主查询加了筛选、忘了按币种的金额查询 → 请求数是筛过的、金额是全站的；
#   - 参数解析失败被当成「不筛选」→ 筛选框明明选着某个分组，数字却是全站的；
#   - 两个条件写成 OR 而不是 AND → 选得越细，数字反而越大。
# 三种都不会抛异常，页面上一切正常。所以这里建两条分属不同分组、不同币种的
# 渠道，各发确定的请求数，再按分组 / 渠道 / 两者同时 三种方式去要汇总，
# 逐个断言到具体数字。
#
# 测试实体一律 __ 前缀：purge-test-logs.sh 按这个前缀兜底清日志，
# 不会碰到用户的真实密钥名与模型名。
import json
import os
import subprocess
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:8888"
ADMIN = BASE + "/api/admin"

HERE = os.path.dirname(os.path.abspath(__file__))
subprocess.run(["bash", os.path.join(HERE, "ensure-mock-upstream.sh")], check=True)

KEY_NAME = "__dash-filter-key"
G1 = "__dash-g1"
G2 = "__dash-g2"
C1 = "__dash-c1-cny"
C2 = "__dash-c2-usd"
M1 = "__dash-m1"
M2 = "__dash-m2"
UPSTREAM = "http://slow-upstream:9999/v1"

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


def stats(path, **params):
    """取一个统计接口，返回 (状态码, 解析后的 JSON)。"""
    q = "&".join("%s=%s" % (k, v) for k, v in params.items())
    req = urllib.request.Request(ADMIN + "/stats/" + path + ("?" + q if q else ""))
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
        print("  [通过] %-44s %s" % (name, got))
    else:
        bad += 1
        print("  [失败] %-44s 期望 %s 实际 %s" % (name, want, got))


def wait_requests(want, **params):
    """按某个筛选取汇总，等到请求数等于期望值再返回。

    日志是**异步写入**的（relay.LogWriter 把行投进队列、后台协程再 Create），
    请求返回 200 的那一刻行还没进库。刚发完就查会查到一个少数一条的快照，
    失败信息看起来像「筛选漏了数据」，其实是写入延迟 —— 实测踩过一次。
    超时后返回最后一次结果，交给断言去报错（不在这里吞掉失败）。
    """
    deadline = time.time() + 20
    data = {}
    while True:
        _, data = stats("summary", range="today", **params)
        if data.get("requests") == want or time.time() > deadline:
            return data
        time.sleep(0.5)


def chat(key, model, n=1):
    """发 n 次请求，返回成功次数。"""
    hit = 0
    for i in range(n):
        payload = json.dumps(
            {"model": model, "messages": [{"role": "user", "content": "hi %d" % i}], "max_tokens": 8}
        ).encode()
        req = urllib.request.Request(BASE + "/v1/chat/completions", data=payload, method="POST")
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", "Bearer " + key)
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                if r.status == 200:
                    hit += 1
        except Exception:
            pass
    return hit


def purge():
    """清掉本用例上次留下的实体（跑到一半被打断时用）。"""
    _, chans = call("GET", "/channels")
    for c in chans.get("items", []):
        if c["name"] in (C1, C2):
            call("DELETE", "/channels/%d" % c["id"])
    _, gs = call("GET", "/groups")
    for g in gs.get("items", []):
        if g["name"] in (G1, G2):
            call("DELETE", "/groups/%d" % g["id"])
    _, ks = call("GET", "/keys")
    for k in ks.get("items", []):
        if k["name"] == KEY_NAME:
            call("DELETE", "/keys/%d" % k["id"])


purge()

print("=== 1. 两条渠道分属两个分组、两种币种 ===")
_, g1 = call("POST", "/groups", {"name": G1})
_, g2 = call("POST", "/groups", {"name": G2})
gid1, gid2 = g1["id"], g2["id"]
_, c1 = call(
    "POST",
    "/channels",
    {
        "name": C1,
        "group_id": gid1,
        "protocol": "openai-chat",
        "base_url": UPSTREAM,
        "api_key": "k",
        "currency": "CNY",
        "models": [{"public_name": M1, "input_per_1m": "1", "output_per_1m": "2"}],
    },
)
_, c2 = call(
    "POST",
    "/channels",
    {
        "name": C2,
        "group_id": gid2,
        "protocol": "openai-chat",
        "base_url": UPSTREAM,
        "api_key": "k",
        "currency": "USD",
        "models": [{"public_name": M2, "input_per_1m": "1", "output_per_1m": "2"}],
    },
)
cid1, cid2 = c1["id"], c2["id"]
chk("渠道 C1 的记账币种", "CNY", c1.get("currency"))
chk("渠道 C2 的记账币种", "USD", c2.get("currency"))
print("  C1 id=%d (分组 %d, CNY)  C2 id=%d (分组 %d, USD)" % (cid1, gid1, cid2, gid2))

print()
print("=== 2. 各发确定的请求数：C1 三次、C2 两次 ===")
_, k = call("POST", "/keys", {"name": KEY_NAME})
key = k["key"]
# 模型名只挂在自己的渠道上，所以落点是确定的
chk("C1 成功调用次数", 3, chat(key, M1, 3))
chk("C2 成功调用次数", 2, chat(key, M2, 2))

print()
print("=== 3. 汇总按分组收窄 ===")
_, allsum = stats("summary", range="today")
s1 = wait_requests(3, group_id=gid1)
s2 = wait_requests(2, group_id=gid2)
chk("分组 G1 的请求数", 3, s1["requests"])
chk("分组 G2 的请求数", 2, s2["requests"])
chk("G1 只出现 CNY 金额", ["CNY"], sorted(s1["costs"].keys()))
chk("G2 只出现 USD 金额", ["USD"], sorted(s2["costs"].keys()))
chk("回显生效的筛选", {"group_id": gid1, "channel_id": 0}, s1.get("filter"))
# 两个分组各自的数量必须真的小于全站：否则「筛选没生效」和「筛选生效」看起来一样
chk("全站请求数大于任一分组", True, allsum["requests"] > max(s1["requests"], s2["requests"]))
# 金额与请求数必须来自同一个范围：只给主查询加筛选时，这里会出现
# 「请求数是 3、金额却是全站的」
chk("G1 金额非 0（说明金额查询也筛过）", True, float(s1["costs"]["CNY"]) > 0)

print()
print("=== 4. 汇总按渠道收窄，且分组与渠道是 AND 而不是 OR ===")
sc1 = wait_requests(3, channel_id=cid1)
sc2 = wait_requests(2, channel_id=cid2)
chk("按渠道 C1 的请求数", 3, sc1["requests"])
chk("按渠道 C2 的请求数", 2, sc2["requests"])
chk("按渠道 C1 只有 CNY", ["CNY"], sorted(sc1["costs"].keys()))
chk("按渠道 C2 只有 USD", ["USD"], sorted(sc2["costs"].keys()))
# 只能断言「不小于」：这台机器上还跑着用户的真实调用，全站数字一定更大
chk("全站请求数不小于两个渠道之和", True, allsum["requests"] >= sc1["requests"] + sc2["requests"])
_, both = stats("summary", range="today", group_id=gid1, channel_id=cid2)
chk("分组 G1 + 渠道 C2（不属于它）= 0", 0, both["requests"])
chk("分组 G2 + 渠道 C2（属于它）= 2", 2, wait_requests(2, group_id=gid2, channel_id=cid2)["requests"])
chk("不存在的分组 = 0", 0, stats("summary", range="today", group_id=9999999)[1]["requests"])

print()
print("=== 5. 非法参数报错，而不是静默变成全量 ===")
code, body = stats("summary", range="today", group_id="abc")
chk("group_id=abc 的状态码", 400, code)
chk("错误信息点名参数", True, "group_id" in json.dumps(body, ensure_ascii=False))
chk("group_id=0 也拒绝（0 会让人以为等于「全部」）", 400, stats("summary", range="today", group_id=0)[0])
chk("channel_id=-1 也拒绝", 400, stats("summary", range="today", channel_id=-1)[0])

print()
print("=== 6. 其余统计接口同样吃筛选 ===")
_, ts = stats("timeseries", range="today", group_id=gid1)
chk("趋势图各桶请求数之和", 3, sum(p["requests"] for p in ts["items"]))
_, ms = stats("models", range="today", limit=100, group_id=gid1)
chk("分组 G1 只出现自己的模型", [M1], sorted(i["name"] for i in ms["items"]))
chk("该模型的请求数", 3, ms["items"][0]["requests"])
_, chs = stats("channels", range="today", limit=100, group_id=gid2)
chk("分组 G2 的渠道榜单", [C2], sorted(i["name"] for i in chs["items"]))
_, hm = stats("heatmap", days=1, channel_id=cid1)
chk("热力图按渠道收窄", 3, sum(i["requests"] for i in hm["items"]))
_, hmall = stats("heatmap", days=1)
chk("热力图不带筛选时更大", True, sum(i["requests"] for i in hmall["items"]) > 3)

print()
print("=== 7. 清理 ===")
call("DELETE", "/channels/%d" % cid1)
call("DELETE", "/channels/%d" % cid2)
call("DELETE", "/groups/%d" % gid1)
call("DELETE", "/groups/%d" % gid2)
call("DELETE", "/keys/%d" % k["id"])
_, left = call("GET", "/channels")
chk("探针渠道已删除", 0, len([c for c in left.get("items", []) if c["name"] in (C1, C2)]))
# 请求日志会留在库里污染看板，交给仓库里那支统一清理脚本（按 __ 前缀匹配）
subprocess.run(["bash", os.path.join(HERE, "purge-test-logs.sh")], check=True)

print()
if bad == 0:
    print("通过 %d 项结果" % ok)
    print("ALL_PASS")
else:
    print("通过 %d 项，失败 %d 项" % (ok, bad))
    print("HAS_FAILURE")
    raise SystemExit(1)
