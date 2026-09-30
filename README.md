# LLM Relay

[![CI](https://github.com/ashu1800/llm-relay/actions/workflows/ci.yml/badge.svg)](https://github.com/ashu1800/llm-relay/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/ashu1800/llm-relay)](https://github.com/ashu1800/llm-relay/releases)

个人自用优先的大模型 API 网关：多协议入站、分组多渠道转发、按渠道 × 模型计价。
单个裸二进制 + systemd 运行，前端产物 embed 进二进制，装好即带 Web 管理台。

- 后端 Go 1.25 + Gin，前端 Vue 3 + Ant Design Vue 4，PostgreSQL 存储
- 默认监听 `127.0.0.1:8888`

## 特性

### 转发与协议

- **多协议入站**：OpenAI Chat Completions / Responses、Anthropic Messages、Gemini generateContent、Embeddings
- **渠道协议 = 上游协议**：入站先归一成 OpenAI Chat，再按渠道的 `protocol` 转成
  上游格式（`anthropic-messages` / `gemini-generateContent` / `openai-*` / `custom`
  自定义鉴权头），鉴权头按协议自动选择（`x-api-key`、`x-goog-api-key`、`Bearer`）
- **故障转移**：上游非 2xx 立即切换候选渠道；429 按 `Retry-After` 冷却（封顶 8 秒）、
  401/403 冷却 30 秒；请求物理尺寸超限（413/414/431）短路返回；换到同一台上游前
  留间隔（`RELAY_RETRY_SAME_UPSTREAM_DELAY`，默认 500ms）；连续失败 3 次自动
  熔断 60 秒，成功一次即恢复
- **出站代理**：socks5 / http / https，渠道级与模型级都可指定（模型 > 渠道 > 直连）；
  代理不可用直接失败、**绝不静默回退直连**；可勾选「用于自动更新」，让版本检测
  与下载走代理
- **限流与并发**：密钥级每分钟配额、分组 RPM / TPM 配额、渠道并发上限、全局在途
  闸门；上游 429 自动冷却并切走

### 计量与成本

- **精细计量**：输入 / 输出 / 缓存命中 / 缓存写入 / 推理 Token，区分「子集型」与
  「并列型」缓存口径
- **价格挂在渠道 × 模型上**（同一模型名在不同渠道成本不同），单位是每 100 万 token
  的价格；支持固定倍率与按时段倍率（4 位小数，按服务器本地时区判断，时段优先）；
  漏配价会被渠道列表点名「N 个未定价」
- **多币种不换算**：币种是渠道属性，日志逐条显示自己的币种，看板按币种并列展示、
  永不合计 —— 「哪条渠道在烧钱」不需要回答汇率从哪来
- **无充值 / 无金额系统**：金额仅作成本感知，不做任何扣减

### 管理台

- **数据看板**：时间 / 渠道 / 模型筛选（条件本地持久化），WebSocket 实时推送，
  数字滚动过渡、新请求日志即到即显
- **请求日志**：首包时间、总耗时、状态码、模型映射、计费过程还原、原始报文
  （`all` / `errors` / `none` 三档留存，凭据自动脱敏）；耗时 / 费用 / 状态服务端
  排序；`?trace_id=…` 与 `?status_class=error` 深链排障
- **渠道管理**：分组与权重、模型白名单（对外名 → 上游名映射）、默认模型兜底、
  可用时段（支持跨午夜）、渠道图标、发一句 "hi" 测连通性
- **密钥管理**：密钥 → 分组存 ID 的强一致引用（分组改名不断链）、模型白名单
  过滤 `/v1/models`（客户端探测到的就是它能调的）
- **配置备份**：分组 / 渠道（含白名单与价格）/ 密钥 / 代理一键导出导入，渠道密钥
  以密文保存
- **在线版本检查与一键更新**：见[在线更新](#在线更新)

### 安全

- **无账号的密钥登录**：全站一把管理密钥换取 HMAC 签名的无状态会话（HttpOnly
  Cookie，默认 7 天），登录接口带 IP 暴力破解限流；轮换密钥即全体下线
- **渠道密钥加密**：上游密钥以 AES-256-GCM 加密后入库

## 快速开始

### 一键安装（推荐）

自动从 GitHub Releases 下载预编译二进制（前端已 embed），装好 PostgreSQL 并注册
systemd 服务。

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
# 管理密钥（RELAY_ADMIN_KEY）在安装结束时打印，也在 /opt/llm-relay/deploy/.env
# 浏览器打开 http://你的服务器IP:8888，输入它登录
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
| `DB_NAME` / `DB_USER` | `llm_relay` / `llmrelay` | 数据库 |
| `VERSION` | 最新 Release | 要安装的版本号（不带 v 前缀） |
| `RELAY_GH_PROXY` | — | GitHub 下载加速前缀（形如 `https://gh-proxy.com`） |

> - 默认只绑 `127.0.0.1`。放到公网时加 `BIND_ADDR=0.0.0.0`，并先设好
>   `RELAY_ADMIN_KEY`（见[密钥与安全](#密钥与安全)），建议再加反向代理 + HTTPS。
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

## 在线更新

管理台侧栏品牌下方的版本徽标就是更新入口：当前版本、检测新版本、一键更新与回滚。

「一键更新」是否可用取决于构建形态（构建时经 `-ldflags` 注入的 `BuildType`）：

| 形态 | 谁注入 | 一键更新 | 原因 |
|---|---|---|---|
| `binary` | goreleaser 发布产物（`install.sh` 安装的即此形态） | 原子替换自己的可执行文件 | systemd `Restart=always` 负责拉起新版本 |
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
| `RELAY_ADMIN_KEY` | 无 | 管理台登录密钥（见下节） |
| `RELAY_PAYLOAD_STORAGE_MODE` | `errors` | 报文留存：`all` / `errors` / `none` |
| `RELAY_PAYLOAD_MAX_KB` | `256` | 单条报文留存上限，超出截断并标注 |
| `RELAY_MAX_CONCURRENCY` | `64` | 全局在途请求上限，超出排队（最多 60 秒）；`0` 不限 |
| `RELAY_DEFAULT_RPM` | `0` | 密钥默认每分钟配额；单把密钥可覆盖，负数表示该密钥不限 |
| `RELAY_RETRY_SAME_UPSTREAM_DELAY` | `500ms` | 换渠道前后仍打同一台上游时的等待间隔，`0` 关闭 |
| `TZ` | `Asia/Shanghai` | 进程本地时区：可用时段与时段倍率按它判断 |

## 密钥与安全

本站有两把相互独立的密钥，不要混用：

### 管理台登录密钥 `RELAY_ADMIN_KEY`

无账号模型的登录凭据：打开管理台时输入它换取会话。

- 首次安装自动生成 48 位随机值；自己设置时**至少 16 位**，太短拒绝启动
- 只在启动时取哈希比对，**不落库、不进配置备份**，泄露后改 `.env` 重启即可
- **轮换并重启后所有已登录会话立即失效** —— 无状态会话唯一的吊销手段，怀疑
  泄露就轮换；会话有效期 `RELAY_SESSION_TTL` 默认 168h
- 未设置时登录鉴权整体关闭（只绑 `127.0.0.1` 的本地部署可接受），启动日志与
  界面横幅持续提醒；登录接口带按 IP 的暴力破解限流（15 分钟内错 5 次锁定），
  经 HTTPS 反代部署时会话 Cookie 自动附加 `Secure` 标志

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

**公网部署**：先设好 `RELAY_ADMIN_KEY` 再把 `SERVER_HOST` 改成 `0.0.0.0`，
建议加反向代理 + HTTPS（Caddy / Nginx 均可）。经反代时按 `SERVER_TRUSTED_PROXIES`
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
前端产物构建后需同步到后端嵌入目录（命令见 `docs/development.md`）。

开发态的 Vite 代理有两个**必须显式配**的坑（`ws: true` 与 Origin 改写，少任何
一个都是「页面能看，但实时不更新」），原因与解法见
[docs/development.md](docs/development.md#本地开发补充)。

## 目录结构

```
llm-relay/
├── backend/                    Go 后端
│   ├── cmd/server/             程序入口（cmd/rotate-secret 为主密钥轮换工具）
│   └── internal/               api / config / model / store / relay / pricing /
│                               proxy / secure / version / update / web(前端 embed)
├── frontend/                   Vue 3 + Vite + Ant Design Vue 4
├── deploy/                     install.sh / restore.sh / .env.example
├── scripts/                    端到端验证脚本与发布脚本（见 docs/development.md）
├── docs/                       ui-spec.md（UI 规格）、development.md（开发细节）
└── .github/workflows/          ci.yml（push/PR）、release.yml（tag 触发发布）
```

## 文档

| 文档 | 内容 |
|---|---|
| [docs/development.md](docs/development.md) | 本地开发细节、实现取舍笔记、验证脚本全集、发布流程 |
| [docs/ui-spec.md](docs/ui-spec.md) | UI 规格书（设计令牌与布局的实测数据） |

## 许可证

[MIT](LICENSE)
