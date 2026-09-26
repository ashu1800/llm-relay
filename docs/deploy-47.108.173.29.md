# 部署记录：47.108.173.29（阿里云 2C1.6G）

> 部署日期：2026-09-25 · 版本 `v0.1.0-55-g862c888` · 形态 `binary`（systemd，无 Docker）

## 一、访问方式

| 项 | 值 |
|---|---|
| 管理后台 | http://47.108.173.29:8888 |
| 管理密钥 | `<见服务器 /opt/llm-relay/deploy/.env，不入库>` |
| 配置文件 | `/opt/llm-relay/deploy/.env`（权限 600） |
| 服务名 | `llm-relay.service`（已设开机自启） |

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

### 1. 静态资源未压缩（带宽浪费 68%）

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

### 2. 无 HTTPS

当前是明文 HTTP。管理密钥与所有 API 密钥都会以明文过公网。
如果只是自用、且经代理访问，风险可接受；否则建议配 nginx + Let's Encrypt。

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
