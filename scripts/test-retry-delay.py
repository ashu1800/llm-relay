#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""验证故障转移的「同上游退避」行为。

判据用的是**上游侧记录的相邻两次请求到达间隔**，不是客户端看到的总耗时：
总耗时里混着连接建立、容器调度等噪声，无法区分「等了一会儿」与「上游慢」。
到达间隔是退避是否生效的直接证据 —— 与 test-channel-concurrency.py 用并发
峰值而不是耗时来证明名额语义是同一个判据思想。

三个场景：
  A 同上游的两条渠道都失败 → 第二次尝试前必须等（约 500ms）
  B 不同上游的两条渠道   → 不许等（这是防回归：换上游不该被拖慢）
  C 429 带 Retry-After   → 按上游说的等，且量级正确（不是冷却的 30s）

先拉起 mock（脚本会自己确保它在跑）：
  bash scripts/ensure-flaky-upstream.sh
  bash scripts/ensure-mock-upstream.sh   # 场景 B 需要它（会成功的第二条上游）
"""
import json
import subprocess
import time
import urllib.request

ADMIN = "http://127.0.0.1:8888/api/admin"
RELAY = "http://127.0.0.1:8888/v1/chat/completions"
FLAKY = "http://127.0.0.1:9996"
MODEL = "retry-delay-probe"
GROUP = "retry-delay-probe-group"
KEY = "retry-delay-probe-key"

# 同上游场景的期望区间：默认 500ms ± 25% 抖动 = [375, 625]，
# 再给网络与调度留一点余量
SAME_UPSTREAM_LO, SAME_UPSTREAM_HI = 350, 900
# 不同上游场景必须远小于这个上界（证明没有退避）
DIFF_UPSTREAM_MAX = 300


def call(method, path, body=None, base=ADMIN):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.status, json.loads(r.read().decode() or "{}")


def post_flaky(path, body=None, method="POST"):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(FLAKY + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.loads(r.read().decode() or "{}")


def flaky_stats():
    return json.loads(urllib.request.urlopen(FLAKY + "/stats", timeout=10).read().decode())


def ensure_mocks():
    """确保两个 mock 上游在跑。

    自己拉起而不是写在说明里：容器会随 compose 重建网络被带走，
    届时用例会全变 502，看起来像产品坏了（见 ensure-mock-upstream.sh
    里记录的那次踩坑）。
    """
    import os
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    for script in ("ensure-flaky-upstream.sh", "ensure-mock-upstream.sh"):
        r = subprocess.run(["bash", os.path.join(root, "scripts", script)],
                           capture_output=True, text=True)
        if r.returncode != 0:
            print("拉起 %s 失败：%s" % (script, r.stderr.strip() or r.stdout.strip()))
            raise SystemExit(1)


def cleanup():
    # 顺序有讲究：分组被密钥引用时删除会 409（这是刻意的保护，
    # 见「密钥分组引用」改造），所以必须先删密钥、再删渠道、最后删分组。
    # 渠道也要在分组之前删 —— 它同样引用分组。
    _, ks = call("GET", "/keys")
    for k in ks.get("items", []):
        if k["name"] == KEY:
            call("DELETE", "/keys/%d" % k["id"])
    _, cs = call("GET", "/channels")
    for c in cs.get("items", []):
        if c["name"].startswith("rd-probe-"):
            call("DELETE", "/channels/%d" % c["id"])
    _, gs = call("GET", "/groups")
    for g in gs.get("items", []):
        if g["name"] == GROUP:
            call("DELETE", "/groups/%d" % g["id"])


def make_group():
    _, g = call("POST", "/groups", {"name": GROUP, "strategy": "failover"})
    return g["id"]


def make_channel(gid, name, base_url, weight):
    _, ch = call("POST", "/channels", {
        "name": name, "protocol": "openai-chat", "base_url": base_url,
        "api_key": "test-key", "group_id": gid, "weight": weight,
    })
    cid = ch["id"]
    call("POST", "/channels/%d/models" % cid, {"public_name": MODEL, "upstream_name": MODEL})
    return cid


def make_key():
    _, k = call("POST", "/keys", {"name": KEY})
    return k["key"]


def relay_once(token, timeout=60):
    """发一次请求，返回 (是否成功, 耗时秒)。失败是预期的（渠道都返回 5xx）。"""
    payload = json.dumps({
        "model": MODEL, "max_tokens": 16,
        "messages": [{"role": "user", "content": "hi"}],
    }).encode()
    req = urllib.request.Request(RELAY, data=payload, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + token)
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            r.read()
        return True, time.time() - t0
    except Exception:
        return False, time.time() - t0


def scenario(name, channels, fail_body, lo, hi, expect_success, expect_hits):
    """跑一个场景：清 mock 状态 → 设失败模式 → 发请求 → 断言。

    expect_hits 是 flaky 上游**应该收到的请求数**：
      - 两条渠道都指向 flaky（同上游）→ 2 次（第一次失败后重试同上游）
      - 两条渠道指向不同上游 → 1 次（重试打到了另一个上游，flaky 看不到）
    这个数字本身就是「有没有对同一上游连打」的证据。
    """
    print("=== %s ===" % name)
    cleanup()
    gid = make_group()
    for i, base in enumerate(channels):
        make_channel(gid, "rd-probe-c%d" % (i + 1), base, i + 1)
    token = make_key()

    post_flaky("/reset")
    post_flaky("/fail", fail_body)

    ok, elapsed = relay_once(token)
    stats = flaky_stats()
    gaps = stats.get("gaps_ms", [])
    hits = stats.get("hits", 0)
    print("  请求%s，总耗时 %.2fs，flaky 上游收到 %d 次请求" % (
        "成功" if ok else "失败（预期）", elapsed, hits))
    print("  相邻到达间隔(ms)：%s" % (gaps if gaps else "（只有一次，无间隔）"))

    verdict = True
    if hits != expect_hits:
        print("  [失败] flaky 上游收到 %d 次，预期 %d 次" % (hits, expect_hits))
        verdict = False
    else:
        print("  [通过] flaky 上游收到 %d 次（符合「%s」的预期）" % (
            hits, "对同一上游重试" if expect_hits > 1 else "换到了别的上游"))

    # 间隔只在同上游场景下才有意义（那时 flaky 能看到两次到达）
    if expect_hits > 1:
        if not gaps:
            print("  [失败] 预期能看到两次到达的间隔，实际没有")
            verdict = False
        else:
            g = gaps[0]
            if g < lo or g > hi:
                print("  [失败] 首次间隔 %dms 不在预期区间 [%d, %d]" % (g, lo, hi))
                verdict = False
            else:
                print("  [通过] 首次间隔 %dms 落在预期区间 [%d, %d]" % (g, lo, hi))
    else:
        # 换到不同上游时必须**没有等待**：总耗时应当远小于同上游场景。
        # 用耗时而不是间隔，是因为 flaky 这里只看到一次请求，产生不了间隔。
        if elapsed * 1000 > hi:
            print("  [失败] 换到不同上游却耗时 %.0fms（上界 %dms）—— 说明被加了等待"
                  % (elapsed * 1000, hi))
            verdict = False
        else:
            print("  [通过] 换到不同上游耗时仅 %.0fms（上界 %dms），没有等待"
                  % (elapsed * 1000, hi))

    if ok != expect_success:
        print("  [失败] 请求结果与预期不符（期望%s）" % ("成功" if expect_success else "失败"))
        verdict = False

    cleanup()
    print()
    return verdict


def main():
    allpass = True
    ensure_mocks()

    # A：两条渠道共用同一个上游地址 → 第二次尝试前必须等
    #    flaky 会收到 2 次（首次失败 + 重试仍打同一上游）
    allpass &= scenario(
        "A 同一上游：退避生效",
        ["http://127.0.0.1:9996/v1", "http://127.0.0.1:9996/v1"],
        {"status": 503, "times": 5},
        SAME_UPSTREAM_LO, SAME_UPSTREAM_HI,
        expect_success=False,
        expect_hits=2,
    )

    # B：两条渠道指向不同端口（不同上游）→ 不该等。
    #    这是防回归的核心：换到别的上游时若被加上等待，说明延迟退化成了
    #    「每次都等」，那正是站主方案里要避免的。
    #    flaky 只会收到 1 次 —— 重试打到了 9997（slow-upstream，会成功），
    #    所以这里预期请求成功。
    allpass &= scenario(
        "B 不同上游：不延迟（防回归）",
        ["http://127.0.0.1:9996/v1", "http://127.0.0.1:9997/v1"],
        {"status": 503, "times": 5},
        0, DIFF_UPSTREAM_MAX,
        expect_success=True,
        expect_hits=1,
    )

    # C：429 + Retry-After: 1 → 按上游说的等约 1s（不是基线 500ms，
    #    也不是冷却用的 30s）。区间给 1s ± 抖动与噪声的余量。
    allpass &= scenario(
        "C 429 Retry-After：按上游说的等",
        ["http://127.0.0.1:9996/v1", "http://127.0.0.1:9996/v1"],
        {"status": 429, "times": 5, "retry_after": 1},
        800, 1600,
        expect_success=False,
        expect_hits=2,
    )

    print()
    if allpass:
        print("ALL_PASS")
    else:
        print("HAS_FAILURE")
        raise SystemExit(1)


if __name__ == "__main__":
    main()
