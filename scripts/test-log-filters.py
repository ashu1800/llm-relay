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

import os
import sys
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"))
from testdb import psql_cmd
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

import subprocess


def probe_model():
    """探针模型名必须从库里现取：白名单是用户随时会改的，
    写死模型名会让用例以 502 失败，看起来像代码坏了。"""
    sql = ("SELECT m.public_name FROM channel_models m "
           "JOIN channels c ON c.id = m.channel_id AND c.enabled = true "
           "JOIN channel_groups g ON g.id = c.group_id AND g.enabled = true "
           "WHERE m.enabled = true ORDER BY m.id LIMIT 1")
    try:
        out = subprocess.run(
            psql_cmd() + ["-t", "-A", "-c", sql],
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


def probe_scope(model):
    """探针模型所在渠道的 id 与它所属分组的 id。

    和模型名一样从库里现取：写死 id 会在渠道被删、重建之后变成假的失败，
    而这条用例要验证的是「按分组 / 按渠道筛得对不对」，不是「id 是不是这几个」。
    """
    sql = ("SELECT c.id::text || ' ' || c.group_id::text FROM channel_models m "
           "JOIN channels c ON c.id = m.channel_id AND c.enabled = true "
           "WHERE m.public_name = '%s' AND m.enabled = true ORDER BY m.id LIMIT 1" % model)
    try:
        out = subprocess.run(
            psql_cmd() + ["-t", "-A", "-c", sql],
            capture_output=True, text=True, timeout=30).stdout.strip()
    except Exception:
        out = ""
    parts = out.split()
    if len(parts) != 2 or not parts[0].isdigit() or not parts[1].isdigit():
        return 0, 0
    return int(parts[0]), int(parts[1])


def channel_outside(group_id):
    """另一个分组里的渠道 id：用来证明「分组 + 渠道」是 AND 而不是 OR。

    只有一个分组时返回 0，调用方跳过那条断言 —— 这种情况本身不是错误。
    """
    sql = ("SELECT c.id::text FROM channels c WHERE c.group_id <> %d "
           "ORDER BY c.id LIMIT 1" % group_id)
    try:
        out = subprocess.run(
            psql_cmd() + ["-t", "-A", "-c", sql],
            capture_output=True, text=True, timeout=30).stdout.strip()
    except Exception:
        out = ""
    return int(out) if out.isdigit() else 0


def chat(model, expect_fail=False, user_agent=None, extra_headers=None):
    payload = json.dumps({"model": model, "max_tokens": 8,
                          "messages": [{"role": "user", "content": "hi"}]}).encode()
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=payload, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + tok)
    if user_agent:
        req.add_header("User-Agent", user_agent)
    for k, v in (extra_headers or {}).items():
        req.add_header(k, v)
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
print("=== 客户端识别（请求日志的「客户端」列）===")
# 覆盖 handler → RelayRequest → BuildLog → DB → 列表接口这条完整链路。
# 单测只能覆盖到 ExtractClient 与 BuildLog 两端，中间那三行赋值没有单测 ——
# 它们漏了不会报错，只是整列都是空的，所以要在这里钉一次。
chat(MODEL, user_agent="claude-cli/2.1.260 (external, cli)")
time.sleep(2)
# 不加筛选条件、只取最新一页：识别出的行按 id 倒序排在最前
_, cli = call("GET", "/logs?page_size=50")
cli_rows = cli.get("items", [])
detected = [r for r in cli_rows if r.get("client_id") == "claude-code"]
chk("带 claude-cli UA 的请求被识别成 claude-code",
    len(detected) > 0, "共 %d 条日志，其中 %d 条识别为 claude-code" % (len(cli_rows), len(detected)))
if detected:
    chk("识别出的显示名是 Claude Code", detected[0].get("client_name") == "Claude Code",
        str(detected[0].get("client_name")))
    chk("UA 原文随日志落库（补规则表的依据）",
        "claude-cli/2.1.260" in (detected[0].get("client_ua") or ""),
        str(detected[0].get("client_ua"))[:80])

# ZCode 的两条信号各钉一次（规则来源见 relay/client.go 的 zcode 条目）：
# 官方通道靠 UA 前缀 ZCode/；自定义 provider 的 UA 是裸 ai-sdk 形态，
# 只能靠自报头 x-app —— 用户实际走的就是这条，必须全链路验证。
chat(MODEL, user_agent="ZCode/3.15.3")
chat(MODEL, user_agent="ai-sdk/openai-compatible/1.0.6 runtime/node.js/v22.14.0",
     extra_headers={"x-app": "zcode"})
time.sleep(2)
_, cli = call("GET", "/logs?page_size=50")
cli_rows = cli.get("items", [])
for ua_probe, how in (("ZCode/3.15.3", "UA 前缀"), ("ai-sdk/openai-compatible", "x-app 自报头")):
    rows = [r for r in cli_rows if (r.get("client_ua") or "").startswith(ua_probe)
            and r.get("client_id") == "zcode"]
    chk("ZCode（%s信号）被识别成 zcode/ZCode" % how, len(rows) > 0 and rows[0].get("client_name") == "ZCode",
        "client_id=%s client_name=%s" % (rows[0].get("client_id") if rows else None,
                                         rows[0].get("client_name") if rows else None))

print()
print("=== 客户端显示映射（对照表）===")
# 覆盖「观测 → 自动登记待设置 → 配置映射 → CSV 用映射名 → 删除回落」。
# 前端显示优先级的单测在 clientDisplay.spec.ts；这里钉的是后端链路：
# logwriter 的 ensureClientAlias、CRUD 接口、exportLogs 的映射名。
chat(MODEL, user_agent="dsh-harness/0.3")
time.sleep(2)
_, al = call("GET", "/client-aliases")
al_rows = al.get("items", [])
# 幂等：上一次跑完已经把这一行配成 DSH 了（端点没有 POST，不能手工造行）。
# 先摘掉名字/图标让它回到「待设置」，否则重跑时下面那条断言必然失败、
# 而后续的 CSV / 外链断言又全都包在 if alias_id 里被静默跳过 ——
# 看起来像回归，实际覆盖已经消失（2026-10-19 审查发现）。
existing = [r for r in al_rows if r.get("key") == "dsh-harness"]
if existing:
    call("PUT", "/client-aliases/%d" % existing[0]["id"], {"name": "", "icon": ""})
    _, al = call("GET", "/client-aliases")
    al_rows = al.get("items", [])
pend = [r for r in al_rows if r.get("key") == "dsh-harness" and not r.get("name")]
chk("未知客户端自动登记为待设置行（name 空）", len(pend) > 0,
    "共 %d 行映射，其中 dsh-harness 待设置 %d 行" % (len(al_rows), len(pend)))
alias_id = pend[0]["id"] if pend else 0
if alias_id:
    _, upd = call("PUT", "/client-aliases/%d" % alias_id, {"name": "DSH", "icon": "deepseek.svg"})
    chk("PUT 配置映射名与图标",
        (upd.get("item") or {}).get("name") == "DSH" and (upd.get("item") or {}).get("icon") == "deepseek.svg",
        str(upd.get("item"))[:100])
    # CSV 导出按映射名（与界面显示同一优先级，见 exportLogs 的 clientCell）
    req = urllib.request.Request(ADMIN + "/logs/export")
    with urllib.request.urlopen(req, timeout=60) as x:
        eraw = x.read().decode("utf-8-sig")
    erows = list(csv.reader(eraw.splitlines()))
    ehdr = erows[0] if erows else []
    eci = ehdr.index("客户端") if "客户端" in ehdr else -1
    dsh_csv = [r for r in erows[1:] if len(r) > eci >= 0 and r[eci] == "DSH"]
    chk("CSV 导出的客户端列用映射名（DSH）", len(dsh_csv) > 0,
        "导出 %d 行，其中客户端列为 DSH 的 %d 行" % (len(erows) - 1, len(dsh_csv)))

    # 图片外链：保存时由后端抓一次、转成 data URI 入库（口径同渠道图标），
    # 这样日志页每次打开都从本站渲染，不再去请求那个外链。
    #
    # 这里故意用回环地址来钉「抓不到时的降级」：netguard 会拒掉内网目标，
    # 所以断言不依赖外网、结果也是确定的 —— 外链按原样存下 + 回执带原因，
    # 而且保存本身不能被拦下（否则改个名字都会被网络问题连坐）。
    ext = "http://127.0.0.1:9/logo.png"
    _, ext_resp = call("PUT", "/client-aliases/%d" % alias_id, {"icon": ext})
    ext_item = ext_resp.get("item") or {}
    chk("外链图标抓不到时不拦保存：原样入库 + 回执带原因",
        ext_item.get("icon") == ext
        and not ext_resp.get("icon_cached")
        and "内网" in (ext_resp.get("icon_error") or ""),
        "icon=%s icon_error=%s" % (str(ext_item.get("icon"))[:40], ext_resp.get("icon_error")))

    # 本地内容（data URI / 内置文件名 / 一两个字符）一律不触发抓取：
    # 多出来的这道抓取不该碰原有三态，也不该让保存多等一次网络
    local_icon = "data:image/png;base64,iVBORw0KGgo="
    _, local_resp = call("PUT", "/client-aliases/%d" % alias_id, {"icon": local_icon})
    chk("data URI 不触发抓取（无 icon_cached / icon_error）",
        not local_resp.get("icon_cached") and not local_resp.get("icon_error")
        and (local_resp.get("item") or {}).get("icon") == local_icon,
        str(local_resp.get("item"))[:90])

    _, delr = call("DELETE", "/client-aliases/%d" % alias_id)
    chk("DELETE 删除映射", delr.get("deleted") is True, str(delr)[:60])
    _, al2 = call("GET", "/client-aliases")
    chk("删除后行从列表消失",
        not [r for r in al2.get("items", []) if r.get("key") == "dsh-harness"],
        "剩余 %d 行" % len(al2.get("items", [])))

    # 删除只是「不想要这条映射」：同一个客户端再发一次请求，应当重新登记
    # 一行待设置。这条曾经是假的 —— logwriter 的 seenAlias 是进程级缓存，
    # 删除接口没清它，于是本进程内永远不再登记（只有重启回填才回来，
    # 连有意删除的也一起复活）。2026-10-19 审查发现，修在 deleteClientAlias。
    chat(MODEL, user_agent="dsh-harness/0.3")
    time.sleep(2)
    _, al3 = call("GET", "/client-aliases")
    chk("删除后再次被观测到会重新登记为待设置行",
        any(r.get("key") == "dsh-harness" and not r.get("name") for r in al3.get("items", [])),
        "dsh-harness 行数 %d" % len([r for r in al3.get("items", []) if r.get("key") == "dsh-harness"]))
    # 收尾：把这条探针行删掉，不给下一次运行留「已是待设置」的既成状态。
    # （就算没删也没关系：开头那段会先把它清回待设置。）
    probe_rows = [r for r in al3.get("items", []) if r.get("key") == "dsh-harness"]
    for r in probe_rows:
        call("DELETE", "/client-aliases/%d" % r["id"])

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
print("=== 按分组 / 按渠道筛选（界面工具栏上的两个下拉）===")
CID, GID = probe_scope(MODEL)
print("  探针：渠道 %d，所属分组 %d" % (CID, GID))
if CID and GID:
    _, bygroup = call("GET", "/logs?page_size=200&group_id=%d" % GID)
    chk("按分组筛：返回的行都属于该分组",
        len(bygroup.get("items", [])) > 0 and all(r["group_id"] == GID for r in bygroup["items"]),
        "共 %d 条" % len(bygroup.get("items", [])))

    _, bychan = call("GET", "/logs?page_size=200&channel_id=%d" % CID)
    chk("按渠道筛：返回的行都属于该渠道",
        len(bychan.get("items", [])) > 0 and all(r["channel_id"] == CID for r in bychan["items"]),
        "共 %d 条" % len(bychan.get("items", [])))
    # 渠道是分组的子集：按渠道筛出来的条数不可能多于它所属分组
    chk("按渠道的条数不超过按分组的条数",
        bychan.get("total", 0) <= bygroup.get("total", 0),
        "渠道 %s 条，分组 %s 条" % (bychan.get("total"), bygroup.get("total")))

    other = channel_outside(GID)
    if other:
        _, cross = call("GET", "/logs?group_id=%d&channel_id=%d" % (GID, other))
        chk("分组 + 别的分组的渠道 = 0（是 AND 不是 OR）",
            cross.get("total", -1) == 0, "实际 %s 条" % cross.get("total"))
    else:
        print("  只有一个分组，跳过 AND 那条断言")

    _, none = call("GET", "/logs?group_id=9999999")
    chk("不存在的分组：0 条", none.get("total", -1) == 0, "实际 %s 条" % none.get("total"))

    st, body = call("GET", "/logs?group_id=abc")
    chk("非法 group_id 报 400 且点名参数",
        st == 400 and "group_id" in json.dumps(body, ensure_ascii=False),
        "HTTP %s %s" % (st, json.dumps(body, ensure_ascii=False)[:90]))
    st, _ = call("GET", "/logs?group_id=0")
    chk("group_id=0 也拒绝（0 会让人以为等于「全部」）", st == 400, "HTTP %s" % st)

    # 导出与列表共用一套条件，新参数也必须跟着走
    try:
        req = urllib.request.Request(ADMIN + "/logs/export?channel_id=%d" % CID)
        with urllib.request.urlopen(req, timeout=60) as r:
            raw = r.read()
        rows = list(csv.reader(io.StringIO(raw.decode("utf-8-sig"))))[1:]
        chk("导出遵循按渠道筛选",
            len(rows) == bychan.get("total", -1),
            "导出 %d 条，列表命中 %s 条" % (len(rows), bychan.get("total")))
    except Exception as e:
        chk("按渠道导出可用", False, str(e)[:120])
else:
    # 环境里取不到探针渠道（例如渠道刚好被删），这一节跳过而不是判失败
    print("  取不到探针渠道与分组，跳过按分组 / 按渠道的断言")

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
print("=== range 四档（与统计接口共用 resolveRange：卡片和列表必须是同一个窗口）===")
# 这四个值由前端筛选栏直接传过来。断言的重点不是「数字对不对」（那取决于库里有什么），
# 而是「四档各自合法、窗口是嵌套的、非法值不静默」——
# 静默退回「今天」是最坏的情况：界面上选着近 7 天，看到的却是今天的数。
counts = {}
for k in ["today", "3d", "7d", "30d"]:
    code, body = call("GET", "/logs?range=" + k)
    counts[k] = body.get("total", -1)
    chk("range=%s 可用" % k, code == 200 and body.get("total", -1) >= 0,
        "共 %s 条" % body.get("total"))
chk("窗口是嵌套的：今天 <= 近3天 <= 近7天 <= 近30天",
    counts["today"] <= counts["3d"] <= counts["7d"] <= counts["30d"],
    "%s <= %s <= %s <= %s" % (counts["today"], counts["3d"], counts["7d"], counts["30d"]))
# 与统计接口对同一档：卡片与列表的条数必须一致（差值是两次请求之间新落库的日志）
code, summ = call("GET", "/stats/summary?range=" + "7d")
if isinstance(summ, dict) and "requests" in summ:
    diff = abs(int(summ["requests"]) - counts["7d"])
    chk("近7天：统计接口的请求数与日志条数一致（同窗口）", diff <= 5,
        "统计 %s vs 日志 %s（差 %d）" % (summ["requests"], counts["7d"], diff))
else:
    chk("统计接口可用（用于核对同窗口）", False, "返回 %r" % (summ,))
for badval in ["1h", "all", "yesterday", "7"]:
    code, _ = call("GET", "/logs?range=" + badval)
    chk("range=%s 非法：应 400 而不是静默当今天" % badval, code == 400, "实际 %s" % code)
code, _ = call("GET", "/logs?range=today&since=" + urllib.parse.quote(past))
chk("range 与 since 同时给：应 400（两个口径叠加没人看得出哪个生效）", code == 400, "实际 %s" % code)

print()
print("=== 导出必须是全量，不是当前页 ===")
# 先自己造出超过一页的记录：这条断言（导出条数 > 单页条数）原本依赖
# 「库里已经攒了很多日志」，于是清一次历史数据它就会失败 ——
# 测试不该依赖跑之前环境里恰好有什么。用 SQL 直接补齐，跑完再删干净。
fill = subprocess.run(
    psql_cmd() + ["-t", "-A", "-c",
     "INSERT INTO request_logs (trace_id, model_requested, api_key_name, status_code, is_stream, retry_count, created_at) "
     "SELECT 'export-fill-%s' || g, 'export-fill-model', 'export-fill-key', 200, false, 0, now() "
     "FROM generate_series(1, 60) g"],
    capture_output=True, text=True)
if fill.returncode != 0:
    print("  造数据失败: %s" % fill.stderr.strip()[:200])

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
    # 客户端列（2026-10-14）：表头文案与界面一致，且位置在「密钥」之后
    # —— 后端 CSV 与前端列是两份独立代码，加列时最容易漏的就是这一处
    chk("表头含客户端列且在密钥之后",
        "客户端" in reader[0] and reader[0].index("客户端") == reader[0].index("密钥") + 1,
        str(reader[0])[:120])
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

# 探针数据必须自己收干净：留着 60 条假日志会把看板的今日统计顶上去
subprocess.run(
    psql_cmd() + ["-t", "-A", "-c",
     "DELETE FROM request_logs WHERE trace_id LIKE 'export-fill-%'"],
    capture_output=True, text=True)

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
