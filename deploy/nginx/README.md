# nginx 反代 + Let's Encrypt（llm.ashu180.cn）

服务器 47.108.173.29 上 `llm-relay` 以明文监听 `*:8888`，前面这层 nginx 只负责
**TLS 终止、转发、真实客户端 IP 透传**，不碰压缩与缓存（原因见下方「为什么反代
不压缩」）。完整部署记录见 [`docs/deploy-47.108.173.29.md`](../../docs/deploy-47.108.173.29.md)。

## 文件对应关系

| 本目录 | 服务器路径 |
|---|---|
| `llm-relay-upgrade.conf` | `/etc/nginx/conf.d/llm-relay-upgrade.conf` |
| `llm.ashu180.cn.conf` | `/etc/nginx/sites-available/llm.ashu180.cn`（再软链到 `sites-enabled/`） |

## 从零重建（换机或重装时）

```bash
# 1. 建 ACME 挑战目录
mkdir -p /var/www/certbot/.well-known/acme-challenge

# 2. 放配置
install -m 644 llm-relay-upgrade.conf /etc/nginx/conf.d/llm-relay-upgrade.conf
install -m 644 llm.ashu180.cn.conf   /etc/nginx/sites-available/llm.ashu180.cn
ln -sfn /etc/nginx/sites-available/llm.ashu180.cn /etc/nginx/sites-enabled/llm.ashu180.cn

# 3. 签证书（此时 80 端口必须空闲，standalone 会临时占用它）
certbot certonly --standalone -d llm.ashu180.cn \
  --non-interactive --agree-tos --preferred-challenges http

# 4. 校验并加载
nginx -t && systemctl reload nginx
```

## 续期（唯一需要人工留意的坑）

签发用的是 `--standalone`，但**续期必须改成 webroot**：签发时 80 端口还空着，
standalone 能自己起临时监听；nginx 接管 80 之后，standalone 会因端口被占而全年
静默失败，等到证书过期那天整站握手失败。`/etc/letsencrypt/renewal/llm.ashu180.cn.conf`
里已经改成：

```ini
authenticator = webroot
webroot_path = /var/www/certbot,
[[webroot_map]]
llm.ashu180.cn = /var/www/certbot
```

另外两件必须一起做的事：

- **续期后要 reload nginx** —— nginx 只在启动/`reload` 时读证书文件，不 reload
  等于续了也没用。已放 `/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh`
  （`chmod 755`）。
- **`certbot.timer` 要 enable** —— Ubuntu 同时装了 `/etc/cron.d/certbot`，但那份
  cron 带 `\! -d /run/systemd/system` 守卫，**在 systemd 主机上根本不执行**，
  真正的续期调度只有 timer。检查：`systemctl is-enabled certbot.timer`。

验证（用 staging 真跑一遍 HTTP-01，不会消耗正式证书额度）：

```bash
certbot renew --dry-run
systemctl list-timers certbot.timer      # 应有 NEXT 时间
```

## 为什么反代不压缩、不缓存

后端 `backend/internal/api/static.go` 在**构建期**就把前端产物预压缩成 `.gz`
一并 embed，运行时按 `Accept-Encoding` 直接下发压缩体，并自带
`Cache-Control`（带哈希的 `/assets/*` 是 `immutable` 一年，`index.html` 走
`ETag` 校验）。实测 `index.html`：不压缩 2958 字节，带 `Accept-Encoding: gzip`
时 1711 字节 —— 压缩在这一层已经发生过了。

反代再压一遍的代价：2 核机器上白烧 CPU，且会与上游已经带上的
`Content-Encoding` 打架。所以这里的 `proxy_buffering off` 是为了 SSE，
与压缩无关。

## 流式与 WebSocket 相关设置（改之前先读）

| 设置 | 为什么 |
|---|---|
| `proxy_buffering off` | 缓冲会把整段回答攒到最后一次性吐出，首包时间退化为一个完整的上游耗时 |
| `proxy_request_buffering off` | 大 body 直传上游，不在 nginx 落临时文件 |
| `proxy_read_timeout 600s` | 后端 `RELAY_UPSTREAM_TIMEOUT=300s`，反代必须比它长 |
| `$connection_upgrade` map | WS 升级用；写死 `Connection: upgrade` 会让普通请求也被声明成升级 |
| `proxy_set_header X-Forwarded-Proto $scheme` | 后端据此给会话 Cookie 加 `Secure`（`console_auth.go:162`） |
| `proxy_set_header Host $host` | 后端 `sameOriginOnly` 拿 `Origin` 的 host 与 `Request.Host` 比对，Host 传错会让管理台所有写操作 403 |
