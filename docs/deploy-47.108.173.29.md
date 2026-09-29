# 部署记录：47.108.173.29（阿里云 2C1.6G）

> 部署日期：2026-09-25 · 版本 `v0.1.0-55-g862c888` · 形态 `binary`（systemd，无 Docker）
> 后续变更：2026-09-29 接入域名 `llm.ashu180.cn` 并启用 HTTPS（见「八、域名与 HTTPS」）

## 一、访问方式

| 项 | 值 |
|---|---|
| **管理后台（推荐）** | **https://llm.ashu180.cn** |
| **API 基地址（推荐）** | **https://llm.ashu180.cn/v1** |
| 管理后台（直连，明文） | http://47.108.173.29:8888 |
| 管理密钥 | `<见服务器 /opt/llm-relay/deploy/.env，不入库>` |
| 配置文件 | `/opt/llm-relay/deploy/.env`（权限 600） |
| 服务名 | `llm-relay.service`（已设开机自启） |
| 反代 / 证书 | nginx 1.24 + Let's Encrypt（`llm.ashu180.cn`，到期 2026-12-28） |

管理密钥首次登录时输入，浏览器换取 7 天会话。**泄露后**改 `.env` 里的
`RELAY_ADMIN_KEY` 再 `systemctl restart llm-relay`，所有已登录会话立即失效。

## 二、为什么不用仓库里的 install.sh

仓库的 `deploy/install.sh` 与 `deploy/install-bare.sh` 都会**在服务器上现装
Go/Node 并编译前端**。这台机器只有 2 核 1.6G，还要同时养着 MySQL（~420MB）、
`basic`(9000)、`coding-plan`(8080)、Docker 和阿里云盾 —— 跑 `npm ci` + `go build`
会把它压到无响应。所以改成：

```
本地交叉编译（GOOS=linux GOARCH=amd64）
  → gzip 压缩（20.4MB → 7.5MB）
  → scp 上传（实测 4.08 MB/s）
  → 服务器解压校验架构 → 原子替换 → restart
```

服务器上**一个编译工具都没装**（只有 apt 装的 PostgreSQL）。

## 三、部署做了什么

### 1. PostgreSQL 16（apt 安装，非 Docker）

装完立刻做了低内存调优，写在 `/etc/postgresql/16/main/conf.d/99-llm-relay-tuning.conf`
（独立文件，不散落进主配置，回滚只需删这一个文件）：

| 参数 | 值 | 为什么 |
|---|---|---|
| `shared_buffers` | 128MB | 1.6G 机器上的合理上限；库只有几 MB |
| `work_mem` | 2MB | 后端 `SetMaxOpenConns(40)`，40×2MB=80MB 仍在预算内 |
| `max_connections` | 60 | 后端最多 40，留 20 给 psql/备份/autovacuum |
| `max_parallel_workers_per_gather` | 1 | **2 核上并行查询是负优化**，会为一条统计查询占满 CPU |
| `checkpoint_completion_target` | 0.9 | 摊平刷盘，避免周期性 I/O 尖峰导致整站卡顿 |
| `effective_cache_size` | 320MB | 默认的 4GB 会让规划器选大哈希连接 → 直接换页 |

数据库 `llm_relay`、用户 `llmrelay`，密码随机生成存 `.env`。

### 2. systemd 单元（防卡死的关键）

```ini
MemoryMax=420M     # 硬上限：超了 cgroup 只杀它自己，不拖垮 MySQL
MemoryHigh=380M    # 软上限：先触发回收减速
CPUWeight=50       # 默认 100，调低一半 —— 不与 basic/coding-plan 抢 CPU
CPUQuota=150%      # 最多 1.5 核，防止突发流量占满 2 核
```

按 2 核 1.6G 收紧的运行参数（写在 `.env`）：
`RELAY_MAX_CONCURRENCY=16`（默认 64）、`RELAY_MAX_REQUEST_BODY_MB=32`（默认 64）、
`RELAY_PAYLOAD_MAX_KB=128`（默认 256）、`RELAY_LOG_RETENTION_DAYS=14`（默认 30）。

### 3. 部署脚本（都留在服务器 `/opt/llm-relay/deploy/`）

| 脚本 | 用途 |
|---|---|
| `setup-db.sh` | 建库建用户（幂等，复用已有密码） |
| `install-server.sh` | 完整部署：装二进制 + 写 .env + 注册 systemd |
| `swap-binary.sh` | 只换二进制并重启（配置不变） |
| `verify-server.sh` | 静态资源/登录/鉴权验证 |
| `e2e-verify.sh` | 端到端：建渠道 → 发密钥 → 对话 → 查计费 |
| `verify-billing.sh` | 计费链路复验（token → 单价 → 金额） |
| `stress-test.sh` | 压测：并发打请求，看内存与其它服务可用性 |
| `clean-testdata.sh` | 清掉验证产生的测试数据 |
| `mock-upstream.py` | 极简 OpenAI 兼容 mock 上游（标准库，无依赖） |

## 四、验证结果

### 功能（端到端，全通过）

```
非流式对话     客户端 → 中转 → mock 上游 → 200，响应体来自上游 ✓
流式 SSE       data: [DONE] 正常收到，14 行事件 ✓
协议转换        Anthropic /v1/messages 入站 → 200，响应转成 Anthropic 格式 ✓
计费           11 in + 7 out，单价 10/20 每百万 CNY
               → expected 0.00025，实际 0.00025 ✓
请求日志       token / 状态码 / 币种 / 金额全部落库 ✓
鉴权           无密钥 401、错误密钥 401、未登录管理接口 401 ✓
统计接口       summary/timeseries/models/channels/heatmap 全 200 ✓
备份导出       /backup/export 200 ✓
未知模型       返回明确的 502「没有可用的渠道」而非挂起 ✓
```

### 资源（这是"别把服务器卡死"的答卷）

| 指标 | 结果 |
|---|---|
| 服务常驻内存 | **7.9 MB**（上限 420MB，余量 98%） |
| 压测峰值内存 | **15.9 MB**（12000 请求后） |
| 压测时其它服务 | basic:9000 → 200，coding-plan:8080 → 200（全程可用） |
| OOM 记录 | 无 |
| 压测后负载 | load 从 29 → **0.02**（完全回落） |
| 磁盘 | 40G 用 27%，余 28G |

压测方式：60 并发 × 200 轮 = 12000 请求，耗时 46.5 秒。

### 公网可达

```
http://47.108.173.29:8888/healthz        → 200 ✓
http://47.108.173.29:8888/               → 200，title "LLM Relay" ✓
http://47.108.173.29:8888/console/channels → 200（SPA 深链回退）✓
```

安全组 8888 端口已放行（部署前用临时探针实测确认）。

## 五、已知问题与后续优化建议

> **状态（2026-09-29 更新）**：本节 1、2 两条均已落地。
> 1 由**后端构建期预压缩**解决（比反代 gzip 更省 CPU，见「八」）；
> 2 由 **nginx + Let's Encrypt** 解决。下面保留原始分析，便于回看当时的判断依据。

### 1. 静态资源未压缩（带宽浪费 68%）——已解决，走的是后端预压缩

当前服务器**不对静态资源做 gzip**，也没发 `Cache-Control`/`ETag`：

| 项 | 实测 |
|---|---|
| 前端资源总量 | 1269 KB（41 个文件） |
| gzip 后 | 410 KB |
| **可节省** | **68%** |
| 首次加载（按 3 Mbps） | 3.46s → **1.12s** |
| 实测出方向带宽 | 5.4 Mbps |

其中 `antd-BkC0MikR.js` 单文件 885KB，gzip 后 264KB（省 70%）。

**成因**：`backend/internal/api/server.go` 的 `registerStatic` 直接用
`http.FileServer` 提供嵌入资源，没有压缩中间件；文件名虽带内容哈希
（`index-DY4A6hDq.js`，本质 immutable），但也没配缓存头，所以**每次刷新
都重下全量**。

**建议**（任选其一，都需要重新构建前端产物）：
- 在 `registerStatic` 前挂一个 gzip 中间件（需加 `gin-contrib/gzip` 依赖），
  并对 `/assets/*` 设 `Cache-Control: public, max-age=31536000, immutable`；
- 或在前面加一层 nginx 反代专门做压缩与缓存（服务器上 nginx 已在跑，
  但当前没有任何站点配置）。
- 顺带能加 HTTPS（用 `certbot`，服务器上已有 `.acme.sh`）。

**注意**：这台机器 CPU 弱，开启 gzip 后每个请求要多花 CPU 压缩。
考虑到资源是**构建期就固定**的，更好的做法是在构建时预压缩成 `.gz` 一并
embed，运行时只做选择 —— 但那需要改 embed 逻辑。

### 2. 无 HTTPS ——已解决（nginx 反代 + Let's Encrypt）

当前是明文 HTTP。管理密钥与所有 API 密钥都会以明文过公网。
如果只是自用、且经代理访问，风险可接受；否则建议配 nginx + Let's Encrypt。

**落地情况**：2026-09-29 已按此建议接入 `https://llm.ashu180.cn`，
详见「八、域名与 HTTPS」。原来的明文入口 `http://47.108.173.29:8888`
保持可用（未动），是否关掉由安全组决定 —— 关之前先确认没有客户端还在用它。

### 3. 与既有服务共存的边界

这台机器已经跑着别人的服务，本服务**只新增**了：
- `llm-relay.service` 与 `/opt/llm-relay/`
- PostgreSQL 16（apt 装，端口 5432 **只监听回环**，未对外暴露）
- `/etc/postgresql/16/main/conf.d/99-llm-relay-tuning.conf`

**未改动**：MySQL（3306）、Redis（6379）、basic（9000）、coding-plan（8080）、
Docker、nginx 的现有配置。MySQL 的 3306 与 PostgreSQL 的 5432 不冲突
（后者只绑 `127.0.0.1`）。

## 六、日常运维

```bash
# 看状态与资源占用
systemctl status llm-relay | grep -E "Memory|CPU|Active"
curl -s localhost:8888/healthz
curl -s -o /dev/null -w '%{http_code}\n' localhost:8888/readyz

# 看日志
journalctl -u llm-relay -f
journalctl -u llm-relay --since "10 min ago"

# 重启 / 停止
systemctl restart llm-relay
systemctl stop llm-relay        # 只停中转，数据库继续跑

# 数据库
su - postgres -c "psql -d llm_relay"

# 备份（备份文件含渠道密钥密文，请妥善保存）
su - postgres -c "pg_dump -d llm_relay" > /root/llm-relay-$(date +%F).sql

# 证书（nginx + Let's Encrypt）
nginx -t && systemctl reload nginx                # 改完反代配置
certbot certificates                              # 看证书与到期日
certbot renew --dry-run                           # 演练续期（staging，不耗额度）
systemctl list-timers certbot.timer               # 确认续期调度存在
journalctl -u certbot.service --since "7 days ago"  # 看续期历史
curl -sI https://llm.ashu180.cn/ | head -1        # 线上冒烟
```

### 重新部署（改了代码之后）

```powershell
# 本地：构建 + 压缩
pwsh -File .tmp-deploy\build-server.ps1

# 上传
scp .tmp-deploy\llm-relay.gz root@47.108.173.29:/tmp/llm-relay.gz

# 服务器：热替换（配置与数据库不动）
bash /opt/llm-relay/deploy/swap-binary.sh
```

**不要**直接在服务器上编译。若必须，先 `systemctl stop` 掉不重要的服务
并盯住 `free -m`。

## 七、踩过的坑（留给下次）

1. **`-dirty` 版本号是假的**：构建脚本调 WSL 的 bash → WSL 的 git，
   而仓库在 `/mnt/c` 上，drvfs 把文件权限位报成 777，git 判定"全部已修改"，
   `git describe --dirty` 于是多出一个假后缀。**必须用 Windows 侧 git**
   （`C:\Program Files\Git\cmd\git.exe`）。
2. **上传快 ≠ 下载快**：scp 实测 4.08 MB/s，但那是**入方向**（阿里云通常不限速）。
   出方向实测 5.4 Mbps，才是用户访问时的真实瓶颈 —— 所以静态资源体积很重要。
3. **`file` 校验要加**：交叉编译产物若架构不对，服务起不来时线上是空的。
   部署脚本里已加 `ELF 64-bit LSB.*x86-64` 断言。
4. **压测的 load 会滞后**：压完看到 load 29 不要慌，那是 1 分钟均值还没衰减；
   看 `/proc/loadavg` 的第四段（运行队列 `1/291` = 只有 1 个可运行进程）才准。
5. **`pkill -9 -f` 会被安全策略拦截**（含删除语义），改用 `pkill -f`。
6. **`pkill -f "http.server 80"` 会杀掉自己**：探针脚本的命令行里也含这个字符串，
   匹配到了发起它的那个 shell。要加方括号写成 `pkill -f 'http[.]server (80|443)'`。
7. **nginx 1.24 不认 `http2 on;`**：那是 1.25.1 才引入的写法，1.24 上必须写
   `listen 443 ssl http2;`，否则 `nginx -t` 直接报 unknown directive。
8. **`--standalone` 签发，续期就得改 webroot**：签发时 80 端口空着，standalone
   能自己起临时监听；nginx 一接管 80，standalone 续期会因端口被占而**静默失败**，
   直到证书过期那天全站握手失败才暴露。改法见 `deploy/nginx/README.md`。
9. **`certbot.timer` 默认可能是 disabled**：Ubuntu 同时装了 `/etc/cron.d/certbot`，
   但那份 cron 带 `\! -d /run/systemd/system` 守卫，**systemd 主机上根本不执行**。
   `certbot renew --dry-run` 通过 ≠ 续期会自动发生，要
   `systemctl is-enabled certbot.timer` 确认。
10. **`ssh_exec` 不带 `connection_id` 用的是「当前连接」**：中途切过 `ssh_connect_profile`
    后，后续命令会跑在另一台机器上。本次排查 443 端口时因此把两台机器的结果混在
    一起，一度误判「443 被安全组拦死」——实际早就放行了。**多条服务器并行操作时，
    每条命令都显式传 connection_id。**

## 八、域名与 HTTPS（2026-09-29）

### 目标与形态

把 `llm.ashu180.cn`（A 记录已指向 47.108.173.29）接到这台机器上的 llm-relay
并申请 HTTPS 证书。域名与证书只服务本网关，**不动同机其它服务**。

```
客户端 ──HTTPS(443)──> nginx 1.24 ──HTTP(127.0.0.1:8888)──> llm-relay
                          └─ 80：/.well-known/acme-challenge/ 供续期，其余 301 跳 HTTPS
```

nginx 只做三件事：**TLS 终止、转发、真实客户端 IP 透传**。配置归档在
[`deploy/nginx/`](../deploy/nginx/)（站点配置 + WebSocket map + 安装续期说明）。

### 关键决策与理由

| 决策 | 理由 |
|---|---|
| 反代**不启用 gzip、不做静态缓存** | 后端 `static.go` 已在构建期把前端产物预压缩成 `.gz` 并自带 `Cache-Control`/`ETag`；再压一遍在 2 核机器上是白烧 CPU，还会与上游 `Content-Encoding` 打架 |
| `proxy_buffering off` | 缓冲会把整段回答攒到最后一次性吐出，首包时间直接退化成一个完整的上游耗时 |
| `proxy_read_timeout 600s` | 后端 `RELAY_UPSTREAM_TIMEOUT=300s`，反代必须比它长，否则先断的是 nginx |
| `Connection` 用 map，不写死 `upgrade` | 写死会让没有 Upgrade 头的普通请求也被声明成升级 |
| `proxy_set_header Host $host` | 后端 `sameOriginOnly` 拿 `Origin` 的 host 与 `Request.Host` 比对，Host 传错会让管理台所有写操作 403 |
| `X-Forwarded-Proto $scheme` | 后端据此判断是否给会话 Cookie 加 `Secure`（`console_auth.go:162`） |
| `client_max_body_size 40m` | 比 `RELAY_MAX_REQUEST_BODY_MB=32` 留余量，超限时回后端自己的 JSON 错误体 |
| HSTS 不加 `includeSubDomains`/`preload` | 这台机器上还有别的服务，不替整站立规矩 |
| 保留明文 `:8888` 入口 | 现有客户端可能还在用它；是否关闭属于安全组操作，另议 |

### 证书与续期

```
签发    certbot certonly --standalone -d llm.ashu180.cn（2026-09-29，ECDSA）
路径    /etc/letsencrypt/live/llm.ashu180.cn/{fullchain,privkey}.pem
到期    2026-12-28（Let's Encrypt 90 天）
续期    authenticator = webroot，目录 /var/www/certbot（**已从 standalone 改过来**）
调度    certbot.timer（每日 00:00、12:00 检查；原为 disabled，已 enable）
钩子    /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh（续期成功后 reload nginx）
```

`certbot renew --dry-run` 已用 staging 真跑过一遍 HTTP-01 挑战，结果
`all simulated renewals succeeded`。

### 验证结果

外部视角（大陆腾讯云 106.12.28.144、海外 Vultr 66.42.99.110 各一遍）：

| 检查项 | 结果 |
|---|---|
| `http://llm.ashu180.cn/` | 301 → HTTPS ✓ |
| `https://llm.ashu180.cn/` | 200，`ssl_verify_result=0`（证书链完整）✓ |
| TLS 握手耗时 | 大陆 0.094s / 海外 0.635s，HTTP/2 协商成功 ✓ |
| TLS 1.0 / 1.1 | 拒绝（`no protocols available`）✓ |
| 证书主体 | `CN=llm.ashu180.cn`，issuer Let's Encrypt，到期 2026-12-28 ✓ |

应用层（走公网域名，逐项实测）：

| 检查项 | 结果 |
|---|---|
| 会话 Cookie | `console_session=…; Path=/; Max-Age=604800; HttpOnly; **Secure**; SameSite=Lax` —— `Secure` 生效即证明 `X-Forwarded-Proto: https` 传对了 ✓ |
| 登录 / 管理接口 | 登录 200；`system/info`、`keys`、`logs` 全 200 ✓ |
| 跨站 Origin | `Origin: https://evil.example` → **403**，`sameOriginOnly` 经反代仍生效 ✓ |
| WebSocket `/api/admin/live` | `HTTP/1.1 101 Switching Protocols` + `Connection: upgrade` ✓ |
| `/v1/models` | 200，正常返回上游模型列表 ✓ |
| 流式 SSE | `first_byte=3.18s` vs `total=4.80s`，83 个事件分块到达 → **未被缓冲** ✓ |
| 静态资源预压缩 | `Accept-Encoding: gzip` 时 index.html 1711 字节（不压缩 2958 字节）✓ |
| 未匹配路径 | 返回后端 JSON 404 而非 nginx HTML 404 → 确证请求到了后端 ✓ |

验证用的临时 API 密钥（`__https-verify-tmp__`、`__sse-buffering-check__`）已删除，
会话 Cookie 与本地凭据文件已清空。

### 开销

nginx master + 2 worker 合计 RSS 约 23.6 MB，`systemctl` 口径 Memory 4.1 MB
（峰值 6.7 MB）—— 对 1.6G 的机器可忽略。llm-relay 本体的内存与重启次数不受影响。
