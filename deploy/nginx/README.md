# nginx 反代 + Let's Encrypt（llm.ashu180.cn）

服务器 47.108.173.29 上 `llm-relay` 以明文监听 `*:8888`，前面这层 nginx 只负责
**TLS 终止、转发、真实客户端 IP 透传**，不碰压缩与缓存（原因见下方「为什么反代
不压缩」）。完整部署记录见 [`docs/deploy-47.108.173.29.md`](../../docs/deploy-47.108.173.29.md)。

## 文件对应关系

| 本目录 | 服务器路径 |
|---|---|
| `llm-relay-upgrade.conf` | `/etc/nginx/conf.d/llm-relay-upgrade.conf` |
| `llm.ashu180.cn.conf` | `/etc/nginx/sites-available/llm.ashu180.cn`（再软链到 `sites-enabled/`） |
| `llm.ashu180.cn-9443.conf` | 同上路径，但用于 36.137.140.126：监听 **9443**，两台机器二选一 |
| `magpie.ashu180.cn.conf` | `/etc/nginx/sites-available/magpie.ashu180.cn`（36.137.140.126，与 llm 共用 9443） |
| `ashu180.top.conf` | `/etc/nginx/sites-available/ashu180.top`（36.137.140.126，标准 **80/443**，`ashu180.top` + `www` 双域名） |

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

## 第二台机器：36.137.140.126（监听 9443，DNS-01 续期）

2026-10-29 起 `llm.ashu180.cn` 的 A 记录指向这台机器。它没有备案，厂商只封
**80 / 443 / 8080 / 8443** 四个端口（实测：这四个外部探测超时，9443、9000、
12345 等其余端口均放行），所以反代监听 9443，站点配置换成
`llm.ashu180.cn-9443.conf`，`llm-relay-upgrade.conf` 原样复用。

| 项 | 值 |
|---|---|
| 管理台 / API | `https://llm.ashu180.cn:9443`、`https://llm.ashu180.cn:9443/v1` |
| 后端 | `llm-relay` 仍是 `0.0.0.0:8888` 明文（保留 IP 直连入口） |
| `SERVER_PUBLIC_ORIGIN` | `https://llm.ashu180.cn:9443`（管理接口锁死这个来源，IP 访问一律 403） |
| 证书 | acme.sh 3.1.1（apt）+ `dns_ali`，ECDSA P-256，装在 `/etc/nginx/ssl/llm.ashu180.cn/` |

### 为什么必须 DNS-01

80 端口被封，`--standalone` / webroot 的 HTTP-01 挑战都打不进来。签发时把阿里云
RAM 的 AccessKey 交给 acme.sh，由它自己增删 `_acme-challenge.llm.ashu180.cn` 的
TXT 记录。凭据落在 `/root/.acme.sh/account.conf`（`SAVED_Ali_Key` / `SAVED_Ali_Secret`，
权限 600），续期调度是 root crontab：

```cron
17 3 * * * "/usr/bin/acme.sh" --cron --home "/root/.acme.sh" >/dev/null 2>&1
```

从零重建：

```bash
apt-get install -y nginx acme.sh
mkdir -p /etc/nginx/ssl/llm.ashu180.cn
install -m 644 llm-relay-upgrade.conf  /etc/nginx/conf.d/llm-relay-upgrade.conf
install -m 644 llm.ashu180.cn-9443.conf /etc/nginx/sites-available/llm.ashu180.cn
ln -sfn /etc/nginx/sites-available/llm.ashu180.cn /etc/nginx/sites-enabled/llm.ashu180.cn

# Ali_Key / Ali_Secret 用只授云解析权限的 RAM 用户
export Ali_Key=... Ali_Secret=...
acme.sh --issue --server letsencrypt --dns dns_ali -d llm.ashu180.cn --keylength ec-256
acme.sh --install-cert -d llm.ashu180.cn --ecc \
  --fullchain-file /etc/nginx/ssl/llm.ashu180.cn/fullchain.pem \
  --key-file       /etc/nginx/ssl/llm.ashu180.cn/privkey.pem \
  --reloadcmd      "systemctl reload nginx"

nginx -t && systemctl reload nginx
```

RAM 用户只需要这几条（`_acme-challenge` TXT 的增删查）：

```json
{
  "Version": "1",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["alidns:AddDomainRecord", "alidns:DeleteDomainRecord",
               "alidns:DescribeDomainRecords", "alidns:DescribeDomainRecordInfo"],
    "Resource": "acs:alidns:*:*:domain/ashu180.cn"
  }]
}
```

### 非标端口必踩的坑：`Host` 要带端口

`proxy_set_header Host $host` 会丢掉非默认端口，浏览器发来的 `Origin` 却是
`https://llm.ashu180.cn:9443`。后端 `sameOriginOnly` 严格比端口
（`backend/internal/api/middleware.go` 的 `sameOriginMatches`：`:8443 ≠ 无端口`），
两者不一致会让管理台**所有写操作 403、读接口全正常**，很容易误判成鉴权问题。
9443 那份配置用的是 `$http_host`。

实测（2026-10-29，公网视角）：首页 200、`/healthz` 200、ALPN `h2`、证书链
`verify=0`、`Origin: https://evil.example` → 403、`/v1/models` 未带密钥 → 401、
WebSocket `/api/admin/live` 未认证 → 401（请求已到后端）。

### 同机第二个站点：magpie.ashu180.cn（共用 9443）

同一台 36.137.140.126 上还跑着一个 [magpie](https://github.com/yetone/magpie)
（模型网关 + 网页控制台）。它和 llm 共用 `listen 9443`，靠 SNI / `server_name`
分流，所以 `magpie.ashu180.cn.conf` 只换了 `server_name`、证书和上游。

```
浏览器/Agent ──HTTPS(9443)──> nginx（按 SNI 分站点）
                  ├─ llm.ashu180.cn    → 127.0.0.1:8888  (llm-relay)
                  └─ magpie.ashu180.cn → 127.0.0.1:3430  (magpie 页面 + 网关)
```

要点（改之前先看）：

| 项 | 值 |
|---|---|
| 进程 | systemd `magpie.service`：`/opt/magpie/magpie serve --web=127.0.0.1:3430`，`User=magpie`、`HOME=/var/lib/magpie` |
| 监听 | 页面 `127.0.0.1:3430`、网关 `127.0.0.1:3425`，**都只绑回环**，外网一律经 nginx |
| 上游只写 3430 | magpie 的页面端口同时回答网关的客户端路由（`/v1/…`、`/v1beta/…`、`/mcp/…`），一个 `location /` 就够 |
| `MAGPIE_PUBLIC_URL` | `https://magpie.ashu180.cn:9443`，控制台与日志里展示的连接示例用它 |
| `MAGPIE_WEB_KEY` | 网页登录密钥，写在 unit 的 `Environment=` 里；登录链接是 `https://magpie.ashu180.cn:9443/?k=<key>`，登录后换成 Cookie |
| 共享开关 | `{"lan": true}` 写进 `/var/lib/magpie/.config/magpie/settings.json`（这个开关只有 GUI 的 `ConfigureLAN` 会写，没有 CLI 命令） |
| 外部网关密钥 | `magpie gateway-key add <名字>` 生成 `sk-magpie-key-…`，客户端用它调 `/v1`；本机回环调用不需要 |
| 证书 | 同一个 acme.sh 账户（`dns_ali`）签 `magpie.ashu180.cn`，DNS-01，无需额外配置 |

`X-Forwarded-For` 那行**别删**：magpie 把带转发头的请求当作「来自另一台机器」，
于是 `/v1` 必须要网关密钥、页面必须登录 —— 反过来（删掉）它会把公网请求当成
本机 Agent，等于开门。实测（2026-10-29，公网）：页面无密钥 401、带 `?k=` 303 后
页面 200、`/api/settings` 带 Cookie 200、`/v1/models` 无密钥 401、带网关密钥 200。

### 标准端口站点：ashu180.top / www.ashu180.top（80 + 443）

`ashu180.top.conf` 是给备案验收准备的标准端口入口：80 只做 ACME 挑战 + 301 到
HTTPS，443 终止 TLS 后反代到 `127.0.0.1:8888`；两个域名共用一张证书
（SAN 双域名，acme.sh DNS-01）。

> **2026-10-31 实测：80/443 仍被厂商封**（大陆出口与海外节点各测一次都是超时，
> 22/9443 正常；服务器本地 `iptables`/`nft` 没有拦，INPUT 策略是 ACCEPT）。
> 服务器侧已就绪：本机 `curl` 443 返回 200、证书链 `verify=0`、80 返回 301，
> **端口一放行即可访问，不需要再改任何配置**。

顺带一笔：为了让管理台能在多个域名上登录，`SERVER_PUBLIC_ORIGIN` 现已留空，
回到「同源即可」的回落校验（`backend/internal/api/middleware.go` 的
`sameOriginMatches`）—— 锁死单一来源时，第二个域名的登录请求会被 403。

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
