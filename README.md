# LLM Relay

[![CI](https://github.com/ashu1800/llm-relay/actions/workflows/ci.yml/badge.svg)](https://github.com/ashu1800/llm-relay/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ashu1800/llm-relay)](https://github.com/ashu1800/llm-relay/releases)

个人自用优先的大模型 API 网关：多协议入站、按密钥的「模型 → 渠道优先级」转发、
按渠道 × 模型计价。单个裸二进制运行（Linux 交给 systemd，Windows 注册为系统服务），
前端产物 embed 进二进制，装好即带 Web 管理台。

> **2026-10-26 破坏性更新**：分组功能整体移除。每把密钥现在直接为选用的模型
> 指定有序候选渠道（不选模型不允许创建密钥）；轮询 / 最低延迟策略、分组 RPM/TPM、
> 分组日预算随之删除。旧库升级时自动把存量密钥按当时的渠道权重生成路由。

- 后端 Go 1.25 + Gin，前端 Vue 3 + Ant Design Vue 4，PostgreSQL 存储
- 默认监听 `127.0.0.1:8888`

## 特性

### 转发与协议

- **多协议入站**：OpenAI Chat Completions / Responses、Anthropic Messages、Gemini generateContent、Embeddings
- **渠道协议 = 上游协议**：入站先归一成 OpenAI Chat，再按渠道的 `protocol` 转成
  上游格式（`anthropic-messages` / `gemini-generateContent` / `openai-*` / `custom`
  自定义鉴权头），鉴权头按协议自动选择（`x-api-key`、`x-goog-api-key`、`Bearer`）
- **故障转移**：候选链由密钥自己决定 —— 每把密钥为选用的模型指定有序候选渠道，
  按顺序依次尝试；上游非 2xx 立即切换下一条；429 按 `Retry-After` 冷却（封顶 8 秒）、
  401/403 冷却 30 秒；请求物理尺寸超限（413/414/431）短路返回；换到同一台上游前
  留间隔（`RELAY_RETRY_SAME_UPSTREAM_DELAY`，默认 500ms）；连续失败 3 次自动
  熔断 60 秒，成功一次即恢复
- **出站代理**：socks5 / http / https，渠道级与模型级都可指定（模型 > 渠道 > 直连）；
  代理不可用直接失败、**绝不静默回退直连**；可勾选「用于自动更新」，让版本检测
  与下载走代理
- **限流与并发**：密钥级每分钟配额、渠道并发上限、全局在途闸门；上游 429 自动
  冷却并切走

### 计量与成本

- **精细计量**：输入 / 输出 / 缓存命中 / 缓存写入 / 推理 Token，区分「子集型」与
  「并列型」缓存口径
- **价格按模型设置**（「模型定价」菜单）：同一个模型可同时填**美元与人民币两套**
  单价，互不换算；记账时按所属渠道的币种取对应那一档。单位是每 100 万 token
- **渠道只保留倍率**：固定倍率与按时段倍率（4 位小数，按服务器本地时区判断，
  时段优先）表达「这条渠道比标价贵/便宜多少」。漏配价的模型会在定价页与渠道
  列表点名「N 个未定价」—— 漏配的后果是这笔调用被记成 0 元，账面上看不出异常
- **多币种不换算**：币种是渠道属性，日志逐条显示自己的币种，看板按币种并列展示、
  永不合计 —— 「哪条渠道在烧钱」不需要回答汇率从哪来
- **无充值 / 无金额系统**：金额仅作成本感知，不做任何扣减

### 管理台

- **数据看板**：时间 / 密钥 / 渠道 / 模型筛选（条件本地持久化，概览卡与请求日志
  列表共用同一组条件），WebSocket 实时推送，数字滚动过渡、新请求日志即到即显
- **请求日志**：首包时间、总耗时、状态码、模型映射、计费过程还原、原始报文
  （`all` / `errors` / `none` 三档留存，凭据自动脱敏）；耗时 / 费用 / 状态服务端
  排序；`?trace_id=…` 与 `?status_class=error` 深链排障
- **渠道管理**：模型白名单（对外名 → 上游名映射）、默认模型兜底、
  可用时段（支持跨午夜）、渠道图标、发一句 "hi" 测连通性
- **思考档位声明**：每个白名单模型可单独声明支持的档位（`off` 到 `max`），
  `/v1/models` 按 `supported_efforts` 返回；不配则按渠道协议推导
  （Anthropic / Gemini 八档全在，OpenAI 兼容上游只列 `low` 以上）
- **密钥管理**：创建时必须选择模型，并为每个模型指定有序候选渠道（只能选白名单
  里有该模型的渠道），转发严格按该顺序故障转移；`/v1/models` 只列该密钥
  真正能调的模型
- **配置备份**：渠道（含白名单与价格）/ 密钥 / 代理一键导出导入，渠道密钥
  以密文保存；旧版（分组时代）备份可导入，密钥的路由会按现有渠道自动重建
- **在线版本检查与一键更新**：见[在线更新](#在线更新)

### 安全

- **二选一的登录认证**：首次登录用安装时生成的**引导密钥**进入设置向导，
  之后在页面上二选一 —— **动态码（2FA，TOTP）/ 自设管理密钥**。选定后引导
  密钥立即退役，登录凭据完全由你掌控；改密、2FA 换绑、切换方式都在
  「系统设置 → 登录认证」里完成（切换需要先验证当前凭据）。登录接口带
  IP 暴力破解限流（15 分钟内错 5 次锁定），2FA 动态码另有 30 秒防重放。
- **渠道密钥加密**：上游密钥以 AES-256-GCM 加密后入库；2FA 密钥同样加密
  存储（两者用不同的派生密钥）

## 快速开始

### 一键安装（推荐）

自动从 GitHub Releases 下载预编译二进制（前端已 embed），装好 PostgreSQL 并注册
为系统服务（Linux 用 systemd，Windows 用 SCM）。

**前置条件**：Linux 服务器（amd64 / arm64，Debian/Ubuntu）+ Root + systemd 运行中
（WSL 需在 `/etc/wsl.conf` 里启用 `[boot] systemd=true`）。PostgreSQL 由脚本自动
安装配置，无需预装。

```bash
curl -sSL https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.sh | sudo bash
```

脚本会自动：检测架构 → 下载最新版并做 sha256 校验 → 安装到 `/opt/llm-relay`
→ 安装并初始化 PostgreSQL → 生成配置与随机密钥 → 注册 systemd 开机自启 →
健康检查。

安装完成后：

```bash
sudo systemctl status llm-relay     # 服务状态
# 引导密钥（RELAY_ADMIN_KEY）在安装结束时打印，也在 /opt/llm-relay/deploy/.env
# 浏览器打开 http://你的服务器IP:8888，输入它登录一次 —— 随后页面会让你
# 二选一：扫码绑定动态码（2FA），或自己设定一个管理密钥。选定后引导密钥
# 立即失效，之后就用你选的那种方式登录。
```

**可选参数**（跟在 `bash -s --` 后面，或先 export 再 `sudo -E`）：

```bash
# 例：监听公网 + 自定义端口
curl -sSL https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.sh | sudo bash -s -- PORT=9000 BIND_ADDR=0.0.0.0
```

| 变量 | 默认值 | 说明 |
|---|---|---|
| `INSTALL_DIR` | `/opt/llm-relay` | 安装目录 |
| `PORT` | `8888` | 监听端口（写入 `.env` 的 `SERVER_PORT`） |
| `BIND_ADDR` | `127.0.0.1` | 监听地址（写入 `.env` 的 `SERVER_HOST`） |
| `SERVER_PUBLIC_ORIGIN` | — | 公网/反代部署时的固定管理台来源，例如 `https://relay.example.com` |
| `DB_NAME` / `DB_USER` | `llm_relay` / `llmrelay` | 数据库 |
| `VERSION` | 最新 Release | 要安装的版本号（不带 v 前缀） |
| `RELAY_GH_PROXY` | — | GitHub 下载加速前缀（形如 `https://gh-proxy.com`） |

> - 默认只绑 `127.0.0.1`。放到公网时加 `BIND_ADDR=0.0.0.0`，并同时设置
>   `RELAY_ADMIN_KEY`，建议再加反向代理 + HTTPS。`SERVER_PUBLIC_ORIGIN`
>   是可选的：有固定域名时设置它可以把管理台锁死到该来源；留空（例如内网
>   穿透地址由隧道平台分配、写不死）时，管理台只接受 Host 与请求地址一致
>   的同源请求。
> - **重复运行就是升级**：已有密钥原样沿用，升级前自动备份数据库到
>   `$INSTALL_DIR/backups/`（恢复用 `deploy/restore.sh`）。
> - 小内存服务器（2 核 1.6G）也能装：不编译任何东西，下载归档约 8 MB。

常用命令：

```bash
systemctl status llm-relay                 # 服务状态
systemctl restart llm-relay                # 重启
journalctl -u llm-relay -f                 # 实时日志
bash deploy/restore.sh --list              # 查看升级前的自动备份
```

### Windows 安装

**一个安装脚本，一个卸载脚本**，各自支持一键与本地两种用法 —— 不需要判断该用哪个。

**一键安装**：以**管理员**身份打开 PowerShell（开始菜单搜索 PowerShell →
右键「以管理员身份运行」；或 Win+X →「终端(管理员)」），执行：

```powershell
irm https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.ps1 | iex
```

国内网络直连 raw.githubusercontent.com 失败时，连**脚本本身**也要走加速前缀——
`-GhProxy` 参数救不了这一步，因为它只在脚本已经跑起来之后才生效：

```powershell
irm https://gh-proxy.com/https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.ps1 | iex
```

需要用 `-GhProxy` 是另一件事：脚本里下载 Release 归档和 PostgreSQL 安装包
（`install.ps1` 会把前缀拼到 `https://github.com` 前面），带参数时用
`[scriptblock]::Create` 包一层（`$args` 无法直接透传给 `iex`）：

```powershell
& ([scriptblock]::Create((irm https://gh-proxy.com/https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.ps1))) -GhProxy https://gh-proxy.com
```

> 一行式与本地运行**是同一个文件**：脚本开头会检测自己是不是从管道执行的，
> 是的话先把自己下载到临时文件再以文件方式重跑，所以两条路径共用同一段
> 安装逻辑，不会出现「一键版和本地版行为不一样」。

**离线安装**（解压归档后）：

```powershell
pwsh -File .\deploy\install.ps1
```

**卸载**（同样是管理员 PowerShell）：

```powershell
irm https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/uninstall-windows.ps1 | iex
```

或本地：`pwsh -File .\deploy\uninstall-windows.ps1`。

卸载会先列出将删除什么，然后让你选：

| 选项 | 删除什么 |
|---|---|
| **1. 保留数据**（默认） | 只删 `llm-relay` 服务与程序文件；独立的 PostgreSQL 服务、数据库、配置、日志、备份全部保留（重装可直接复用） |
| **2. 连同数据删除** | 需要再输入 `DELETE` 确认 |

「连同数据删除」的行为**取决于当初安装时用的哪个数据库**（安装时记录在
`install.json`）：

- 安装时选了**新的独立实例** → 停掉并删除该实例的服务，整个数据目录随安装目录一起删除
- 安装时选了**已有的 PostgreSQL** → 只删本项目的数据库与用户，
  **PostgreSQL 本身保留**，也不会停你别的服务。删用户需要 postgres 超管口令，
  脚本会问你要；直接回车跳过则会打印手动执行的 SQL

**前置条件**：Windows 10/11 或 Server 2016+（amd64）+ 管理员权限。

**关于数据库**：安装器不问你用哪个数据库，它只看机器上有没有：

- **没有** PostgreSQL → 在 `%ProgramData%\llm-relay` 下装一个独立实例（约 348 MB），
  超级用户口令随机生成，装完打印出来并写进 `credentials.txt`。
- **已有** PostgreSQL → 直接复用它，只建一个数据库，**不动**该实例里其他任何东西。
  建库需要该实例 postgres 超级用户的口令：交互运行时提示输入，脚本化时用
  `-PgSuperPassword` 传入。

两种情况**都用 `postgres` 作为应用的连接身份**，安装器不再单独建业务角色——
少一个「角色是否存在 / 密码是否同步」的失败面。

> 为什么不再提供「重新装一个实例」的选项：那条路依赖 EnterpriseDB 安装器的
> unattended option file，而在已有 PostgreSQL 的机器上它会**静默忽略**
> `prefix`、`servicename`、`disable-components` 并仍然返回退出码 0
> （2026-10-23 实测：目标目录为空，pgAdmin 被解包 21136 次到既有安装目录）。
> 一个失败时不会报错的选项，不如没有选项。

脚本会自动：检测架构 → 下载并 sha256 校验 → 解压到 `%ProgramData%\llm-relay`
→ 复用或安装 PostgreSQL 并建库 → 生成配置与随机密钥（ACL 只允许 SYSTEM 与
Administrators 读取）→ 注册为 Windows 服务并配置失败自动重启 → 健康检查 →
打印全部凭据并写入 `credentials.txt`。

全部选项都有合理默认值，**直接运行即可，不需要任何参数**。需要定制时：

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-InstallDir` | `%ProgramData%\llm-relay` | 安装目录 |
| `-Port` | `8888` | 监听端口 |
| `-BindAddr` | `0.0.0.0` | 监听地址（内网/穿透访问需要；只本机用可传 `127.0.0.1`） |
| `-PublicOrigin` | — | 固定管理台来源，可选。留空时只接受 Host 与请求地址一致的同源请求 |
| `-DbName` | `llm_relay` | 数据库名（连接身份固定为 `postgres`） |
| `-Version` | 最新 Release | 要安装的版本号（不带 v） |
| `-GhProxy` | — | GitHub 下载加速前缀 |
| `-PgSuperPassword` | — | 复用已有实例时建库用的 postgres 口令；不传则交互提示，非交互直接报错 |
| `-Force` | — | 已装同版本时仍然重新下载并覆盖 |

> 重复运行同一个版本时**不会重新下载**（归档缓存在
> `%LOCALAPPDATA%\llm-relay\cache`，且已装同版本会跳过替换），升级到新版本
> 才下载。要强制重装加 `-Force`。「已装哪个版本」读的是安装时写下的
> `install.json`，不是 exe 的文件版本属性——Go 的 `-ldflags -X` 不会生成
> PE 版本资源，发布出来的 exe 里 `ProductVersion` 是空的。

> - 安装目录特意放在 `%ProgramData%` 而不是 `Program Files`：**一键更新**需要在
>   可执行文件同目录写临时文件并改名，`Program Files` 对服务账户不可写。
> - 日志走文件（`<InstallDir>\logs\llm-relay.log`，100 MB 轮转一份历史），
>   因为 Windows 服务没有 stdout 接收方，systemd 的 journald 在这里没有对应物。
> - **开机自启**靠两件事一起保证：服务注册时写入对 PostgreSQL 服务的
>   `DependOnService`（SCM 因此先等数据库起来），以及 `sc failureflag=1`
>   加失败重启策略（进程即便仍以非零码退出，SCM 也会按 5s/10s/30s 重试）。
>   二进制自身在启动阶段还会对「数据库尚未就绪」这类连接错误做 30 秒有界重试；
>   认错密码、库不存在则立即失败并写日志——以前这类启动失败在服务形态下**没有任何记录**。
> - 重复运行就是升级：三把密钥（DB 密码 / `RELAY_SECRET` / `RELAY_ADMIN_KEY`
>   引导密钥）原样沿用，升级前自动把数据库 `pg_dump` 到 `<InstallDir>ackups\`。
>   已完成的认证状态（2FA 或自设密钥）存在数据库里，升级不会重置。

常用命令：

```powershell
Get-Service llm-relay                          # 服务状态
Restart-Service llm-relay                      # 重启
Get-Content -Wait "$env:ProgramData\llm-relay\logs\llm-relay.log"   # 实时日志
```

## 在线更新

管理台侧栏品牌下方的版本徽标就是更新入口：当前版本、检测新版本、一键更新与回滚。

「一键更新」是否可用取决于构建形态（构建时经 `-ldflags` 注入的 `BuildType`）：

| 形态 | 谁注入 | 一键更新 | 原因 |
|---|---|---|---|
| `binary` | goreleaser 发布产物（`install.sh` / `install.ps1` 安装的即此形态） | 原子替换自己的可执行文件 | 托管方负责拉起新版本：Linux 是 systemd `Restart=always`，Windows 是 SCM 的 `sc failure` 恢复策略 |
| `source` | 兜底（本地 `go build` 未注入时一律按它处理） | **不允许** | 避免用官方二进制覆盖开发者本地的构建产物 |

更新流程的几个刻意取舍：

- **更新是异步任务**：下载是分钟级操作，界面轮询进度（阶段 + 百分比 + 日志），
  刷新页面也不丢；
- **校验和缺失即拒绝安装**、**替换前留 `.backup`**（回滚不依赖网络）；
- **更新完不自动重启**：新版本若启动就崩，自动重启会变成崩溃循环，而留在旧进程上的
  管理台正是唯一的救援入口。

检测走 GitHub Releases API，未配 token 限额每小时 60 次。**服务端每 5 分钟自己检测一次**
（后台定时，进程启动一分钟后开始；未配 token 时每小时 12 次，占共享限额的五分之一），
结果落在与手动检测同一份缓存里，打开面板或侧栏徽标读到的就是它，检测时间最多差 5 分钟；
面板里的「检测更新」按钮仍会强制穿透缓存。GitHub 不可达时显示「未能确认」，
不谎称「已是最新」。
网络不稳时可在「代理管理」里勾选「用于自动更新」，或在「系统设置 → 版本更新」里
填备用代理或 token（token 只发往 `api.github.com`）。
环境变量里的代理（`HTTPS_PROXY` / `HTTP_PROXY` / `ALL_PROXY`，大小写都认，
也支持 `127.0.0.1:7890` 这种省略 scheme 的写法）同样会被用上，并且与界面上填的代理
**同等对待**：它是运维自己配的可信出口（系统级 Clash/v2ray 就写在这里），
不会因为「代理在本机」而被当成内网目标拦下；`NO_PROXY` 仍然生效，
被它排除的请求照旧直连并做公网地址校验。

回滚两种方式：

| 方式 | 依据 | 说明 |
|---|---|---|
| 本地回滚 | 可执行文件旁的 `.backup` | 不联网也可用，回滚本身也可撤销 |
| 下载指定版本 | Release 列表（比当前旧、非预发布） | 目标必须在白名单列表里，防止装上未经发布验证的产物 |

版本号的单一来源是 `backend/internal/version` 包（ldflags 注入 → `VERSION`
文件 → `dev` 三级兜底）。确认线上版本：

```bash
curl -s http://127.0.0.1:8888/system/info | python3 -m json.tool
```

## 配置

优先级：**环境变量 > YAML 文件 > 内置默认值**。完整字段见 `config.example.yaml`。

关键项：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `SERVER_PORT` | `8888` | 监听端口 |
| `SERVER_HOST` | `127.0.0.1` | 监听地址。`deploy/.env` 与 `install.sh` 的 `BIND_ADDR` 参数写的都是它 |
| `DB_*` | — | PostgreSQL 连接 |
| `RELAY_SECRET` | 无 | 渠道密钥的加密主密钥，**必须显式设置**（见下节） |
| `RELAY_ADMIN_KEY` | 无 | 管理台**引导密钥**（首次登录进入设置向导用，见下节） |
| `RELAY_PAYLOAD_STORAGE_MODE` | `errors` | 报文留存：`all` / `errors` / `none` |
| `RELAY_PAYLOAD_MAX_KB` | `256` | 单条报文留存上限，超出截断并标注 |
| `RELAY_MAX_CONCURRENCY` | `64` | 全局在途请求上限，超出排队（最多 60 秒）；`0` 不限 |
| `RELAY_DEFAULT_RPM` | `0` | 密钥默认每分钟配额；单把密钥可覆盖，负数表示该密钥不限 |
| `RELAY_RETRY_SAME_UPSTREAM_DELAY` | `500ms` | 换渠道前后仍打同一台上游时的等待间隔，`0` 关闭 |
| `RELAY_CLIENT_MAP_LIMIT` | `2048` | 客户端显示映射的进程内缓存上限；满了淘汰最久未用的，不拒绝新客户端 |
| `RELAY_CLIENT_MAP_TTL` | `168h` | 映射缓存条目的闲置过期时间，`0` 表示只靠容量淘汰 |
| `TZ` | `Asia/Shanghai` | 进程本地时区：可用时段与时段倍率按它判断 |

## 密钥与安全

本站有两把相互独立的密钥，不要混用：

### 管理台登录认证（二选一）与引导密钥 `RELAY_ADMIN_KEY`

管理台是**无账号**模型，登录凭据二选一：

| 方式 | 登录时输入 | 绑定/设置入口 |
|---|---|---|
| 动态码（2FA / TOTP） | 认证器 App 上的 6 位动态码 | 首次向导扫码，或「设置 → 登录认证 → 切换到动态码」 |
| 管理密钥 | 你自己设定的密钥（≥16 位） | 首次向导手填，或「设置 → 登录认证 → 修改密钥」 |

**首次登录用的是引导密钥**（`RELAY_ADMIN_KEY`，安装脚本自动生成并打印）：

- 它只在你完成初始化之前有效。登录后页面让你二选一：扫码绑 2FA，或手填
  管理密钥；选定后它立即从登录链路退役，**之后只有你选的凭据能登录**。
- 它同时是公网部署的第一道门：初始化完成之前，它是唯一门槛 —— 所以别把
  「跳过向导」当成可以拖的事。
- 认证状态存在数据库中（`settings` 表的 `console_auth_v1` 一行），**不进配置
  备份**。2FA 密钥以 AES-256-GCM 加密存储，密钥派生带域分隔前缀（与渠道密钥
  的派生互不相同）。
- 切换方式、改密、换绑都在「系统设置 → 登录认证」完成，且**必须先通过当前
  凭据**（2FA 模式验动态码，密钥模式验旧密钥）—— 劫持了浏览器会话的人没有
  当前凭据，换不掉你的 2FA。
- 任何凭据变更都会轮换会话版本：**其它设备上的登录立即失效**，当前浏览器由
  服务端重签 Cookie 无感续期。会话有效期 `RELAY_SESSION_TTL` 默认 168h。
- 登录接口带按 IP 的暴力破解限流（15 分钟内错 5 次锁定）；动态码另有 30 秒
  防重放（同一个码不能用两次）。
- 未设置 `RELAY_ADMIN_KEY` 时登录鉴权整体关闭（只绑 `127.0.0.1` 的本地部署
  可接受），启动日志与界面横幅会持续提醒。

**设备丢失/忘记密钥怎么办**：删掉认证状态那一行后重启，会回到「引导密钥 +
设置向导」的状态，重新初始化即可：

```bash
su - postgres -c "psql -d llm_relay -c \"DELETE FROM settings WHERE key='console_auth_v1'\""
sudo systemctl restart llm-relay
```

> 这一步的代价是「任何拿到服务器 shell 的人都能重置认证」—— 物理/root 权限
> 本来就等同于全部权限，这一条不扩大攻击面。数据库备份里不含认证状态，
> 用旧备份恢复后同样会回到向导。

### 渠道密钥加密主密钥 `RELAY_SECRET`

渠道里的上游密钥以 AES-256-GCM 加密后存库，密钥由 `RELAY_SECRET` 派生。

- 首次安装自动生成 48 位随机值；**重装或换机必须沿用同一个值**，否则渠道密钥
  解不开，表现为渠道全部认证失败
- 备份文件里存的是密文，**备份与 `RELAY_SECRET` 要分开保管**：只拿到备份解不开
  密钥，只拿到主密钥也拿不到密文
- 两把密钥**不得相同**（启动时拒绝）；留空会退回程序内置的公开默认值，等于没有
  加密
- 老版本部署过内置默认密钥的，用轮换工具迁移，不必重填上游密钥：

  ```bash
  bash scripts/rotate-secret.sh          # 内部会先 dry-run 再正式迁移
  ```

**公网部署**：先设好 `RELAY_ADMIN_KEY`（引导密钥），再把 `SERVER_HOST` 改成 `0.0.0.0`，
部署完成后**尽快打开管理台完成初始化**（选 2FA 或自设密钥），
建议加反向代理 + HTTPS（Caddy / Nginx 均可）。有固定域名时再设 `SERVER_PUBLIC_ORIGIN`
把管理台锁死到该来源；域名不固定（内网穿透）可留空，此时只接受同源请求。
经反代时按 `SERVER_TRUSTED_PROXIES`
（默认只信回环）采信 `X-Forwarded-For`，登录限流才能拿到真实客户端 IP。

## 本地开发

```bash
# 后端（Go 1.25）
cd backend
go run ./cmd/server -config ../config.example.yaml

# 前端（Vue 3 + Vite）
cd frontend
npm install
npm run dev          # 开发服务器 :5173，/api 代理到 :8888
```

前端类型与契约检查：`npm run type-check`、`npm run check`；单测 `npm run test`。
前端产物构建后需同步到后端嵌入目录（命令见 `DEVELOPMENT.md`）。

开发态的 Vite 代理有两个**必须显式配**的坑（`ws: true` 与 Origin 改写，少任何
一个都是「页面能看，但实时不更新」），原因与解法见
[DEVELOPMENT.md](DEVELOPMENT.md#本地开发补充)。

## 目录结构

```
llm-relay/
├── backend/                    Go 后端
│   ├── cmd/server/             程序入口（cmd/rotate-secret 为主密钥轮换工具）
│   └── internal/               api / config / model / store / relay / pricing /
│                               proxy / secure / version / update / web(前端 embed)
├── frontend/                   Vue 3 + Vite + Ant Design Vue 4
├── deploy/                     install.sh / install.ps1 / uninstall-windows.ps1 / restore.sh / .env.example
├── scripts/                    端到端验证脚本与发布脚本（见 DEVELOPMENT.md）
├── DEVELOPMENT.md              开发细节、实现取舍笔记、验证脚本全集、发布流程
├── UI-SPEC.md                  UI 规格（令牌、布局、列宽、动效的实测数据）
├── docs/                       本地产物（截图、布局测量、历史计划），不入版本控制
└── .github/workflows/          ci.yml（push/PR）、release.yml（tag 触发发布）
```

## 文档

| 文档 | 内容 |
|---|---|
| [DEVELOPMENT.md](DEVELOPMENT.md) | 本地开发细节、实现取舍笔记、验证脚本全集、发布流程 |
| [UI-SPEC.md](UI-SPEC.md) | UI 规格书（设计令牌与布局的实测数据） |

## 许可证

[MIT](LICENSE)
