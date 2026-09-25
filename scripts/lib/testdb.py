"""测试脚本共用的数据库访问（原生 psql over TCP，无 Docker）。

与 shell 侧的 lib/testdb.sh 同一份连接参数解析规则：
环境变量 RELAY_TEST_DB_* > ${INSTALL_DIR:-/opt/llm-relay}/deploy/.env 的 DB_*
> 默认 127.0.0.1:5432（.env 没有 DB_PORT 键时探测 5432/15432）。

用法：
    import os
    import sys
    sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"))
    from testdb import psql_cmd, psql

    out = psql("SELECT count(*) FROM channels")     # 等价 psql -t -A -c
    subprocess.run(psql_cmd() + ["-c", sql], ...)
"""

import os
import subprocess

_LIB_DIR = os.path.dirname(os.path.abspath(__file__))


def _env_val(key, path):
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if line.startswith(key + "="):
                    return line[len(key) + 1:].strip().strip("\"'")
    except OSError:
        pass
    return ""


def _load():
    global DB_HOST, DB_PORT, DB_USER, DB_NAME, DB_PASSWORD
    env_file = os.path.join(os.environ.get("INSTALL_DIR", "/opt/llm-relay"), "deploy", ".env")
    DB_HOST = os.environ.get("RELAY_TEST_DB_HOST") or _env_val("DB_HOST", env_file) or "127.0.0.1"
    DB_USER = os.environ.get("RELAY_TEST_DB_USER") or _env_val("DB_USER", env_file) or "llmrelay"
    DB_NAME = os.environ.get("RELAY_TEST_DB_NAME") or _env_val("DB_NAME", env_file) or "llm_relay"
    DB_PASSWORD = os.environ.get("RELAY_TEST_DB_PASSWORD") or _env_val("DB_PASSWORD", env_file)
    DB_PORT = os.environ.get("RELAY_TEST_DB_PORT") or _env_val("DB_PORT", env_file)
    if not DB_PORT:
        # 旧 Docker 部署的 .env 没有 DB_PORT 键：探测两个习惯位置
        for p in ("5432", "15432"):
            try:
                r = subprocess.run(
                    ["pg_isready", "-h", DB_HOST, "-p", p, "-t", "2"],
                    capture_output=True, timeout=10,
                    env={**os.environ, "PGPASSWORD": DB_PASSWORD},
                )
                if r.returncode == 0:
                    DB_PORT = p
                    break
            except Exception:  # noqa: BLE001
                pass
    DB_PORT = DB_PORT or "5432"
    # 密码经环境变量交给 libpq：不进 argv（ps aux 看不见）
    os.environ["PGPASSWORD"] = DB_PASSWORD


_load()


def psql_cmd():
    """psql 的连接参数前缀，后面自行接 -c/-f/-t -A 等参数。"""
    return ["psql", "-h", DB_HOST, "-p", DB_PORT, "-U", DB_USER, "-d", DB_NAME]


def psql(sql, timeout=30):
    """执行一条 SQL，返回 stdout（-t -A 口径，去尾空白）。"""
    try:
        return subprocess.run(
            psql_cmd() + ["-t", "-A", "-c", sql],
            capture_output=True, text=True, timeout=timeout,
        ).stdout.strip()
    except Exception:  # noqa: BLE001
        return ""
