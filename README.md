# LLM Relay

本地大模型中转服务 —— 在 WSL2 + Docker 上运行的个人自用 AI API 网关。
默认端口 **8888**，提供与 `llm.ohub.vip` 视觉一致的管理后台。

## 特性

- **多协议入站**：OpenAI Chat Completions / Responses、Anthropic Messages、Gemini generateContent、Embeddings
- **渠道协议 = 上游协议**：客户端用哪种协议请求都行，站内先归一成 OpenAI Chat，
  再按渠道上的 `protocol` 转成上游格式发出，响应转回来后再改写回客户端协议。
  `anthropic-messages` 会改写成 Anthropic Messages 并请求 `/v1/messages`；
  `gemini-generateContent` 会改写成 Gemini 的 `contents` 结构并请求
  `/v1beta/models/{模型}:generateContent`（流式加 `:streamGenerateContent?alt=sse`）；
  `openai-chat` / `openai-responses` / `openai-embeddings` 走 OpenAI 端点；
  `custom` 可用 `auth_header` / `auth_prefix` 指定任意鉴权头。
  鉴权按协议自动选择：`anthropic-messages` 用 `x-api-key`（并补 `anthropic-version`），
  `gemini` 用 `x-goog-api-key`，其余用 `Authorization: Bearer`
- **精细计量**：输入 / 输出 / 缓存命中 / 缓存写入 / 推理 Token，区分「子集型」与「并列型」缓存口径
- **模型定价与预估金额**：价格全部手工录入；支持固定倍率与**按时段倍率**（如工作日 9:00-12:00 按 ×2 计费）；金额仅作成本感知，**不做任何扣减**
- **详尽的请求日志**：首包时间、总耗时、渠道与模型映射、状态码、原始报文、计费过程还原、导出
- **渠道管理**：分组、权重、可用时段（支持跨午夜）、**模型白名单（对外名 → 上游名映射）**、内置模板一键建渠道
- **限流与并发**：密钥级每分钟配额、渠道并发上限、全局在途闸门；上游 429 按
  `Retry-After` 自动冷却并切走，不再把已限流的上游打得更惨
- **报文留存**：`all` / `errors` / `none` 三档，按体积截断，凭据类请求头自动脱敏
- **配置备份**：一键导出/导入分组、渠道（含模型白名单）、密钥与定价，渠道密钥以密文保存
- **无登录 / 无充值 / 无金额系统**：纯本地运行

## 目录结构

```
llm-relay/
├── backend/                    Go 1.24 + Gin 后端
│   ├── cmd/server/             程序入口
│   └── internal/
│       ├── api/                HTTP 路由与处理
│       ├── config/             配置加载（YAML + 环境变量覆盖）
│       ├── model/              数据实体
│       ├── store/              数据库与缓存
│       ├── relay/              协议适配与转发内核
│       ├── pricing/            单价解析与成本计算
│       ├── usage/              Token 计量
│       └── web/                前端产物 embed
├── frontend/                   Vue 3 + Vite + Ant Design Vue 5
│   └── src/
│       ├── components/         MainLayout / PanelCard / StatCard / PageToolbar
│       ├── views/              各功能页面
│       ├── stores/             主题等状态
│       └── styles/theme.css    设计令牌（实测自参考站）
├── deploy/
│   ├── Dockerfile              多阶段构建（前端 → 后端 → 运行时）
│   ├── docker-compose.yml      app + postgres + redis
│   ├── .env.example            部署配置模板
│   ├── install.sh              一键部署脚本（Docker）
│   └── install-bare.sh         无 Docker 部署脚本（systemd 直跑）
├── scripts/
│   ├── capture-ui.mjs          CDP 抓取参考站计算样式
│   ├── capture-layout.mjs      CDP 深度抓取 DOM 与 class 规格
│   └── screenshot.mjs          CDP 页面截图
└── docs/
    ├── ui-spec.md              UI 规格书（实测数据）
    └── layout-*.json           原始抓取数据
```

## 一键部署（WSL2 / Ubuntu 24.04）

```bash
cd /path/to/llm-relay
# 方式一：WSL 免密直用 root（推荐，本机已验证）
wsl -u root -- bash deploy/install.sh

# 方式二：常规 sudo
sudo bash deploy/install.sh
```

脚本会依次完成：

1. 清理 Docker Desktop 卸载后残留的失效软链
2. 从 `download.docker.com` 安装 Docker Engine（直连可用）
3. **自动探测** Docker Hub 连通性：直连通 -> 跳过；否则 socks5 可用就用 **privoxy** 转成 http
   给 Docker daemon（daemon **不原生支持 socks5**，必须转换）；再不行退回 http 代理
4. 部署源码到 `/opt/llm-relay`，生成带随机数据库密码的 `.env`
5. `docker compose up -d --build`
6. 注册 `llm-relay.service` 实现开机自启，并等待健康检查通过

部署完成后访问 `http://localhost:8888`（Windows 浏览器直接可开，WSL2 localhost 转发）。

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `INSTALL_DIR` | `/opt/llm-relay` | 安装目录 |
| `PORT` | `8888` | 服务端口 |
| `USE_PROXY` | `auto` | `auto` / `yes` / `no` |
| `UPSTREAM_SOCKS` | 见脚本 | socks5 上游（优先使用） |
| `UPSTREAM_HTTP` | 见脚本 | http 代理（备用） |

## 无 Docker 部署

不支持容器、或不想为一个服务常驻容器的场景：

```bash
sudo bash deploy/install-bare.sh
```

脚本会自行装齐 Go / Node / PostgreSQL，本机编译前端与后端，产出单个二进制
交由 systemd 托管（不需要 Redis —— 后端从未使用它，编排里也已移除）。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `BIND_ADDR` | `127.0.0.1` | 监听地址，对应应用的 `SERVER_HOST` |
| `PORT` | `8888` | 端口 |
| `DB_NAME` / `DB_USER` | `llm_relay` / `llmrelay` | 数据库 |

> 应用的绑定变量是 `SERVER_HOST` / `SERVER_PORT`。脚本里写成 `BIND_ADDR` / `PORT`
> 只是为了和其他部署方式统一，生成 `.env` 时会转成前者——名字写错不会报错，
> 只在非默认端口时静默失效。

## 密钥管理（重要）

渠道里的上游密钥以 **AES-256-GCM** 加密后存库，密钥由 `RELAY_SECRET` 派生。

- `install.sh` / `install-bare.sh` 首次安装会自动生成 48 位随机 `RELAY_SECRET`
- **重装或换机必须沿用同一个值**，否则渠道密钥解不开，表现为渠道全部认证失败
- 备份文件里存的是密文，因此**备份与 `RELAY_SECRET` 要分开保管**：
  只拿到备份解不开密钥，只拿到主密钥也拿不到密文

如果已经在用内置默认密钥（老版本部署过），用轮换工具迁移，不必重填上游密钥：

```bash
bash scripts/rotate-secret.sh          # 内部会先 dry-run 再正式迁移
```

### 常用命令

```bash
systemctl status llm-relay                 # 服务状态
systemctl restart llm-relay                # 重启
cd /opt/llm-relay/deploy && docker compose logs -f app
cd /opt/llm-relay/deploy && docker compose down
```

### 网络说明（本机实测结论）

| 目标 | 直连 | HTTP 代理 :8088 | SOCKS5 :1080 |
|---|---|---|---|
| `registry-1.docker.io` | 失败 | **连接被重置** | **可达（401）** |
| `auth.docker.io` | 超时 | 连接被重置 | 可达（200） |
| `ghcr.io` / `quay.io` | 可达 | 可达 | 可达 |
| `download.docker.com` | 可达 | 可达 | — |
| `goproxy.cn` / `registry.npmmirror.com` | 可达 | 可达 | — |

**关键结论：HTTP 代理对 Docker Hub 会被重置，只有 socks5 能通**，所以脚本优先用
socks5 + privoxy 转换，而不是直接用 HTTP 代理。

### 构建期踩坑记录

以下三点已固化进配置，改动前请留意：

1. **Dockerfile 首行不能写 `# syntax=docker/dockerfile:1`**
   该指令会让 BuildKit 去 Docker Hub 拉取外部前端镜像，本机不可达会直接构建失败。
   已改用 BuildKit 内置前端。
2. **前端依赖用 `npm ci` 而不是 `npm install`**
   实测 `npm ci` 在容器内 **9.8 秒**完成；而在非 TTY 的构建环境下 `npm install`
   会长时间无输出（>17 分钟仍未完成）。
3. **privoxy 的 `listen-address` 必须唯一**
   重复行会导致 privoxy 启动失败（端口冲突），脚本已做去重处理。

## 本地开发

### 后端

```bash
cd backend
export GOPROXY=https://goproxy.cn,direct GOSUMDB=off
go run ./cmd/server -config ../config.example.yaml
```

### 前端

```bash
cd frontend
npm install --registry=https://registry.npmmirror.com
npm run dev          # 开发服务器 :5173，/api 代理到 :8888
npm run build        # 产物输出到 dist/
```

构建后同步产物到后端嵌入目录：

```bash
rm -rf backend/internal/web/dist && cp -r frontend/dist backend/internal/web/dist
```

## 配置

优先级：**环境变量 > YAML 文件 > 内置默认值**。完整字段见 `config.example.yaml`。

关键项：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `SERVER_PORT` | `8888` | 监听端口 |
| `DB_*` | — | PostgreSQL 连接 |
| `RELAY_PAYLOAD_STORAGE_MODE` | `errors` | 报文留存：`all` / `errors` / `none` |
| `RELAY_PAYLOAD_MAX_KB` | `256` | 单条报文留存上限，超出截断并标注 |
| `RELAY_MAX_CONCURRENCY` | `64` | 全局在途请求上限，超出排队（最多 60 秒）；`0` 不限 |
| `RELAY_DEFAULT_RPM` | `0` | 密钥默认每分钟配额；单把密钥可覆盖，负数表示该密钥不限 |
| `RELAY_SECRET` | 无 | 渠道密钥的加密主密钥，**必须显式设置** |
| `BIND_ADDR` | `127.0.0.1` | 仅 Docker 部署：宿主侧端口绑定地址 |

## UI 还原说明

设计令牌与布局尺寸均通过 Chrome DevTools Protocol 抓取参考站登录后的真实
`getComputedStyle` 得到，而非目测。详见 `docs/ui-spec.md`。

核心规格：主色 `#c87864`、底色 `#f8f5ee`、卡片圆角 8px + `0 0 8px rgba(0,0,0,.1)` 阴影、
顶栏 64px、侧边栏 224px、菜单项 32px 胶囊、栅格 gap 8px、衬线字体族。

## 开发进度

- [x] Phase 0 项目骨架：Go+Gin 服务、Vue3+AntdV 前端、Docker 多阶段构建、一键脚本
- [x] Phase 1 UI 逆向与设计系统：CDP 抓取、`ui-spec.md`、主题令牌、MainLayout、看板骨架
- [x] Phase 2 数据层与转发内核：实体与迁移、渠道路由（加权/轮询/故障转移）、协议适配
- [x] Phase 3 计量与成本：Token 计量归一化、手工定价、时段倍率（价格不再自动同步）
- [x] Phase 4 全协议与页面完善：Chat / Responses / Anthropic / Gemini / Embeddings 入站
- [x] Phase 5 健壮性与可观测：报文留存、限流与并发、渠道模板、配置备份
- [x] Phase 6 部署固化与冷启动验收：`install-bare.sh`、密钥轮换、冷启动实测

## 验证脚本

`scripts/` 下均为可直接运行的端到端验证，密钥从环境变量取：

| 脚本 | 验证内容 |
|---|---|
| `test-regression.sh` | 全部管理接口 + 四种协议端点连通性 |
| `test-ratelimit.sh` | 密钥级 RPM 放行/拒绝、`Retry-After` |
| `test-payload.sh` | 报文留存三档模式与凭据脱敏 |
| `test-templates.sh` | 渠道模板 CRUD、重名冲突、一键建渠道 |
| `test-backup.sh` | 备份导出/导入、明文泄漏检查 |
| `test-backup-roundtrip.sh` | 删除渠道后从备份恢复并真实调用 |
| `test-coldstart.sh` | 拆除容器与镜像后从零重建，核对数据完好 |
| `check-secrets.sh` | 扫描仓库与提交历史中的明文凭据 |
