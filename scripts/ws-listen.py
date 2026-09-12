# 一个最小的 WebSocket 客户端，只用来验证实时推送：连上、收若干秒、打印统计。
#
# 为什么自己写而不是装 websocket-client：这个用例只需要「能握手 + 能读文本帧」，
# 而验证脚本跑在 WSL 里（没有 node），引入一个只在测试里用到的第三方包
# 会让「跑一次验证」多一道安装步骤。
import base64
import json
import os
import socket
import sys
import time

HOST = os.environ.get("LIVE_HOST", "127.0.0.1")
PORT = int(os.environ.get("LIVE_PORT", "8888"))
SECONDS = float(sys.argv[1]) if len(sys.argv) > 1 else 6.0


def read_exact(sock, n, buf):
    while len(buf) < n:
        chunk = sock.recv(65536)
        if not chunk:
            raise EOFError("连接已关闭")
        buf += chunk
    return buf[:n], buf[n:]


def main():
    key = base64.b64encode(os.urandom(16)).decode()
    sock = socket.create_connection((HOST, PORT), timeout=10)
    req = (
        "GET /api/admin/live HTTP/1.1\r\n"
        "Host: %s:%d\r\n"
        "Upgrade: websocket\r\n"
        "Connection: Upgrade\r\n"
        "Sec-WebSocket-Key: %s\r\n"
        "Sec-WebSocket-Version: 13\r\n"
        # 同源校验中间件会看这个头：不带就握手失败（403）
        "Origin: http://%s:%d\r\n\r\n" % (HOST, PORT, key, HOST, PORT)
    )
    sock.sendall(req.encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        chunk = sock.recv(4096)
        if not chunk:
            print("HANDSHAKE_FAIL 连接被关闭")
            return 1
        buf += chunk
    head, buf = buf.split(b"\r\n\r\n", 1)
    status = head.split(b"\r\n")[0].decode(errors="replace")
    if "101" not in status:
        print("HANDSHAKE_FAIL " + status)
        return 1
    print("HANDSHAKE_OK")

    counts = {}
    log_ids = []
    stats_fields = 0
    deadline = time.time() + SECONDS
    sock.settimeout(1.0)
    while time.time() < deadline:
        try:
            header, buf = read_exact(sock, 2, buf)
        except (socket.timeout, EOFError):
            continue
        b1, b2 = header[0], header[1]
        opcode = b1 & 0x0F
        length = b2 & 0x7F
        if length == 126:
            raw, buf = read_exact(sock, 2, buf)
            length = int.from_bytes(raw, "big")
        elif length == 127:
            raw, buf = read_exact(sock, 8, buf)
            length = int.from_bytes(raw, "big")
        masked = b2 & 0x80
        if masked:
            mask, buf = read_exact(sock, 4, buf)
        payload, buf = read_exact(sock, length, buf)
        if masked:
            payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        if opcode == 0x8:
            print("CLOSED_BY_SERVER")
            break
        if opcode == 0x9:  # ping -> pong
            sock.sendall(b"\x8a\x80" + os.urandom(4))
            continue
        if opcode != 0x1:
            continue
        try:
            msg = json.loads(payload.decode("utf-8"))
        except Exception:
            counts["BAD_JSON"] = counts.get("BAD_JSON", 0) + 1
            continue
        t = msg.get("type", "?")
        counts[t] = counts.get(t, 0) + 1
        if t == "stats" and isinstance(msg.get("data"), dict):
            stats_fields = len(msg["data"])
        if t == "logs" and isinstance(msg.get("data"), list):
            log_ids.extend(x.get("id") for x in msg["data"])

    print("TYPES " + json.dumps(counts, ensure_ascii=False))
    print("STATS_FIELDS %d" % stats_fields)
    print("LOG_IDS " + json.dumps([i for i in log_ids if i is not None]))
    sock.close()
    return 0


sys.exit(main())
