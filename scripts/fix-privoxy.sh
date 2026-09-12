#!/usr/bin/env bash
# 修复 privoxy 配置（去重 listen-address）并启动
set -uo pipefail
log() { printf '\n\033[1;36m[FIX]\033[0m %s\n' "$*"; }

log "1/4 查看当前 listen-address"
grep -n '^listen-address' /etc/privoxy/config

log "2/4 去重：只保留一行 127.0.0.1:8118"
python3 - <<'PY'
p='/etc/privoxy/config'
lines=open(p,encoding='utf-8',errors='replace').read().splitlines()
out=[];seen=False
for ln in lines:
    if ln.startswith('listen-address'):
        if seen:
            continue
        out.append('listen-address  127.0.0.1:8118')
        seen=True
    else:
        out.append(ln)
open(p,'w',encoding='utf-8').write('\n'.join(out)+'\n')
print('  已重写')
PY
grep -n '^listen-address' /etc/privoxy/config

log "3/4 校验配置语法"
privoxy --config-test /etc/privoxy/config 2>&1 | tail -5 || true

log "4/4 启动 privoxy"
systemctl restart privoxy
sleep 2
systemctl is-active privoxy
journalctl -u privoxy -n 8 --no-pager 2>&1 | tail -8

echo "--- 代理探测 ---"
curl -s -o /dev/null -w "privoxy -> registry-1: HTTP %{http_code}\n" -m 30 -x http://127.0.0.1:8118 https://registry-1.docker.io/v2/ || echo "registry-1 探测失败"
curl -s -o /dev/null -w "privoxy -> auth.docker: HTTP %{http_code}\n" -m 30 -x http://127.0.0.1:8118 https://auth.docker.io/token || echo "auth 探测失败"
echo "FIX_DONE"
