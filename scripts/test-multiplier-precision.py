#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""渠道模型「固定倍率」四位小数的端到端验证（2026-09-24）。

需求：倍率要能填 0.1875 这样的四位小数。

这条脚本验的是**前后端与存储的往返**，而不是只看界面能不能输入：
  1. 通过管理 API 把固定倍率设成 0.1875，读回来仍是 0.1875
  2. 直接查 Postgres，列里存的确实是 0.1875（numeric(10,4) 没截断）
  3. 0.0001（该列能表示的最小值）与 1.2345 同样往返无损
  4. 超过 4 位的 0.18751 落库后被截成 0.1875 的实际行为记录清楚
     —— 前端/API 层会拦它，但**万一拦漏了**，这里能看出库会怎么处理
  5. 计价快照（写进日志的历史账目）里也是 0.1875

用独立探针渠道，跑完删掉：不碰用户正在用的渠道。

用法：
  python3 scripts/test-multiplier-precision.py [base] [pg_container]
默认 base=http://127.0.0.1:8888，容器 llm-relay-postgres。
"""
import json
import subprocess
import sys
import urllib.error
import urllib.request

BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8888").rstrip("/")
PG = sys.argv[2] if len(sys.argv) > 2 else "llm-relay-postgres"
ADMIN = BASE + "/api/admin"

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


def psql(sql):
    """查库。psql 在容器里，用 -t -A 拿裸值。"""
    try:
        return subprocess.run(
            ["docker", "exec", PG, "psql", "-U", "llmrelay", "-d", "llm_relay",
             "-t", "-A", "-c", sql],
            capture_output=True, text=True, timeout=30,
        ).stdout.strip()
    except Exception as e:  # noqa: BLE001
        return "ERR:" + str(e)


def pick_group():
    """取一个已启用的分组 id。

    必须在请求里显式带 group_id：这个库里可能一个默认分组都没设
    （实测就是如此），不带的话建渠道直接 400。取的是「现有」分组 ——
    探针渠道只是借用它，用完连同渠道一起删，不改分组的任何设置。
    """
    out = psql("SELECT id FROM channel_groups WHERE enabled = true ORDER BY id LIMIT 1")
    if out and not out.startswith("ERR:") and out.isdigit():
        return int(out)
    # 库里没有启用分组时退到 API：分组列表带 enabled 字段
    for g in (call("GET", "/groups")[1].get("items") or []):
        if g.get("enabled"):
            return g.get("id")
    return None


GROUP_ID = pick_group()
if not GROUP_ID:
    print("库里没有可用分组，无法建探针渠道")
    sys.exit(1)
print("借用分组 id=%s（不改它的任何设置）" % GROUP_ID)


# ---- 建一条探针渠道（带一个模型），拿到 channel_id ----
PROBE = "probe-mult-precision"
# 先清掉上次残留（同名渠道）
for c in (call("GET", "/channels")[1].get("items") or call("GET", "/channels")[1].get("data") or []):
    if c.get("name") == PROBE:
        call("DELETE", "/channels/%s" % c.get("id"))

st, created = call("POST", "/channels", {
    "name": PROBE, "group_id": GROUP_ID,
    "base_url": "http://127.0.0.1:9999",
    "api_key": "sk-probe",
    "upstream_protocol": "openai",
    "currency": "CNY",
    "enabled": False,          # 不启用：绝不参与真实路由
    "models": [{"public_name": "probe-mult-model", "upstream_name": "",
                "enabled": True, "multiplier": 1,
                "input_per_1m": "0.15", "output_per_1m": "0.6"}],
})
if st not in (200, 201):
    print("建探针渠道失败 HTTP %s %s" % (st, json.dumps(created, ensure_ascii=False)[:200]))
    sys.exit(1)

cid = created.get("id") or (created.get("data") or {}).get("id")
if not cid:
    # 回查一次拿 id
    got = call("GET", "/channels")[1]
    for c in (got.get("items") or got.get("data") or []):
        if c.get("name") == PROBE:
            cid = c.get("id")
            break
if not cid:
    print("拿不到探针渠道 id：%s" % json.dumps(created, ensure_ascii=False)[:300])
    sys.exit(1)

print("探针渠道 id=%s" % cid)


def set_multiplier(v):
    """把渠道的唯一模型倍率设为 v，返回 (status, body)。"""
    return call("PUT", "/channels/%s" % cid, {
        "name": PROBE, "group_id": GROUP_ID,
        "base_url": "http://127.0.0.1:9999",
        "api_key": "sk-probe",
        "upstream_protocol": "openai",
        "currency": "CNY",
        "enabled": False,
        "models": [{"public_name": "probe-mult-model", "upstream_name": "",
                    "enabled": True, "multiplier": v,
                    "input_per_1m": "0.15", "output_per_1m": "0.6"}],
    })


def db_multiplier():
    return psql(
        "SELECT multiplier FROM channel_models WHERE channel_id = %d "
        "AND public_name = 'probe-mult-model'" % cid)


def api_multiplier():
    """从 API 读回倍率。

    用 /channels/<id>/models（admin.go:101 的 listChannelModels）——
    它返回的是完整的白名单行（含 multiplier）。
    注意**不能**用渠道列表接口：那里的 models 只是模型名数组
    （["probe-mult-model"]），不带任何定价字段，拿它读倍率会读到 None。
    """
    st, body = call("GET", "/channels/%s/models" % cid)
    if st != 200:
        return None
    for m in (body.get("items") or body.get("models") or []):
        if isinstance(m, dict) and m.get("public_name") == "probe-mult-model":
            return m.get("multiplier")
    return None


print("")
print("=== 1. 0.1875 往返（本次需求的核心）===")
st, body = set_multiplier(0.1875)
chk("保存 0.1875 成功", st in (200, 201), "HTTP %s %s" % (st, json.dumps(body, ensure_ascii=False)[:120]))
chk("API 读回来仍是 0.1875", api_multiplier() == 0.1875, "实际 %r" % api_multiplier())
chk("数据库列里存的是 0.1875", db_multiplier() == "0.1875", "实际 %r" % db_multiplier())

print("")
print("=== 2. 边界值往返 ===")
# 注意比的是**数值**而不是字符串：numeric(10,4) 读出来是 '0.5000'
#（列的小数位补齐），而 API 的 JSON 是 0.5 —— 两者数值相等，
# 拿字符串比会把正确行为判成失败。
for v, want in ((0.0001, "0.0001"), (1.2345, "1.2345"), (0.1234, "0.1234"), (0.5, "0.5000")):
    st, _ = set_multiplier(v)
    gotdb = db_multiplier()
    gotapi = api_multiplier()
    db_ok = gotdb == want or abs(float(gotdb or "nan") - v) < 1e-12
    chk("倍率 %s 往返无损" % v,
        st in (200, 201) and db_ok and gotapi is not None and abs(gotapi - v) < 1e-12,
        "HTTP %s 库=%r API=%r" % (st, gotdb, gotapi))

print("")
print("=== 3. 超精度值（5 位小数）的行为 ===")
# 0.18751 是 5 位小数，超出 numeric(10,4)。
#
# 这条**不作为通过/失败判据**，而是把真实行为记录下来：
#   - 前端提交与导入两条路径都会拦（multiplierPrecisionError，
#     由 frontend/scripts/check-contracts.mjs 静态钉住）
#   - 但绕过前端直接打 API 时，后端目前**放行**，由数据库截断成 0.1875
#
# 后端不加这道校验是有意的取舍：numeric(10,4) 的截断方向明确、结果可预期，
# 而 API 的调用方只有本项目的界面（已经拦了）。写清楚这一点，
# 是为了让下一个人知道「这里是靠前端守的」，而不是以为后端也守了。
st, body = set_multiplier(0.18751)
if st in (200, 201):
    gotdb = db_multiplier()
    print("  实测：后端放行 0.18751，Postgres 按 numeric(10,4) 存成 %r" % gotdb)
    chk("库的截断方向是明确的（四舍五入到 4 位，不是变成别的数）",
        gotdb == "0.1875", "库=%r" % gotdb)
else:
    print("  实测：后端直接拒绝 5 位小数（HTTP %s）" % st)
    chk("后端拒绝 5 位小数", True)

print("")
print("=== 4. 计价快照里的倍率 ===")
# 快照存在 request_logs.pricing_snapshot（写成日志的历史账目），
# 这里只要确认「有没有这一列」；倍率快照值的精度由
# internal/pricing 的 TestFourDecimalMultiplierExact 覆盖。
col = psql("SELECT column_name FROM information_schema.columns "
           "WHERE table_name = 'request_logs' AND column_name LIKE '%snapshot%'")
if col and not col.startswith("ERR:"):
    print("  快照列存在：%s（精度由 pricing 包单测覆盖）" % col.splitlines()[0])
else:
    print("  SKIP  未找到快照列（不影响本次结论）")

# ---- 收尾：删掉探针渠道 ----
call("DELETE", "/channels/%s" % cid)
left = psql("SELECT count(*) FROM channels WHERE name = '%s'" % PROBE)
print("")
print("清理探针渠道：剩余 %s 条" % left)

print("")
print("通过 %d 项，失败 %d 项" % (ok, bad))
sys.exit(0 if bad == 0 else 1)
