# LLM Relay

本地大模型中转服务 —— 以**裸二进制 + systemd** 运行的个人自用 AI API 网关。
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
- **看板可按渠道 / 模型筛选**：时间范围（今天 / 近3天 / 近7天 / 近30天）右侧
  可再选渠道与模型，**四张概览卡**按这个范围取数（筛选条件本地持久化，回来还是它）。
  下面的请求日志列表**不跟着筛**（2026-09-16 起，见「详尽的请求日志」那条）。
  **金额单位跟着筛选走**：筛到某条渠道时，就用那条渠道记账用的币种显示 ——
  筛一条只记美元账的渠道，卡片给的是 `$`，而不是「人民币优先」下的 `¥0.0000`；
  未筛渠道时混着几种币就不猜，仍按并列显示
- **价格随渠道模型配置**：在渠道里加模型时就地把价配好（输入/输出/缓存读/缓存写，
  单位是每 100 万 token 的价格，币种见上一条）。价格挂在**渠道 × 模型**上而不是按模型名全局一份 ——
  同一个模型名在不同渠道成本本来就不一样（官网直连 vs 中转站），全局价只能取其一；
  支持**固定倍率**与**按时段倍率**（如工作日 9:00-12:00、14:00-18:00 按 ×2 计费，
  也可以填 0.5 做折扣），时段按**服务器本地时区**判断，命中时段时以时段倍率优先；
  倍率**最多 4 位小数**（如 0.1875，与数据库列 `numeric(10,4)` 同精度；
  2026-09-24 之前是 2 位，0.1875 会被界面悄悄舍成 0.19 —— 见下）；
  渠道列表会点名「N 个未定价」——漏配价的后果是这笔调用被记成 0 元，账面上看不出异常；
  金额仅作成本感知，**不做任何扣减**
- **详尽的请求日志**：首包时间、总耗时、渠道与模型映射、状态码、原始报文、计费过程还原。
  界面上的「导出」按钮已去掉（2026-09-16），接口 `GET /api/admin/logs/export` 仍在，
  需要时直接调它，参数与列表接口完全一致。
  列表就在**数据看板**页（四张概览卡下面）。它**不吃**上面那组筛选，
  恒定显示**全部最新请求**（2026-09-16 起）—— 列表的用处是盯着最新发生了什么，
  筛过之后反而看不到刚进来的调用，而刚进来的几条恰恰最该被看到。
  筛选用在上面那四张概览卡上：时间范围（今天 / 近3天 / 近7天 / 近30天）/
  渠道 / 模型，这三个会**记在本地**，刷新或下次打开还是同一个视角，
  存下来的渠道 / 模型若已被删掉则自动退回「全部」。
  工具栏上**没有**「筛选只作用于上方卡片」这类作用范围提示（2026-09-17 应站主要求
  移除，见 `docs/ui-spec.md` 第 9 条）—— 列表不吃筛选这件事由契约检查钉住，
  不再靠界面文案说明。
  trace_id 与「只看失败」这两个排障入口**仍然作用于列表**，但只走 URL：
  `?trace_id=…`（或在日志详情里点「只看这条链路」）与 `?status_class=error`；
  激活时看板工具栏上会出现一个可关闭的小标签。列表顶部的「仅失败」切换按钮
  与「实时 · 最后更新」状态行已于 2026-09-26 应站主要求移除。
  这两个条件是临时的，不跟着记住；而且**带这两个参数的链接不受本机记住的筛选影响**
  （同一个链接发给谁、隔多久打开，看到的都是同一批记录）。
  旧地址 `/console/logs` 会带着 query 跳到看板，老的链接与书签仍然可用
- **渠道管理**：分组、权重、可用时段（支持跨午夜，按服务进程本地时区判断——官方部署由 `.env` 的 TZ 决定，与时段倍率同一口径）、**模型白名单（对外名 → 上游名映射）**、
  **默认模型映射（兜底）**、渠道图标（可从上游抓 favicon，也可填 emoji）、**发一句 "hi" 测连通性**
- **默认模型映射（兜底）**：每条渠道可单独开启，指定一个白名单里的模型。
  开启后，**白名单没精确命中的请求**一律改用这个模型发往上游；精确命中的仍按
  白名单走（精确优先，兜底候选永远排在最后）。
  这是为 Claude Code 这类自己挑模型名的客户端准备的：它发来的
  `claude-sonnet-4-6` / `claude-opus-4-7` 不在任何渠道白名单里，从前会得到
  耗时 1–3ms、token 全 0 的 502（模型名原样转发给上游，被上游当场拒绝）。
  开了兜底就不必每次回来改白名单。三件事要知道：
  - **日志里请求模型仍记客户端的原名**，上游模型记实际发往上游的名字，并带「兜底」标记 ——
    开启了兜底之后**模型名写错也不再报错**，这个标记是发现「其实没命中」的唯一途径。
    兜底目标行配了「对外名 → 上游名」映射时，映射照常生效（2026-09-21 修复：
    此前映射被忽略，上游收到它不认识的对外名，复现兜底要消灭的那种 502）
  - **费用按兜底目标的单价计算**（客户端的模型名在渠道里通常没配价）
  - **响应里的模型名是上游回显的那个**（即兜底目标），不是客户端请求的名字 ——
    这是**既有行为**，与兜底无关：任何配了「上游模型名」的渠道都这样（响应改写器
    优先取上游响应体里的 model 字段，取不到才回落到请求名）。客户端拿到一个
    与自己请求不同的模型名是预期内的
  另：**密钥的 `allowed_models` 白名单不被兜底豁免**（配了限制就仍然 403，
  不会被兜底绕开）；兜底目标必须是该渠道白名单里**启用中**的条目 —— 保存时拦
  （目标行停用也拦，2026-09-21 起），分两步造出的半残（先配好、事后停用那一行）
  由失败提示点名、渠道列表的「兜底」胶囊不亮；**备份导入**带进来的半残配置
  会被剥离并写进导入报告
- **出站代理**：socks5 / http / https，可测连通性与延迟；
  渠道级与**模型级**都能指定（优先级：模型 > 渠道 > 直连），
  **代理不可用时直接失败、绝不回退直连** —— 静默回退会把本机真实 IP 暴露给上游，
  而界面上一切正常；代理还能勾选「**用于自动更新**」（单选互斥），
  让版本检测与更新下载走它，服务器连不上 GitHub 也能照常更新
- **实时推送**：WebSocket 推送看板数值与请求日志，数字用滚动动画过渡、
  日志插到第一行，都不重绘整页。**有新日志时两帧走同一拍**：日志循环查到新行后
  会立刻叫统计循环算一次，所以标记新行的入场动画与卡片数字的滚动是同时开始的
  （从前两者是两个互不相干的定时器，新行先到、数字最多晚两秒，见
  `docs/ui-spec.md` 第 16 条）。推送本身固定是「今天 + 全站」的视角，所以：
  列表没有额外条件时直接把新行插到第一行；**带 trace / 仅失败深链时则借这个
  推送信号安静地重取一次当前视角** —— 新日志符不符合条件只有服务端知道，
  客户端不去重复实现一遍筛选语义。
   服务端每 25 秒发一个空的心跳帧（2026-09-24 加）：三条推送都是「变了才推」，
   闲时一帧不发，而客户端的看门狗是「45 秒没收到任何消息就当断开」——
   没有心跳的话，深夜没有流量时一条完全正常的连接会被判成断开并反复重连。
   心跳只证明链路活着，**不代表有新数据**
- **新日志直接进列表**（2026-09-25 改回）：实时推送带来的新行一律插到第一行，
   不等待任何交互。此前有过一版「挂起」机制（9e9f3b4，P1-6）：表体滚过中段、
   鼠标停在表体上、或详情抽屉开着时把新行攒起来，表格上方显示
   「有 N 条新日志，点击查看」。它的判据里「鼠标停在表体上」在真实使用中
   几乎是常驻状态（正看着某一行、或在等新行出现），而释放只发生在
   滚动 / 鼠标移出 / 抽屉关闭三个时刻，于是新行只攒不落、列表看起来冻住了 ——
   站主反馈「只显示提示，但是列表不更新，鼠标动一下才更新」。
   新行插到头部必然让内容下移一行（这是「最新在最上面」的定义），
   位置稳定由前端自己用 JS 显式做：插行前量出「视口里第一个可见行」
   相对表体顶部的偏移，插行后按差值补 `scrollTop`，那一行就仍停在原处
   （只在用户已经滚动过时补偿；停在顶部时用户要看的正是刚进来的几条）。
   **不依赖浏览器的原生滚动锚定**：实测（2026-09-25）Chrome 的
   `overflow-anchor` 在最小页面上有效，在这张日志表上却不生效 ——
   纯 DOM 插入、余量充足、祖先链无 `none` 的情况下仍不补偿，
   且已排除固定列 sticky、table-layout、`overflow: hidden` 三种因素
- **实时状态指示**（已移除）：请求日志面板顶部的「实时 · 最后更新 HH:mm:ss」
   状态行 2026-09-26 应站主要求移除（连同「仅失败」切换按钮，见上）。
   此前它解决的问题是 `liveConnected` 导出了却无人使用、WS 断了界面照旧显示旧数据；
   移除后 WebSocket 断线仍会自动重连，数据停止跳动即是断线的表现
- **请求日志可排序 + 快速跳页**（2026-09-24 加）：耗时 / 费用 / 状态三列点表头即可
  按服务端排序（`sort=elapsed|cost|status`，带 `-` 前缀为降序），第三次点击回到
  「最新在前」。**排序必须由服务端做** —— 前端只能排当前页那 20/50 条，
  而「最慢的一单」「最贵的一单」显然不在当前页。费用那一列另有一条口径：
  站里不做汇率换算，所以后端**先按币种分组、再在组内按金额排**
  （`cost_currency` 排在 `estimated_cost` 之前），界面上会同时给出一行说明 ——
  否则 `¥2.35` 排到 `$0.0007` 后面会被读成「美元更贵」。分页器另加
  快速跳页输入框：25000 条按 20 条一页是 1260 页，只靠 `•••`（一次 5 页）
  等于到不了中间那些页
- **新日志扫光**：新行插进来时，沿这一行的下分割线从左扫过一道彩虹（约 2 秒，
  见 `docs/ui-spec.md` 第 11 条）。只有实时推送带来的新行会闪：刷新页面、改筛选、
  翻页、点「刷新」都不闪，系统开了「减弱动态效果」时也不闪。
  亮带画在表格外面的独立一层里，**不往表格里加任何元素** —— 往 `tr` 里放伪元素
  会让 Chrome 在 `table-layout: fixed` 下不再分配多余宽度，整表塌回声明宽度、
  右侧空出一条（只在窗口比表格宽时出现，2026-09-16 修）
- **两排数值的列左缘对齐**：「词元」「任务耗时」两格里的内容块居中，但轨道宽度
  写成了定值 —— `auto` 轨道会跟着数值长短伸缩，整块宽度逐行不同，竖条和
  图标因此每行落在不同的 x 上（宽窗口实测漂 7.2 / 9.38px）。定值取「列最窄时
  可用宽度」倒推，任何窗口下都不会溢出（见 `docs/ui-spec.md` 第 10 条）
- **任务耗时的双色竖条**：竖条断成两段，上段跟「首字」、下段跟「耗时」，
  段色各自继承该行数值的 `currentColor`。首字 ≤10s 绿 / 10-30s 橙 / >30s 红，
  耗时 ≤20s 绿 / 20-60s 橙 / >60s 红 —— 两行各用一套阈值（首字才是「卡不卡」
  的那一下，总耗时含上游生成本来就长）；一整条单色只能表达「这次调用慢」，
  断成两段才看得出慢在哪一段。秒值一律补齐两位小数（`8.70s`），
  不足 1 秒仍按毫秒显示（见 `docs/ui-spec.md` 第 6 条）
- **限流与并发**：密钥级每分钟配额、渠道并发上限、全局在途闸门；上游 429 按
  `Retry-After` 自动冷却并切走，不再把已限流的上游打得更惨
- **密钥 → 分组的强一致引用**：密钥白名单里存**分组 ID**（与渠道的 `group_id`
  同一待遇），保存时逐条校验存在性 —— 分组改名不再打断任何密钥（曾经白名单
  存分组名，改名即全体 403，界面上毫无征兆）；删除分组前检查密钥引用，仍被
  引用时 409 并点名密钥。`/v1/models` 同时按密钥的**模型白名单**过滤 ——
  客户端探测到的就是它能调的，不会选一个就被 403 一次。老数据在启动时
  自动迁移（名字条目换算成 ID，幂等；解析不了的保留原样并在日志里点名）
- **故障转移**：上游返回任何非 2xx 状态码都立即切到候选链的下一个渠道；
  客户端主动断开（手动结束推理）除外 —— 收件人已不在，换渠道重发没有意义，
  也不会把这种失败记到渠道头上。429 按 `Retry-After` 冷却、401/403 冷却 30 秒；
  413/414/431（请求物理尺寸超限）第一个渠道就短路返回，不再空试；
  一次转发中多个渠道报出相同的请求形状类 4xx（如都 400）时判定为请求级
  错误，不记渠道失败；渠道连续失败 3 次自动冷却 60 秒，成功一次即恢复。
  换渠道前若**下一个候选仍打在同一台上游**（两条渠道共用同一个 `base_url`），
  先等 `RELAY_RETRY_SAME_UPSTREAM_DELAY`（默认 500ms，可设 0 关闭）——
  零间隔连打同一端点会被上游当成攻击，正是熔断的诱因；换到别的上游则不等，
  那不会给谁造成压力。上游给了 `Retry-After`（429/503）时以它为准，封顶 8 秒
- **报文留存**：`all` / `errors` / `none` 三档，按体积截断，凭据类请求头自动脱敏
- **配置备份**：一键导出/导入分组、渠道（含模型白名单与各自的价格）、密钥与代理，
  渠道密钥以密文保存；导入旧备份时，里面的全局定价会按模型名应用到匹配的渠道模型上
- **无账号的密钥登录**：管理后台只有一把管理密钥（`RELAY_ADMIN_KEY` 环境变量），
  没有用户名、注册与找回。登录换发 HMAC 签名的无状态会话（HttpOnly Cookie，
  默认 7 天），轮换密钥即全体下线；登录接口带按 IP 的暴力破解限流，
  WebSocket 实时推送与全部管理接口同受保护。未设置该密钥时鉴权关闭
  （只绑回环的本地部署不受影响），启动日志与界面横幅持续提醒
- **在线版本检查与一键更新**：侧栏品牌下方常驻一枚版本徽标，点开可看到
  当前版本、检测新版本、一键更新与回滚。按构建形态自动选择更新方式 ——
  官方预编译二进制（`deploy/install.sh` 安装的形态）直接原子替换自己的
  可执行文件，再由 systemd 拉起；源码构建只提示不自动更新（避免用官方
  二进制覆盖开发者本地的构建物）。检测结果服务端缓存 20 分钟，且**检测
  不成功时明说「未能确认」**，不会把一次失败的请求显示成「已是最新」。
  详见「在线更新」一节
- **无充值 / 无金额系统**：金额只做成本感知，不参与任何扣减

## 目录结构

```
llm-relay/
├── backend/                    Go 1.25 + Gin 后端
│   ├── cmd/server/             程序入口
│   ├── cmd/rotate-secret/      主密钥轮换工具（发布归档自带）
│   └── internal/
│       ├── api/                HTTP 路由与处理
│       ├── config/             配置加载（YAML + 环境变量覆盖）
│       ├── model/              数据实体
│       ├── store/              数据库与缓存
│       ├── relay/              协议适配与转发内核
│       ├── pricing/            单价解析与成本计算
│       ├── proxy/              出站代理（socks5/http/https）与连通性测试
│       ├── secure/             AES-GCM 加密与密钥哈希
│       ├── version/            版本号与构建形态（单一来源，见「版本号」）
│       ├── update/             版本检测、下载校验、自替换与回滚
│       └── web/                前端产物 embed
├── frontend/                   Vue 3 + Vite + Ant Design Vue 4
│   └── src/
│       ├── components/         MainLayout / PanelCard / StatCard / VersionBadge
│       ├── views/              各功能页面
│       ├── stores/             主题等状态
│       └── styles/theme.css    设计令牌（实测自参考站）
├── deploy/
│   ├── install.sh              一键部署脚本（下载 Release 预编译二进制 + systemd）
│   ├── restore.sh              备份恢复脚本（原生 psql）
│   └── .env.example            部署配置模板
├── .github/workflows/
│   ├── release.yml             tag 触发：goreleaser 归档 + Release
│   └── ci.yml                  push/PR：后端 vet+test、前端 type-check+build
├── Makefile                    version / build / test / release-snapshot
├── scripts/
│   ├── capture-ui.mjs          CDP 抓取参考站计算样式
│   ├── capture-layout.mjs      CDP 深度抓取 DOM 与 class 规格
│   ├── verify-version-ui.mjs   CDP 验证版本徽标与更新面板
│   ├── lib/testdb.sh|py        测试共用的数据库访问层（原生 psql）
│   └── screenshot.mjs          CDP 页面截图
└── docs/
    ├── ui-spec.md              UI 规格书（实测数据）
    └── layout-*.json           原始抓取数据
```

## 部署方式

### 方式一：脚本安装（推荐）

一键安装脚本，自动从 GitHub Releases 下载预编译的二进制文件（前端产物已
embed 进二进制），装好 PostgreSQL 并注册 systemd 服务。

**前置条件**

- Linux 服务器（amd64 或 arm64），Debian/Ubuntu
- Root 权限
- systemd 运行中（WSL 需在 `/etc/wsl.conf` 里启用 `[boot] systemd=true`）

PostgreSQL 由脚本自动安装并配置，无需预装。

**安装步骤**

```bash
curl -sSL https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.sh | sudo bash
```

**脚本会自动：**

1. 检测系统架构（amd64 / arm64）
2. 从 GitHub Releases 下载最新版本并做 sha256 校验
3. 安装二进制文件到 `/opt/llm-relay`
4. 安装并初始化 PostgreSQL（建库、建用户）
5. 生成配置与随机密钥（数据库密码 / `RELAY_SECRET` / `RELAY_ADMIN_KEY`）
6. 创建 systemd 服务并设置开机自启
7. 健康检查确认服务就绪

**安装后配置**

```bash
# 1. 查看服务状态
sudo systemctl status llm-relay

# 2. 管理密钥在安装结束时打印，也保存在 /opt/llm-relay/deploy/.env
#    （RELAY_ADMIN_KEY，打开管理台时输入它登录）

# 3. 在浏览器中打开管理后台
# http://你的服务器IP:8888
```

**可选参数**（跟在 `bash -s --` 后面，或先 export 再 `sudo -E`）：

```bash
# 例：监听公网 + 自定义端口
curl -sSL https://raw.githubusercontent.com/ashu1800/llm-relay/main/deploy/install.sh   | sudo bash -s -- PORT=9000 BIND_ADDR=0.0.0.0
```

| 变量 | 默认值 | 说明 |
|---|---|---|
| `INSTALL_DIR` | `/opt/llm-relay` | 安装目录 |
| `PORT` | `8888` | 监听端口（写入 `.env` 的 `SERVER_PORT`） |
| `BIND_ADDR` | `127.0.0.1` | 监听地址（写入 `.env` 的 `SERVER_HOST`） |
| `DB_NAME` / `DB_USER` | `llm_relay` / `llmrelay` | 数据库 |
| `VERSION` | 最新 Release | 要安装的版本号（不带 v 前缀） |
| `RELAY_GH_PROXY` | — | GitHub 下载加速前缀（形如 `https://gh-proxy.com`） |

> - 默认只绑 `127.0.0.1`。放到公网时加 `BIND_ADDR=0.0.0.0`，
>   并保管好 `RELAY_ADMIN_KEY`（登录鉴权），建议再加反向代理 + HTTPS。
> - **重复运行就是升级**：数据库密码、`RELAY_SECRET`、`RELAY_ADMIN_KEY`
>   从已有 `.env` 原样沿用，升级前自动备份数据库到
>   `$INSTALL_DIR/backups/`（恢复入口 `deploy/restore.sh`）。
> - 小内存服务器（2 核 1.6G）也能装：不编译任何东西，下载归档约 8 MB。

## 在线更新

管理台侧栏品牌下方那枚版本徽标就是更新入口。点开它可以看到当前版本、
检测新版本、一键更新、以及回滚。

### 两种构建形态，两种更新方式

「更新」能力**取决于这份程序是怎么构建的**（构建时经 `-ldflags` 注入的
`BuildType`，见 `backend/internal/version`）：

| 形态 | 谁注入 | 一键更新 | 原因 |
|---|---|---|---|
| `binary` | goreleaser 发布产物（`deploy/install.sh` 安装的即此形态） | 直接原子替换自己的可执行文件 | systemd `Restart=always` 负责拉起新版本 |
| `source` | 兜底（本地 `go build` 未注入时一律按它处理） | **不允许** | 避免用官方二进制覆盖开发者本地的构建产物 |

`source` 形态下界面会说明原因并给出发布页链接，而不是显示一个点了没反应的按钮。

### 更新流程

一键更新 = 后台任务「下载目标版本归档 → sha256 校验 → 原子替换可执行文件 →
人点一下重启」。几个刻意的取舍：

- **更新是异步任务**：跨洋下载归档是分钟级操作，同步 HTTP 必被浏览器或
  反向代理掐断。界面轮询进度（阶段 + 百分比 + 日志），刷新页面也不丢。
- **校验和缺失即拒绝安装**：这个功能的本质是「从网络上下载一个可执行文件
  并让它以服务身份运行」，校验和是最后一道完整性检查。
- **替换前留 `.backup`**：旧版本始终在场，回滚不依赖网络。
- **更新完不自动重启**：替换后如果新版本启动就崩，自动重启会把服务变成
  崩溃循环，而此刻留在旧进程上的管理台正是唯一的救援入口。
- 已是最新时任务正常结束并如实说明，不会假装更新了一遍。

### 检测与限额

检测走 GitHub Releases API。**未配 token 时限额只有每小时 60 次**，
因此：

- 只在打开面板时才检测，关闭状态下不发请求；
- 结果在服务端缓存 20 分钟（点刷新可跳过缓存）；
- 上游是 GitHub 不可达时，面板显示「未能确认是否最新」——
  不会谎称「已是最新」。这个区分很重要：前者要你去看网络，
  后者让你安心走开。

如果所在网络访问 GitHub 不稳定，可以在「代理管理」里给某个代理勾选
**用于自动更新**（版本检测与下载都走它，本机/内网代理如 `127.0.0.1:7890`
也可用），或在 `系统设置 → 版本更新` 里填一个备用代理地址或 token
（token 只发往 `api.github.com`，跨域重定向时会被主动剥掉）；
两处都配时勾选的代理优先。

### 回滚

| 方式 | 依据 | 说明 |
|---|---|---|
| 本地回滚（不指定版本） | 可执行文件旁的 `.backup` 文件 | 完全不联网，GitHub 不可达时依然可用；回滚本身也可撤销 |
| 下载指定版本 | Release 列表（比当前版本旧、且非预发布） | 目标必须在白名单列表里 —— 没有它，调用方可以传任意 tag，把未经发布验证的产物装上服务器 |

面板里还会列出「比当前版本旧、且非预发布」的历史版本，可以选一个回滚。

### 版本号

版本号的**单一来源是 `backend/internal/version` 包**，按优先级取三处：

| 优先级 | 来源 | 用在什么场合 |
|---|---|---|
| 1 | `-ldflags -X llm-relay/internal/version.Version=...` | 正式发布（goreleaser 注入） |
| 2 | `//go:embed VERSION` 文件 | 直接 `go build` 时兜底 |
| 3 | `dev` | 以上都没有 |

必须有兜底：手写的版本常量迟早和实际代码对不上，而「界面上显示的版本
是不是正在跑的那份代码」正是它唯一要回答的问题。发布流水线在打 tag 时
把版本号写进 VERSION 文件并回写 main，与 goreleaser 的注入互为校验。

确认线上版本：

```bash
curl -s http://127.0.0.1:8888/system/info | python3 -m json.tool
```

### 发布流程（本仓库维护者）

**发布 = 一条命令。** 服务器侧没有「部署」这个动作 —— 实例通过自动更新
自己拿新版本，人只负责把版本号命名出来：

```powershell
pwsh -File scriptselease.ps1 patch     # 或 minor / major / v0.1.5 / --dry-run
```

（Linux/macOS 直接跑 `bash scripts/release.sh`，两者同一实现，ps1 只是
Windows 侧把 gh 的 PATH 拼好再转发。）

脚本依次完成：前置检查（main 分支、工作区干净、gh 已登录）→ 本地预检
（go vet/test、前端 type-check 与契约检查 —— release.yml 会再跑一遍，
这里先跑是把失败拦在打 tag 之前，tag 一推就是「已发布」状态，不可收回）→
按 semver 递增算出新版本号 → 推送 main → 打 tag 推送（触发流水线）→
`gh run watch` 观察到全部 job 结束 → 验证 Release 归档与 checksums.txt
真的在场（归档名是安装脚本与在线更新拼下载 URL 的硬契约）。

发布完成后，服务器上**不需要任何操作**：

```
管理台 → 系统设置 → 版本更新 → 检测更新 → 一键更新
（下载归档 → sha256 校验 → 原子替换 → systemd 拉起，约 1-2 分钟）
```

也可以手工打 tag（脚本做的事等价于）：

```bash
git tag -a v0.1.2 -m "v0.1.2" && git push origin v0.1.2
```

tag 触发 `.github/workflows/release.yml`，它会：

1. 跑测试，并把版本号写进 `backend/internal/version/VERSION`；
2. 用 goreleaser 构建 5 个平台的归档（含 `rotate-secret` 工具）
   + `checksums.txt`，创建 Release；
3. 把 `VERSION` 回写到主分支，让下次构建的兜底版本号跟上。

产物命名是硬契约，`deploy/install.sh` 与在线更新按它拼下载地址：

```
llm-relay_<版本>_<系统>_<架构>.tar.gz     # windows 用 .zip，版本号不带 v
  └── llm-relay / llm-relay.exe          # 归档内的主可执行文件
  └── rotate-secret                      # 主密钥轮换工具（linux 归档）
  └── deploy/                            # install.sh / restore.sh / .env.example
checksums.txt                            # sha256，两空格分隔
```

也可以在本机试跑：`make release-snapshot`（需先装 goreleaser）。

## 密钥管理（重要）

本站有两把相互独立的密钥，不要混用：

### 管理台登录密钥 `RELAY_ADMIN_KEY`

无账号模型的登录凭据：打开管理台时输入它换取会话。

- 首次安装自动生成 48 位随机值，`install.sh` 会在部署摘要里打印；
  也可以自己设置（**至少 16 位**，太短会拒绝启动）
- 只在启动时取哈希比对，**不落库、不进配置备份**，泄露后改 `.env` 重启即可
- 轮换并重启后所有已登录会话立即失效；会话有效期 `RELAY_SESSION_TTL` 默认 168h

### 渠道密钥加密主密钥 `RELAY_SECRET`

渠道里的上游密钥以 **AES-256-GCM** 加密后存库，密钥由 `RELAY_SECRET` 派生。

- `install.sh` 首次安装会自动生成 48 位随机 `RELAY_SECRET`
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
journalctl -u llm-relay -f                 # 实时日志
bash deploy/restore.sh --list              # 查看升级前的自动备份
```

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

开发态代理有两处**必须显式配**的地方（都在 `vite.config.ts` 里，附原因注释），
少任何一个的表现都是「页面能看，但实时不更新」：

- `ws: true`：不声明的话 Vite 不把 WebSocket 升级交给代理，`/api/admin/live` 永远连不上；
- `headers: { Origin: 'http://127.0.0.1:8888' }`：后端有同源校验
  （`sameOriginOnly` 比 Origin 与 Host），而 `changeOrigin` 只改 Host ——
  浏览器发来的 Origin 仍是 5173，于是**带 Origin 的请求**（WebSocket 握手、
  所有写操作）一律 403。同源 GET 不带 Origin，所以「读」一直是好的，
  很容易被误判成后端的问题。

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
| `SERVER_HOST` | `127.0.0.1` | 监听地址。`deploy/.env` 与 `install.sh` 的 `BIND_ADDR` 参数写的都是它 |
| `TZ` | `Asia/Shanghai` | 进程本地时区：可用时段与时段倍率按它判断。systemd 服务默认没有时区，官方部署的 `.env` 里显式写了这一项 |

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
布局是**应用外壳**：高度锁在视口里、只有内容区滚，品牌在侧栏；
主题切换在看板页工具栏右上角（2026-09-26 从侧栏底部迁来，灯泡形态不变）
（参考站那条 64px 顶栏装的是公告条与顶部导航，我们这边只剩品牌一个元素，
留着就是一行空白，所以去掉了 —— 参考站自身的尺寸仍记录在 `docs/ui-spec.md`）。

鼠标光标是本项目自己的品牌元素（参考站没有）：默认箭头与交互手型**每个主题
各有一套**，四份图形在 `frontend/public/cursor-arrow-light.svg`、
`cursor-hand-light.svg`、`cursor-arrow-dark.svg`、`cursor-hand-dark.svg`。
两套不是同一张图换个色 —— 浅色是长而窄的锐角轮廓（高宽比 1.47），
深色是短而阔的圆角轮廓（1.19），并列时一眼能分出是两套。填充与描边取自各主题
「主色实心块」的那一对变量（浅色 `#b15840` 填充 + 白描边，实测对页底 4.44:1；
深色 `#e8a48c` 填充 + `#3a241d` 描边，对面板 6.36:1）。两套的热区坐标
（箭头 `2 2`、手型 `9 2`）必须逐字相同，否则切主题时点击落点会跳。
输入框 I 型、拖拽、禁用、帮助四类保留系统光标。
要改光标形状或补齐 antd 升级后新增的交互组件，看 `frontend/src/styles/theme.css`
的「自定义光标」段 —— 那里记着清单的来源、为什么必须 `!important`、
为什么文件名带主题后缀（这几个资源没有 ETag/Last-Modified，缓存只能靠 URL 判断），
以及 `npm run check` 里盯住哪几条。形状的生成/核对工具在 `.shots/`（不入库）：
`gen-cursor-dark.mjs` 生成深色路径、`measure-cursors.mjs` 用浏览器 `getBBox()`
量实际尺寸、`contrast.mjs` 算对比度 —— 注释里引用的数字都出自这三个脚本。

## 开发进度

- [x] Phase 0 项目骨架：Go+Gin 服务、Vue3+AntdV 前端、一键部署脚本
- [x] Phase 1 UI 逆向与设计系统：CDP 抓取、`ui-spec.md`、主题令牌、MainLayout、看板骨架
- [x] Phase 2 数据层与转发内核：实体与迁移、渠道路由（加权/轮询/故障转移）、协议适配
- [x] Phase 3 计量与成本：Token 计量归一化、手工定价、时段倍率（价格不再自动同步）
- [x] Phase 4 全协议与页面完善：Chat / Responses / Anthropic / Gemini / Embeddings 入站
- [x] Phase 5 健壮性与可观测：报文留存、限流与并发、配置备份
- [x] Phase 6 部署固化与冷启动验收：一键安装（Release 预编译二进制 + systemd）、密钥轮换、重装演练

## 验证脚本

`scripts/` 下均为可直接运行的端到端验证，密钥从环境变量取：

> 登录鉴权上线后，调用管理接口的脚本会自动适配：它们统一 source 了
> `scripts/admin-auth.sh`，会从 `RELAY_ADMIN_KEY` 环境变量或 `deploy/.env`
> 里取管理密钥登录并给后续 curl 注入会话 Cookie；鉴权关闭（未配置密钥）时
> 静默跳过，行为同从前。服务不在 8888 端口时用 `ADMIN_BASE` 指定地址。
>
> 查库类断言统一走 `scripts/lib/testdb.sh`（bash）与 `scripts/lib/testdb.py`
> （python）：原生 psql over TCP，连接参数取自 `deploy/.env`（可用
> `RELAY_TEST_DB_*` 环境变量覆盖），不再依赖任何容器。依赖 mock 上游的
> 用例（分组路由、流式截断、重试退避……）以**宿主 node 进程**跑
> mock（≥18 即可，`bash scripts/ensure-mock-upstream.sh` 自动拉起）。

| 脚本 | 验证内容 |
|---|---|
| `test-regression.sh` | 全部管理接口 + 四种协议端点连通性 |
| `test-ratelimit.sh` | 密钥级 RPM 放行/拒绝、`Retry-After` |
| `test-key-group-refs.py` | 密钥分组白名单存 ID、不存在引用创建即 400、`/v1/models` 按模型白名单过滤、删分组检查密钥引用 |
| `test-payload.sh` | 报文留存三档模式与凭据脱敏 |
| `test-pricing-filter.sh` | 渠道列表的「未定价」计数 |
| `test-pricing-rules.sh` | 时段倍率优先于固定倍率、跨午夜窗口、最后一条生效 |
| `test-multiplier-precision.py` | 倍率四位小数端到端往返：设 0.1875 → API 读回 → **直接查 Postgres 列**确认 0.1875 未被截断；边界值 0.0001 / 1.2345 / 0.1234 / 0.5；并如实记录 5 位小数打 API 时库会按 `numeric(10,4)` 截断（前端两处已拦，见下） |
| `test-group-quota.sh` | 分组 RPM / TPM 配额放行与拒绝 |
| `test-proxies.sh` | 代理 CRUD、连通性与延迟测试、被引用时禁止删除 |
| `test-egress-proxy.sh` | **请求确实经代理出网**（假域名 + 代理改写证明） |
| `test-live.sh` | WebSocket 握手、统计快照、新请求的日志推送 |
| `test-cost.sh` | 计费口径：缓存读写、推理 Token、子集型与并列型缓存 |
| `test-default-model.sh` | **默认模型映射**：兜底改写、精确优先、密钥白名单不豁免、日志标记与计费口径 |
| `test-thinking-map.sh` | **思考强度跨协议落地**：mock 上游断言 Anthropic 收到 `output_config.effort`、Gemini 收到 `thinkingBudget`、OpenAI 兼容上游收到原生档位（跨协议归一出的 off/auto 被剔除），以及 anthropic→anthropic 的无损往返 |
| `test-legacy-column-add.sh` | 老库缺列时自动补列并恢复可用（会短暂重启应用） |
| `test-backup.sh` | 备份导出/导入、明文泄漏检查 |
| `test-backup-roundtrip.sh` | 删除渠道后从备份恢复并真实调用 |
| `test-coldstart.sh` | 重装演练：重跑 `install.sh`（钉住当前版本），核对数据完好、密钥沿用、服务健康 |
| `check-secrets.sh` | 扫描仓库与提交历史中的明文凭据 |
| `purge-test-logs.sh` | 清掉验证脚本产生的请求日志（否则会污染看板的今日统计） |

另有一批 python 用例（`test-group-update.py`、`test-key-whitelist.py`、
`test-key-group-refs.py`、`test-delete-semantics.py`、`test-accept-encoding.py`、
`test-log-filters.py`、`test-csrf.py`），覆盖分组更新、密钥白名单（分组引用
形态与模型过滤）、删除语义、压缩协商、
日志筛选与同源校验。

### 在 Windows 上跑这些脚本：`scripts/wsl-bash.psm1`

这些脚本都在 WSL 里跑，而从 Windows PowerShell 直接调 `wsl` 会把三层解析
（PowerShell → wsl.exe → bash）的坑同时引出来。这个模块把它们一次解决：

```powershell
Import-Module .\scripts\wsl-bash.psm1

# 跑脚本文件（推荐）：内容由 bash 直接读，中文与各种引号都不需要转义
Invoke-WslScript -Path .\deploy\install.sh

# 跑一条内联命令：**用单引号**，避免 PowerShell 先做变量插值
(Invoke-WslBash -Command 'systemctl is-active llm-relay').Output
```

| 坑 | 症状 | 解法 |
|---|---|---|
| wsl.exe 的输出编码 | 每次调用都刷一屏乱码 | 模块设 `WSL_UTF8=1`；**不要**靠重定向 stderr 了事（会把真报错一起丢掉） |
| 那条固定的 localhost 代理警告 | 混进输出，干扰解析 | 模块按前缀丢弃**这一条**已知噪音，其余 stderr 全部保留 |
| PowerShell 双引号会插值 | `"$HOME"` 在送到 bash 前就被替换成空串 | 内联命令一律用**单引号**；`$(...)`、反引号、`$` 在单引号里都是字面量 |
| Windows 路径不能直接给 bash | `bash C:\foo.sh` → 127 | 模块用 `wslpath -u` 转换（不要手拼 `/mnt/c/...`） |
| `Set-Content` 写 CRLF/BOM | `bash: $'\r': command not found` 或 shebang 失效 | `New-WslScript` 按 LF + UTF-8 无 BOM 写，并 `chmod +x` |
| `-lc` 会读 ~/.profile | 启动文件可能污染退出码（命令成功却是 1） | 模块用 `-c` 起干净的非登录 shell |


`scripts/audit-cursors.mjs` 是前端的光标审计（需要先起一个带调试端口的 Chrome，
用法见文件顶部）：把七个路由连同弹窗、抽屉、下拉里每个元素的**计算光标**统计
一遍，确认没有一处退回系统光标。它存在的理由很具体 —— **CSS 的 cursor 不进截图**，
少写一个选择器、antd 升级换了 class 名、光标图片 404，表现都只是那一处悄悄变回
系统箭头，不报错也不失败：

```bash
node scripts/audit-cursors.mjs http://127.0.0.1:5173   # 开发态
node scripts/audit-cursors.mjs                          # 默认打 127.0.0.1:8888
node scripts/audit-cursors.mjs http://127.0.0.1:8888 light   # 只审浅色（默认两个主题都跑）
```

每个用例在两个主题下各跑一遍（靠预置 `localStorage` 里的 `llm-relay-theme`
切主题，导航前注入，所以首屏那批元素也在审计范围内）。深色页面上出现
**浅色那套**文件名判失败，单独归为「串主题」一类打印 —— 只检查「是不是自定义
光标」的话，深色块漏配覆盖时会继承 `:root` 的值，两个主题的审计报告会一模一样
全绿，而「绿得看不出区别」的检查等于没检查。这条判断本身有反向验证：
`.shots/verify-cross-theme-detection.mjs` 会手动把深色页面的光标变量改写成浅色
那套，确认审计真的报出 1062 个串主题箭头 + 205 个串主题手型。

`scripts/check-table-widths.mjs` 与 `scripts/check-log-columns.mjs` 一起守请求日志
表格的列宽。列宽 2026-09-23 起由脚本按当前页内容量出来（见 `docs/ui-spec.md` 第 17 条），
于是「列宽对不对」不再能从源码里一眼看出来：

- **check-table-widths.mjs**（静态，不需要浏览器）：每个表的 `scroll.x` 与各列声明宽度
  要对得上。运行时表（列宽来自 `colW`）它查的是「**每一列都绑了那个表达式**」——
  混进一个裸数字，那一列就不会随测量联动，表现为它永远停在声明值上。这个脚本此前
  只认数字字面量，请求日志表改成 `colW.xxx` 之后**整张表被静默跳过**，
  「没报错」于是被读成「没问题」，所以现在它会把「跳过」明确打出来并给出退出码；
- **check-log-columns.mjs**（需要调试端口的 Chrome）：静态检查只能确认「测量代码接对了」，
  而「量出来的宽度是否真的装得下内容」只有渲染后才知道 —— 这类问题不报错，
  内容被省略号截掉、页面一切正常。它按「3 视口 × 3 种每页条数」逐格断言三件事：
  锚点元素没有溢出（内容没被截断）、单元格自身没有横向溢出、横向滚到最右端时
  右侧固定列不遮挡内容。每页条数是从源码读出的 localStorage 键名写进去的，
  并断言行数真的等于这一轮的 pageSize —— 否则整套矩阵会跑在同一个行数上，
  看着全绿其实只测了一种（第一版就是这么错的）。

```bash
node scripts/check-table-widths.mjs                                # 静态，秒出
node scripts/check-log-columns.mjs                                 # 默认 3 视口 × 20/50/100 条
node scripts/check-log-columns.mjs http://127.0.0.1:8888 1440 20   # 单组合
```

`scripts/verify-all.sh` 会按顺序跑完上面这些可离线执行的用例并汇总，
最后打印 `ALL_PASS`；日常改完代码跑它一次就够。
（`test-coldstart.sh` 要重跑 `install.sh` 重启服务，不在其中。）

界面动效的回归脚本在 `.shots/`（不入库，随源码本地保留）。它们都需要一个带
调试端口的 Chrome，用真实数据造场景，所以不进 `verify-all.sh`：

```bash
# 带调试端口起一个专用 Chrome（Windows）
chrome.exe --headless=new --remote-debugging-port=9222 \
           --user-data-dir=%TEMP%\chrome-cdp about:blank

node .shots/verify-log-effects.mjs layout   # 入场动效三档不许参与布局
node .shots/verify-log-effects.mjs sweep    # 默认档：亮带贴行底、2.4s 后撤掉
node .shots/verify-live-sync.mjs            # 新行与卡片数字必须同时到达（见 ui-spec 第 16 条）
node .shots/verify-live-sync-filtered.mjs   # 同上，但先切到「近 7 天」—— 非默认视角走的是
                                            # 静默重取那条路，与默认视角的推送合并不是同一条代码路径
node .shots/verify-col-adaptive.mjs         # 列宽是否跟着内容走（ui-spec 第 17 条）：插两种长度的
                                            # 模型名探针，模型列应 194 → 254 → 300（软上限），
                                            # 删掉探针行后回落 —— 走的是实时推送那条重算路径
node .shots/verify-reliability.mjs          # 可靠性三件套端到端：日志队列丢弃告警、渠道运行态、
                                            # 日预算告警（会临时设一个必超支的预算，跑完自动清除）
node .shots/verify-delight.mjs              # 观感增强端到端：脉搏条、页签心跳、昨日战报、
                                            # 里程碑彩带、渠道生命灯、主题扩散
node .shots/verify-failonly.mjs             # 仅失败深链：?status_class=error 进来时列表只出现
                                            # 失败请求、看板工具栏标签同步出现、关掉恢复全量
                                            # （面板顶部那个「仅失败」开关 2026-09-26 已移除）
node .shots/verify-ws-heartbeat.mjs 8899 60 # 心跳帧本身：60 秒窗口内按 25 秒节拍到达、
                                            # 是空帧、不超上限（走服务的临时实例，见下）
node scripts/verify-version-ui.mjs          # 版本徽标与更新面板：徽标可见、只出现一次版本号、
                                            # 面板 teleport 到 body 后不再被侧栏的 overflow 裁掉、
                                            # 展开回滚区后仍在视口内、正文可滚动、
                                            # 且「检测失败」时不许出现绿勾与「已是最新」
bash scripts/verify-release-artifacts.sh    # 产物命名契约（**本机需装 goreleaser**，会真跑一次
                                            # snapshot 构建）：归档名必须是
                                            # llm-relay_<版本>_<系统>_<架构>.tar.gz（windows 用
                                            # .zip）、内含可执行文件 llm-relay 与 rotate-secret、
                                            # checksums.txt 是 sha256 两列格式、且 deploy/ 下
                                            # install.sh 要用到的文件都在归档里。
                                            # 这个契约跨 Go 模板 / YAML / shell 三处，读配置看不出
                                            # 对错 —— 一旦漂了，第一次发布时所有平台的下载都会 404
node .shots/verify-focus-ring.mjs           # 键盘焦点环：Tab 走查 6 个路由，断言全部落点
                                            # 只有一套环、没有一个是看不见的（画在 0×0 或
                                            # opacity:0 的元素上就算看不见），且环色对 halo
                                            # 与页面底色的对比度都 ≥ 3:1（WCAG 1.4.11）
node .shots/verify-live-insert.mjs           # 实时插行：鼠标停在表体上、列表滚到中段、
                                            # 详情抽屉开着时，新行都直接落到第一行
                                            # （不再有「有 N 条新日志」提示条 —— 那套挂起
                                            # 机制 2026-09-25 已移除，见特性里的说明）；
                                            # 同一条用例里还验「正在读的那一行不被顶走」——
                                            # 插行前记下视口首行的偏移，插行后必须原样
node .shots/verify-log-sort.mjs             # 排序与快速跳页：三列可排（aria-sort 跟着变）、
                                            # 耗时升/降真的按大小排、费用按币种分组后再排
                                            # （升序首页全是「-」，降序首页是真实金额）、
                                            # 第三次点击回默认、快速跳页能直达第 37 页
node .shots/verify-page-title.mjs           # 页面标题：六个管理页各是「页面名 · LLM Relay」
                                            # 且互不相同、页面名在站点名前（标签栏从右往左
                                            # 截断）、站内跳转后跟着变。鉴权开启的部署会先
                                            # 用 RELAY_ADMIN_KEY 换会话再走查
node .shots/verify-multiplier-precision.mjs # 倍率四位小数（界面侧）：进渠道编辑 → 模型行的
                                            # 定价胶囊 → 固定倍率框键入 0.1875 → **失焦**
                                            # 后仍是 0.1875（老代码在这里被舍成 0.19）

# 量「差几像素」的那几处对齐（ui-spec 第 18、19 条）。这类问题肉眼看得见、说清很难，
# 所以脚本直接把盒模型摊开：左右留白各是多少、中心线落在哪
node .shots/measure-sidebar.mjs             # 侧栏：图标 / 圆标 / 底部两枚按钮的中心线
                                            # 是否都落在中轴（窄视口下侧栏默认收起，脚本先看状态）
node .shots/measure-think-pill.mjs max      # 思考档位胶囊**真正渲染出去**的颜色与对比度
                                            # （三处 color-mix 都从基色算出来，只看基色看不出结果），
                                            # 浅深两个主题各截一张图
node .shots/check-table-tags.mjs            # 各列表页 td 里的标签是否居中 —— antd 给 .ant-tag
                                            # 默认带了 8px 右边距，单标签格会因此偏左 4px
node .shots/verify-sidebar-footer.mjs       # 侧栏底部两枚文字按钮不被截断（2026-09-25 站主截图
                                            # 反馈的「收…」「退…」）：展开态标签 scroll ≤ client
                                            # 且容得下文字实宽、两枚等宽左缘对齐；收起态两枚
                                            # 图标中心线都落在中轴（ui-spec 第 19 条）
node .shots/verify-sidebar-theme-btn.mjs    # 看板工具栏右上角灯泡的命中区（2026-09-26 从侧栏
                                            # 底部迁来）：用 CDP 真实鼠标事件点，断言命中的
                                            # 不是被邻居盖住的图层，且点灯泡真的切换主题并落盘
node .shots/repro-sidebar-footer.mjs        # 页脚盒模型摊开（量「差几像素」时用）：
                                            # 每个按钮的宽度 / 标签 client 与 scroll / 是否截断
node .shots/verify-sidebar-short.mjs        # 矮视口（420/520/620 高）下页脚变高没有顶掉菜单：
                                            # 菜单与页脚不重叠、滚到底两枚动作都在视口内
```

三者都会往 `request_logs` 插探针行（trace_id 前缀 `logfx-probe` /
`live-sync-probe`），跑完按前缀删掉；插入的是真日志，后端会照常推送，
所以验的是「线上收到新日志」这条路，而不是模拟事件。

## 安全提醒

管理后台采用**无账号的密钥登录**：全站只有一把管理密钥
（`deploy/.env` 里的 `RELAY_ADMIN_KEY`，`install.sh`
首次安装会自动生成 48 位随机值并在摘要里打印），浏览器打开管理台时
输入它换取登录会话（HttpOnly Cookie，默认 7 天，`RELAY_SESSION_TTL` 可调）。
登录接口带暴力破解限流（15 分钟内错 5 次锁定），经 HTTPS 反代部署时
会话 Cookie 自动附加 `Secure` 标志。

- **轮换 `RELAY_ADMIN_KEY` 并重启后，所有已登录会话立即失效** ——
  这是无状态会话唯一的吊销手段，怀疑泄露时就轮换
- 未设置该变量时登录鉴权整体关闭（只绑 `127.0.0.1` 的本地部署可以接受），
  启动日志与系统设置页会持续显示警告横幅
- **公网部署必须先设好 `RELAY_ADMIN_KEY` 再把 `SERVER_HOST` 改成 `0.0.0.0`**，
  并建议加反向代理 + HTTPS（Caddy / Nginx 均可）。经反代时按
  `SERVER_TRUSTED_PROXIES`（默认只信回环）采信 `X-Forwarded-For`，
  登录限流才能拿到真实客户端 IP

渠道里的上游密钥用 `RELAY_SECRET` 做 AES-GCM 加密后入库，所以这把主密钥
务必自己生成并保管好：留空会退回程序内置的公开默认值，等于没有加密。
管理密钥与加密主密钥**相互独立、不得相同**（启动时会拒绝相同值）：
前者出现在每个登录请求里，泄露面更大。

## 许可证

[MIT](LICENSE)
