# LLM Relay

本地大模型中转服务 —— 在 WSL2 + Docker 上运行的个人自用 AI API 网关。
默认端口 **8888**，提供与 `llm.ohub.vip` 视觉一致的管理后台。

## 特性

- **多协议入站**：OpenAI Chat Completions / Responses、Anthropic Messages、Gemini generateContent、Embeddings
- **多协议出站**：渠道可选 `openai-chat`、`openai-responses`、`anthropic-messages`、`gemini`、`custom`（可配置适配器，无需写代码即可接入任意站点）
- **精细计量**：输入 / 输出 / 缓存命中 / 缓存写入 / 推理 Token，区分「子集型」与「并列型」缓存口径
- **模型定价与预估金额**：定时同步官方价格（OpenAI / DeepSeek），支持 DeepSeek **峰谷双价**；金额仅作成本感知，**不做任何扣减**
- **详尽的请求日志**：首包时间、总耗时、渠道与模型映射、状态码、原始报文、计费过程还原、导出
- **渠道管理**：分组、权重、可用时段（支持跨午夜）、模型映射、模板、连通性测试
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
│       ├── pricing/            单价同步与成本计算
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
│   └── install.sh              一键部署脚本
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
| `PRICING_REMOTE_URL` | LiteLLM 定价库 | 兜底定价来源 |
| `PRICING_UPDATE_INTERVAL_HOURS` | `24` | 单价同步间隔 |
| `PRICING_OFFICIAL_SYNC_ENABLED` | `true` | 是否抓取官方定价页（含峰谷双价） |
| `RELAY_PAYLOAD_STORAGE_MODE` | `errors` | 报文留存：`all` / `errors` / `none` |

## UI 还原说明

设计令牌与布局尺寸均通过 Chrome DevTools Protocol 抓取参考站登录后的真实
`getComputedStyle` 得到，而非目测。详见 `docs/ui-spec.md`。

核心规格：主色 `#c87864`、底色 `#f8f5ee`、卡片圆角 8px + `0 0 8px rgba(0,0,0,.1)` 阴影、
顶栏 64px、侧边栏 224px、菜单项 32px 胶囊、栅格 gap 8px、衬线字体族。

## 开发进度

- [x] Phase 0 项目骨架：Go+Gin 服务、Vue3+AntdV 前端、Docker 多阶段构建、一键脚本
- [x] Phase 1 UI 逆向与设计系统：CDP 抓取、`ui-spec.md`、主题令牌、MainLayout、看板骨架
- [ ] Phase 2 数据层与转发内核
- [ ] Phase 3 计量、成本与定价同步
- [ ] Phase 4 全协议与页面完善
- [ ] Phase 5 健壮性与可观测
- [ ] Phase 6 部署固化与冷启动验收
