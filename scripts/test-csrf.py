#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证管理接口拒绝跨站请求。

背景：管理接口长期没有登录态，浏览器里任意一个网页都能对 localhost 发起请求。
表单提交属于「简单请求」，不触发预检，浏览器不拦、服务端也看不出区别，
一个 <form method="post" action="http://127.0.0.1:8888/api/admin/...">
就足以触发管理操作。

如今管理接口已有密钥登录，但同源校验仍是 CSRF 的第一道防线（排在鉴权之前），
跨站请求在到达登录校验之前就会被 403。本脚本自动适配两种部署形态：
配置了 RELAY_ADMIN_KEY 时先登录带会话，没配置（鉴权关闭）时行为同从前。
"""
import json
import os
import urllib.error
import urllib.request

ADMIN = "http://127.0.0.1:8888/api/admin"
BASE = "http://127.0.0.1:8888"
ok = 0
bad = 0
COOKIE = ""


def load_cookie():
    """有 RELAY_ADMIN_KEY 就登录换会话 Cookie，没有就空串（鉴权关闭）。"""
    key = os.environ.get("RELAY_ADMIN_KEY", "")
    if not key:
        for p in ("deploy/.env", "/opt/llm-relay/deploy/.env"):
            try:
                with open(p, encoding="utf-8") as f:
                    for line in f:
                        if line.startswith("RELAY_ADMIN_KEY="):
                            key = line.split("=", 1)[1].strip()
                            break
            except OSError:
                continue
            if key:
                break
    if not key:
        return ""
    r = urllib.request.Request(
        BASE + "/api/admin/auth/login",
        data=json.dumps({"key": key}).encode(),
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(r, timeout=10) as resp:
            return resp.headers.get("Set-Cookie", "").split(";")[0]
    except Exception:
        return ""


def req(method, url, body=None, ctype=None, origin=None, headers=None, with_cookie=True):
    data = body
    r = urllib.request.Request(url, data=data, method=method)
    if ctype:
        r.add_header("Content-Type", ctype)
    if origin:
        r.add_header("Origin", origin)
    # 会话 Cookie 只给「本站正常使用」的请求带；跨站请求模拟的是攻击者
    # 页面发出的简单请求 —— 那种场景里浏览器虽然会自动带上 Cookie，
    # 但本脚本要验证的是「同源校验」这条独立防线本身
    if with_cookie and COOKIE:
        r.add_header("Cookie", COOKIE)
    for k, v in (headers or {}).items():
        r.add_header(k, v)
    try:
        with urllib.request.urlopen(r, timeout=30) as x:
            return x.status, x.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", "replace")


COOKIE = load_cookie()

EVIL = "http://evil.example"
SELF = "http://127.0.0.1:8888"

print("=== 1. 跨站表单提交必须被拒 ===")
# 用「表单编码」发一个管理操作。这个请求不含自定义头，属于简单请求，
# 浏览器不会预检，正是 CSRF 的实际形态。
st, body = req("POST", ADMIN + "/keys", b"name=csrf-probe",
               ctype="application/x-www-form-urlencoded", origin=EVIL, with_cookie=False)
print("  跨站表单 POST /keys -> HTTP %d  %s" % (st, body[:90].replace("\n", " ")))


def chk(name, cond, detail=""):
    global ok, bad
    if cond:
        ok += 1
        print("  [通过] %s" % name)
    else:
        bad += 1
        print("  [失败] %s  %s" % (name, detail))


chk("被拒绝（403）", st == 403, "实际 %d —— 说明请求到达了业务处理函数" % st)
chk("拒绝原因可读", "跨站" in body, body[:90])

print()
print("=== 2. 跨站读取也必须被拒 ===")
st, _ = req("GET", ADMIN + "/channels", origin=EVIL, with_cookie=False)
chk("跨站 GET /channels -> 403", st == 403, "实际 %d" % st)
st, _ = req("DELETE", ADMIN + "/keys/999999", origin=EVIL, with_cookie=False)
chk("跨站 DELETE -> 403", st == 403, "实际 %d" % st)
st, _ = req("GET", ADMIN + "/channels", origin="null", with_cookie=False)
chk("Origin: null -> 403", st == 403, "实际 %d" % st)
st, _ = req("GET", ADMIN + "/channels", origin="http://localhost:9999", with_cookie=False)
chk("端口不同的来源 -> 403", st == 403, "实际 %d" % st)

print()
print("=== 3. 正常使用不能受影响 ===")
st, _ = req("GET", ADMIN + "/channels")
chk("不带 Origin（curl / SDK）-> 200", st == 200, "实际 %d" % st)
st, _ = req("GET", ADMIN + "/channels", origin=SELF)
chk("同源 Origin -> 200", st == 200, "实际 %d" % st)
st, _ = req("GET", ADMIN + "/channels", origin="http://localhost:8888")
chk("同源但写成 localhost -> 放行或拒绝都合理", st in (200, 403), "实际 %d" % st)

# 用 JSON 正常建一把密钥再删掉，确认写操作没被误伤
st, body = req("POST", ADMIN + "/keys", json.dumps({"name": "csrf-ok-probe"}).encode(),
               ctype="application/json", origin=SELF)
chk("同源 JSON 写入 -> 200", st == 200, "%d %s" % (st, body[:80]))
kid = None
if st == 200:
    kid = json.loads(body).get("id")
    st2, _ = req("DELETE", ADMIN + "/keys/%d" % kid, origin=SELF)
    chk("同源删除 -> 200", st2 == 200, "实际 %d" % st2)

print()
print("=== 4. 中继接口不受影响（中间件只挂在管理组上）===")
st, body = req("POST", BASE + "/v1/chat/completions",
               json.dumps({"model": "no-such-model-csrf", "messages": []}).encode(),
               ctype="application/json", origin=EVIL, with_cookie=False)
print("  中继端点跨站 -> HTTP %d  %s" % (st, body[:80].replace("\n", " ")))
chk("中继端点不返回 403（不受该中间件影响）", st != 403,
    "实际 %d —— 中继接口被误伤了" % st)

print()
print("通过 %d 项，失败 %d 项" % (ok, bad))
print("ALL_PASS" if bad == 0 else "HAS_FAILURE")
