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
- **多渠道币种**：币种是渠道属性（人民币的中转站 / 美元的上游），单价按渠道币种录入，
  日志与看板按各自币种统计 —— 请求日志逐条显示它自己的币种，看板在哪一栏出现两种币就
  分成两行/两条线，**不做汇率换算**（换算率从哪来、何时更新、历史按哪个汇率，三个问题
  都没有正确答案，而「哪条渠道在烧钱」不需要回答它们）。不同币种的钱不能相加，
  所以看板的「消耗金额」永远不出现合计，只并列
- **看板可按分组 / 渠道 / 模型筛选**：时间范围（今天 / 近3天 / 近7天 / 近30天）右侧
  可再选分组、渠道与模型，四张概览卡与下面的请求日志列表**全部**按这个范围取数
  （筛选条件本地持久化，回来还是它）。
  **金额单位跟着筛选走**：筛到某个分组或渠道时，就用那个范围记账用的币种显示 ——
  筛一条只记美元账的渠道，卡片给的是 `$`，而不是「人民币优先」下的 `¥0.0000`；
  分组里混着两种币时不猜，仍按并列显示
- **价格随渠道模型配置**：在渠道里加模型时就地把价配好（输入/输出/缓存读/缓存写，
  单位是每 100 万 token 的价格，币种见上一条）。价格挂在**渠道 × 模型**上而不是按模型名全局一份 ——
  同一个模型名在不同渠道成本本来就不一样（官网直连 vs 中转站），全局价只能取其一；
  支持**固定倍率**与**按时段倍率**（如工作日 9:00-12:00、14:00-18:00 按 ×2 计费，
  也可以填 0.5 做折扣），时段按**服务器本地时区**判断，命中时段时以时段倍率优先；
  渠道列表会点名「N 个未定价」——漏配价的后果是这笔调用被记成 0 元，账面上看不出异常；
  金额仅作成本感知，**不做任何扣减**
- **详尽的请求日志**：首包时间、总耗时、渠道与模型映射、状态码、原始报文、计费过程还原。
  界面上的「导出」按钮已去掉（2026-09-16），接口 `GET /api/admin/logs/export` 仍在，
  需要时直接调它，参数与列表接口完全一致。
  列表就在**数据看板**页（四张概览卡下面），与卡片共用同一套筛选条件
  （时间范围 / 分组 / 渠道 / 模型）——
  这两块回答的本来就是同一个问题，分在两页时最常做的事是「看总量 → 查明细 → 再切回去」，
  而那时两边的筛选还各记各的；
  这四个筛选会**记在本地**，刷新或下次打开还是同一个视角，
  存下来的分组 / 渠道 / 模型若已被删掉则自动退回「全部」；
  trace_id 与「只看失败」这两个排障入口不占工具栏，走 URL（`?trace_id=…`、
  `?status_class=error`），或用详情里的「只看这条链路」——它们是临时的，
  不跟着记住；而且**带这两个参数的链接不受本机记住的筛选影响**
  （同一个链接发给谁、隔多久打开，看到的都是同一批记录）。
  旧地址 `/console/logs` 会带着 query 跳到看板，老的链接与书签仍然可用
- **渠道管理**：分组、权重、可用时段（支持跨午夜，按服务进程本地时区判断——容器部署由 TZ 决定，与时段倍率同一口径）、**模型白名单（对外名 → 上游名映射）**、
  渠道图标（可从上游抓 favicon，也可填 emoji）、**发一句 "hi" 测连通性**
- **出站代理**：socks5 / http / https，可测连通性与延迟；
  渠道级与**模型级**都能指定（优先级：模型 > 渠道 > 直连），
  **代理不可用时直接失败、绝不回退直连** —— 静默回退会把本机真实 IP 暴露给上游，
  而界面上一切正常
- **实时推送**：WebSocket 推送看板数值与请求日志，数字用滚动动画过渡、
  日志插到第一行，都不重绘整页。推送本身固定是「今天 + 全站」的视角，所以：
  没筛选时直接把新行插到第一行；**筛过之后（或看板选了别的时间范围）则借这个
  推送信号安静地重取一次当前视角** —— 新日志符不符合筛选只有服务端知道，
  客户端不去重复实现一遍筛选语义
- **限流与并发**：密钥级每分钟配额、渠道并发上限、全局在途闸门；上游 429 按
  `Retry-After` 自动冷却并切走，不再把已限流的上游打得更惨
- **报文留存**：`all` / `errors` / `none` 三档，按体积截断，凭据类请求头自动脱敏
- **配置备份**：一键导出/导入分组、渠道（含模型白名单与各自的价格）、密钥与代理，
  渠道密钥以密文保存；导入旧备份时，里面的全局定价会按模型名应用到匹配的渠道模型上
- **无登录 / 无充值 / 无金额系统**：纯本地运行

## 目录结构

```
llm-relay/
├── backend/                    Go 1.25 + Gin 后端
│   ├── cmd/server/             程序入口
│   └── internal/
│       ├── api/                HTTP 路由与处理
│       ├── config/             配置加载（YAML + 环境变量覆盖）
│       ├── model/              数据实体
│       ├── store/              数据库与缓存
│       ├── relay/              协议适配与转发内核
│       ├── pricing/            单价解析与成本计算
│       ├── proxy/              出站代理（socks5/http/https）与连通性测试
│       ├── secure/             AES-GCM 加密与密钥哈希
│       └── web/                前端产物 embed
├── frontend/                   Vue 3 + Vite + Ant Design Vue 4
│   └── src/
│       ├── components/         MainLayout / PanelCard / StatCard / PageToolbar
│       ├── views/              各功能页面
│       ├── stores/             主题等状态
│       └── styles/theme.css    设计令牌（实测自参考站）
├── deploy/
│   ├── Dockerfile              多阶段构建（前端 → 后端 → 运行时）
│   ├── docker-compose.yml      app + postgres
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
cd llm-relay
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
侧边栏 224px、菜单项 32px 胶囊、栅格 gap 8px、衬线字体族。

列表与表格里的**西文与数字**用的是参考站那份 Harding，随包分发在
`frontend/public/fonts/Harding-Regular.ttf`（中文不受影响，仍走系统雅黑 ——
Harding 不含中文字形，会一路回退到 `--font-family-base`）。
两个容易踩的点记在 `frontend/src/styles/theme.css` 的字体段：字体 URL 必须写成
根绝对路径（打包后 CSS 在 `/assets/` 下，相对路径取不到图），
以及 antd 会给 `.ant-tag` 硬写 `font-family`、把继承来的列表字体顶掉，
标签要单独覆盖一次。**Harding 是商业授权字体**（参考站自托管的那份），
本项目内部自用不受影响，但对外分发前需要自行确认授权范围。
布局是**应用外壳**：高度锁在视口里、只有内容区滚，品牌与主题切换都在侧栏
（参考站那条 64px 顶栏装的是公告条与顶部导航，我们这边只剩品牌一个元素，
留着就是一行空白，所以去掉了 —— 参考站自身的尺寸仍记录在 `docs/ui-spec.md`）。

鼠标光标是本项目自己的品牌元素（参考站没有）：默认箭头与交互手型用
`frontend/public/cursor-arrow.svg` 与 `cursor-hand.svg`（主色填充 + 白描边，
两种主题下都过 3:1），输入框 I 型、拖拽、禁用、帮助保留系统光标。
要改光标形状或补齐 antd 升级后新增的交互组件，看 `frontend/src/styles/theme.css`
的「自定义光标」段 —— 那里记着清单的来源、为什么必须 `!important`、
以及 `npm run check` 里盯住哪几条。

## 开发进度

- [x] Phase 0 项目骨架：Go+Gin 服务、Vue3+AntdV 前端、Docker 多阶段构建、一键脚本
- [x] Phase 1 UI 逆向与设计系统：CDP 抓取、`ui-spec.md`、主题令牌、MainLayout、看板骨架
- [x] Phase 2 数据层与转发内核：实体与迁移、渠道路由（加权/轮询/故障转移）、协议适配
- [x] Phase 3 计量与成本：Token 计量归一化、手工定价、时段倍率（价格不再自动同步）
- [x] Phase 4 全协议与页面完善：Chat / Responses / Anthropic / Gemini / Embeddings 入站
- [x] Phase 5 健壮性与可观测：报文留存、限流与并发、配置备份
- [x] Phase 6 部署固化与冷启动验收：`install-bare.sh`、密钥轮换、冷启动实测

## 验证脚本

`scripts/` 下均为可直接运行的端到端验证，密钥从环境变量取：

| 脚本 | 验证内容 |
|---|---|
| `test-regression.sh` | 全部管理接口 + 四种协议端点连通性 |
| `test-ratelimit.sh` | 密钥级 RPM 放行/拒绝、`Retry-After` |
| `test-payload.sh` | 报文留存三档模式与凭据脱敏 |
| `test-pricing-filter.sh` | 渠道列表的「未定价」计数 |
| `test-pricing-rules.sh` | 时段倍率优先于固定倍率、跨午夜窗口、最后一条生效 |
| `test-group-quota.sh` | 分组 RPM / TPM 配额放行与拒绝 |
| `test-proxies.sh` | 代理 CRUD、连通性与延迟测试、被引用时禁止删除 |
| `test-egress-proxy.sh` | **请求确实经代理出网**（假域名 + 代理改写证明） |
| `test-live.sh` | WebSocket 握手、统计快照、新请求的日志推送 |
| `test-cost.sh` | 计费口径：缓存读写、推理 Token、子集型与并列型缓存 |
| `test-legacy-column-add.sh` | 老库缺列时自动补列并恢复可用（会短暂重启应用） |
| `test-backup.sh` | 备份导出/导入、明文泄漏检查 |
| `test-backup-roundtrip.sh` | 删除渠道后从备份恢复并真实调用 |
| `test-coldstart.sh` | 拆除容器与镜像后从零重建，核对数据完好 |
| `check-secrets.sh` | 扫描仓库与提交历史中的明文凭据 |
| `purge-test-logs.sh` | 清掉验证脚本产生的请求日志（否则会污染看板的今日统计） |

另有一批 python 用例（`test-group-update.py`、`test-key-whitelist.py`、
`test-delete-semantics.py`、`test-accept-encoding.py`、`test-log-filters.py`、
`test-csrf.py`），覆盖分组更新、密钥白名单、删除语义、压缩协商、
日志筛选与同源校验。

`scripts/audit-cursors.mjs` 是前端的光标审计（需要先起一个带调试端口的 Chrome，
用法见文件顶部）：把七个路由连同弹窗、抽屉、下拉里每个元素的**计算光标**统计
一遍，确认没有一处退回系统光标。它存在的理由很具体 —— **CSS 的 cursor 不进截图**，
少写一个选择器、antd 升级换了 class 名、光标图片 404，表现都只是那一处悄悄变回
系统箭头，不报错也不失败：

```bash
node scripts/audit-cursors.mjs http://127.0.0.1:5173   # 开发态
node scripts/audit-cursors.mjs                          # 默认打 127.0.0.1:8888
```

`scripts/verify-all.sh` 会按顺序跑完上面这些可离线执行的用例并汇总，
最后打印 `ALL_PASS`；日常改完代码跑它一次就够。
（`test-coldstart.sh` 要拆容器与镜像，不在其中。）

## 安全提醒

管理接口**没有登录鉴权**（`deploy/.env.example` 里默认 `BIND_ADDR=127.0.0.1`
只监听本机就是为此）。绑到 `0.0.0.0` 之前请自行加反向代理与访问控制 ——
否则同网段的任何人都能读到你的上游密钥与全部调用日志。

渠道里的上游密钥用 `RELAY_SECRET` 做 AES-GCM 加密后入库，所以这把主密钥
务必自己生成并保管好：留空会退回程序内置的公开默认值，等于没有加密。

## 许可证

[MIT](LICENSE)
