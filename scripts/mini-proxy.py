# 一个最小的 HTTP 代理，只给测试用：支持 CONNECT 隧道与绝对 URI 转发。
#
# 为什么自己写一个：代理功能的核心是「请求真的从代理出去了」，
# 拿一个不存在的地址去测只能证明失败路径。要证明成功路径，
# 就得有一个真实可用的代理 —— 而引入一个第三方代理镜像会让
# 验证脚本依赖外部网络（拉镜像）。
import select
import socket
import threading
from urllib.parse import urlparse

PORT = int(__import__("os").environ.get("MINI_PROXY_PORT", "18099"))


def pipe(a, b):
    try:
        while True:
            r, _, _ = select.select([a, b], [], [], 30)
            if not r:
                return
            for s in r:
                data = s.recv(65536)
                if not data:
                    return
                (b if s is a else a).sendall(data)
    except Exception:
        pass
    finally:
        for s in (a, b):
            try:
                s.close()
            except Exception:
                pass


def handle(c):
    try:
        req = c.recv(65536)
        if not req:
            return
        line = req.split(b"\r\n")[0].decode(errors="replace")
        parts = line.split(" ")
        if len(parts) < 3:
            return
        method, target = parts[0], parts[1]
        if method.upper() == "CONNECT":
            host, _, port = target.partition(":")
            up = socket.create_connection((host, int(port or 443)), 10)
            c.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
        else:
            # 明文 HTTP 经代理时请求行是绝对 URI，转发前要改回路径形式
            u = urlparse(target)
            up = socket.create_connection((u.hostname, u.port or 80), 10)
            rest = req.split(b"\r\n", 1)[1]
            path = u.path or "/"
            if u.query:
                path += "?" + u.query
            up.sendall(("%s %s HTTP/1.1\r\n" % (method, path)).encode() + rest)
        pipe(c, up)
    except Exception:
        try:
            c.close()
        except Exception:
            pass


srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("0.0.0.0", PORT))
srv.listen(64)
print("mini proxy listening on %d" % PORT, flush=True)
while True:
    conn, _ = srv.accept()
    threading.Thread(target=handle, args=(conn,), daemon=True).start()
