# 开发、验证与发布细节

> 2026-09-27 从 README 拆出：README 只保留使用与快速上手，本文件收录面向维护者的完整细节 —— 本地开发的坑、实现取舍、验证脚本、发布流程与 UI 还原说明。

## 本地开发补充

### 前端开发代理的两个必配项

开发态代理有两处**必须显式配**的地方（都在 `vite.config.ts` 里，附原因注释），
少任何一个的表现都是「页面能看，但实时不更新」：

- `ws: true`：不声明的话 Vite 不把 WebSocket 升级交给代理，`/api/admin/live` 永远连不上；
- `headers: { Origin: 'http://127.0.0.1:8888' }`：后端有同源校验
  （`sameOriginOnly` 比 Origin 与 Host），而 `changeOrigin` 只改 Host ——
  浏览器发来的 Origin 仍是 5173，于是**带 Origin 的请求**（WebSocket 握手、
  所有写操作）一律 403。同源 GET 不带 Origin，所以「读」一直是好的，
  很容易被误判成后端的问题。

### 构建后同步产物到后端嵌入目录

```bash
rm -rf backend/internal/web/dist && cp -r frontend/dist backend/internal/web/dist
```

## 实现细节笔记

特性列表只写结论，这里记的是支撑那些结论的取舍与实测。编号引用的「ui-spec
第 N 条」见 `docs/ui-spec.md`。

### 请求日志的筛选语义

- 界面上的「导出」按钮已去掉（2026-09-16），接口 `GET /api/admin/logs/export`
  仍在，需要时直接调它，参数与列表接口完全一致。
- 列表就在**数据看板**页（四张概览卡下面）。它**不吃**上面那组筛选，
  恒定显示**全部最新请求**（2026-09-16 起）—— 列表的用处是盯着最新发生了什么，
  筛过之后反而看不到刚进来的调用，而刚进来的几条恰恰最该被看到。
  筛选用在上面那四张概览卡上：时间范围 / 渠道 / 模型，这三个会**记在本地**，
  刷新或下次打开还是同一个视角，存下来的渠道 / 模型若已被删掉则自动退回「全部」。
- 工具栏上**没有**「筛选只作用于上方卡片」这类作用范围提示（2026-09-17 应站主要求
  移除）—— 列表不吃筛选这件事由契约检查钉住，不再靠界面文案说明。
- trace_id 与「只看失败」这两个排障入口**仍然作用于列表**，但只走 URL：
  `?trace_id=…`（或在日志详情里点「只看这条链路」）与 `?status_class=error`；
  激活时看板工具栏上会出现一个可关闭的小标签。列表顶部的「仅失败」切换按钮
  与「实时 · 最后更新」状态行已于 2026-09-26 应站主要求移除。
  这两个条件是临时的，不跟着记住；而且**带这两个参数的链接不受本机记住的筛选影响**
  （同一个链接发给谁、隔多久打开，看到的都是同一批记录）。
- 旧地址 `/console/logs` 会带着 query 跳到看板，老的链接与书签仍然可用。

### 排序与快速跳页（2026-09-24 加）

耗时 / 费用 / 状态三列点表头即可按服务端排序（`sort=elapsed|cost|status`，
带 `-` 前缀为降序），第三次点击回到「最新在前」。**排序必须由服务端做** ——
前端只能排当前页那 20/50 条，而「最慢的一单」「最贵的一单」显然不在当前页。
费用那一列另有一条口径：站里不做汇率换算，所以后端**先按币种分组、再在组内
按金额排**（`cost_currency` 排在 `estimated_cost` 之前），界面上会同时给出一行说明 ——
否则 `¥2.35` 排到 `$0.0007` 后面会被读成「美元更贵」。分页器另加快速跳页
输入框：25000 条按 20 条一页是 1260 页，只靠 `•••`（一次 5 页）等于到不了
中间那些页。

### 实时推送

- WebSocket 推送看板数值与请求日志，数字用滚动动画过渡、日志插到第一行，
  都不重绘整页。**有新日志时两帧走同一拍**：日志循环查到新行后会立刻叫统计
  循环算一次，所以标记新行的入场动画与卡片数字的滚动是同时开始的
  （从前两者是两个互不相干的定时器，新行先到、数字最多晚两秒，
  见 `docs/ui-spec.md` 第 16 条）。推送本身固定是「今天 + 全站」的视角：
  列表没有额外条件时直接把新行插到第一行；**带 trace / 仅失败深链时则借这个
  推送信号安静地重取一次当前视角** —— 新日志符不符合条件只有服务端知道，
  客户端不去重复实现一遍筛选语义。
- 服务端每 25 秒发一个空的心跳帧（2026-09-24 加）：三条推送都是「变了才推」，
  闲时一帧不发，而客户端的看门狗是「45 秒没收到任何消息就当断开」——
  没有心跳的话，深夜没有流量时一条完全正常的连接会被判成断开并反复重连。
  心跳只证明链路活着，**不代表有新数据**。
- 「实时 · 最后更新」状态行移除后，WebSocket 断线仍会自动重连，
  数据停止跳动即是断线的表现。

### 新日志直接进列表（2026-09-25 改回）

实时推送带来的新行一律插到第一行，不等待任何交互。此前有过一版「挂起」机制
（9e9f3b4，P1-6）：表体滚过中段、鼠标停在表体上、或详情抽屉开着时把新行攒起来，
表格上方显示「有 N 条新日志，点击查看」。它的判据里「鼠标停在表体上」在真实
使用中几乎是常驻状态（正看着某一行、或在等新行出现），而释放只发生在滚动 /
鼠标移出 / 抽屉关闭三个时刻，于是新行只攒不落、列表看起来冻住了 —— 站主反馈
「只显示提示，但是列表不更新，鼠标动一下才更新」。

新行插到头部必然让内容下移一行（这是「最新在最上面」的定义），位置稳定由
前端自己用 JS 显式做：插行前量出「视口里第一个可见行」相对表体顶部的偏移，
插行后按差值补 `scrollTop`，那一行就仍停在原处（只在用户已经滚动过时补偿；
停在顶部时用户要看的正是刚进来的几条）。**不依赖浏览器的原生滚动锚定**：
实测（2026-09-25）Chrome 的 `overflow-anchor` 在最小页面上有效，在这张日志表上
却不生效 —— 纯 DOM 插入、余量充足、祖先链无 `none` 的情况下仍不补偿，
且已排除固定列 sticky、table-layout、`overflow: hidden` 三种因素。

### 新日志扫光

新行插进来时，沿这一行的下分割线从左扫过一道彩虹（约 2 秒，见
`docs/ui-spec.md` 第 11 条）。只有实时推送带来的新行会闪：刷新页面、改筛选、
翻页、点「刷新」都不闪，系统开了「减弱动态效果」时也不闪。亮带画在表格外面的
独立一层里，**不往表格里加任何元素** —— 往 `tr` 里放伪元素会让 Chrome 在
`table-layout: fixed` 下不再分配多余宽度，整表塌回声明宽度、右侧空出一条
（只在窗口比表格宽时出现，2026-09-16 修）。

### 表格里几处「差几像素」的规格

- **两排数值的列左缘对齐**：「词元」「任务耗时」两格里的内容块居中，但轨道
  宽度写成了定值 —— `auto` 轨道会跟着数值长短伸缩，整块宽度逐行不同，竖条和
  图标因此每行落在不同的 x 上（宽窗口实测漂 7.2 / 9.38px）。定值取「列最窄时
  可用宽度」倒推，任何窗口下都不会溢出（见 `docs/ui-spec.md` 第 10 条）。
- **任务耗时的双色竖条**：竖条断成两段，上段跟「首字」、下段跟「耗时」，
  段色各自继承该行数值的 `currentColor`。首字 ≤10s 绿 / 10-30s 橙 / >30s 红，
  耗时 ≤20s 绿 / 20-60s 橙 / >60s 红 —— 两行各用一套阈值（首字才是「卡不卡」
  的那一下，总耗时含上游生成本来就长）；一整条单色只能表达「这次调用慢」，
  断成两段才看得出慢在哪一段。秒值一律补齐两位小数（`8.70s`），
  不足 1 秒仍按毫秒显示（见 `docs/ui-spec.md` 第 6 条）。

### 默认模型映射（兜底）

每条渠道可单独开启，指定一个白名单里的模型。开启后，**白名单没精确命中的
请求**一律改用这个模型发往上游；精确命中的仍按白名单走（精确优先，兜底候选
永远排在最后）。这是为 Claude Code 这类自己挑模型名的客户端准备的：它发来的
`claude-sonnet-4-6` / `claude-opus-4-7` 不在任何渠道白名单里，从前会得到
耗时 1–3ms、token 全 0 的 502（模型名原样转发给上游，被上游当场拒绝）。
开了兜底就不必每次回来改白名单。三件事要知道：

- **日志里请求模型仍记客户端的原名**，上游模型记实际发往上游的名字，并带
  「兜底」标记 —— 开启了兜底之后**模型名写错也不再报错**，这个标记是发现
  「其实没命中」的唯一途径。兜底目标行配了「对外名 → 上游名」映射时，映射
  照常生效（2026-09-21 修复：此前映射被忽略，上游收到它不认识的对外名，
  复现兜底要消灭的那种 502）。
- **费用按兜底目标的单价计算**（客户端的模型名在渠道里通常没配价）。
- **响应里的模型名是上游回显的那个**（即兜底目标），不是客户端请求的名字 ——
  这是**既有行为**，与兜底无关：任何配了「上游模型名」的渠道都这样（响应改写器
  优先取上游响应体里的 model 字段，取不到才回落到请求名）。客户端拿到一个
  与自己请求不同的模型名是预期内的。

另：**密钥的 `allowed_models` 白名单不被兜底豁免**（配了限制就仍然 403，
不会被兜底绕开）；兜底目标必须是该渠道白名单里**启用中**的条目 —— 保存时拦
（目标行停用也拦，2026-09-21 起），分两步造出的半残（先配好、事后停用那一行）
由失败提示点名、渠道列表的「兜底」胶囊不亮；**备份导入**带进来的半残配置会被
剥离并写进导入报告。

### 故障转移细节

- 上游返回任何非 2xx 状态码都立即切到候选链的下一个渠道；客户端主动断开
  （手动结束推理）除外 —— 收件人已不在，换渠道重发没有意义，也不会把这种
  失败记到渠道头上。
- 429 按 `Retry-After` 冷却（上游给了 `Retry-After` 或 503 时以它为准，封顶
  8 秒）、401/403 冷却 30 秒；413/414/431（请求物理尺寸超限）第一个渠道就
  短路返回，不再空试。
- 一次转发中多个渠道报出相同的请求形状类 4xx（如都 400）时判定为请求级错误，
  不记渠道失败；渠道连续失败 3 次自动冷却 60 秒，成功一次即恢复。
- 换渠道前若**下一个候选仍打在同一台上游**（两条渠道共用同一个 `base_url`），
  先等 `RELAY_RETRY_SAME_UPSTREAM_DELAY`（默认 500ms，可设 0 关闭）——
  零间隔连打同一端点会被上游当成攻击，正是熔断的诱因；换到别的上游则不等，
  那不会给谁造成压力。

### 密钥 → 分组的强一致引用

密钥白名单里存**分组 ID**（与渠道的 `group_id` 同一待遇），保存时逐条校验
存在性 —— 分组改名不再打断任何密钥（曾经白名单存分组名，改名即全体 403，
界面上毫无征兆）；删除分组前检查密钥引用，仍被引用时 409 并点名密钥。
`/v1/models` 同时按密钥的**模型白名单**过滤 —— 客户端探测到的就是它能调的，
不会选一个就被 403 一次。老数据在启动时自动迁移（名字条目换算成 ID，幂等；
解析不了的保留原样并在日志里点名）。

## UI 还原说明

设计令牌与布局尺寸均通过 Chrome DevTools Protocol 抓取参考站（`llm.ohub.vip`）
登录后的真实 `getComputedStyle` 得到，而非目测。详见 `docs/ui-spec.md`。

核心规格：主色 `#c87864`、底色 `#f8f5ee`、卡片圆角 8px + `0 0 8px rgba(0,0,0,.1)`
阴影、侧边栏 224px、菜单项 32px 胶囊、栅格 gap 8px、衬线字体族。

布局是**应用外壳**：高度锁在视口里、只有内容区滚，品牌在侧栏；主题切换在看板页
工具栏右上角（2026-09-26 从侧栏底部迁来，灯泡形态不变）（参考站那条 64px 顶栏
装的是公告条与顶部导航，我们这边只剩品牌一个元素，留着就是一行空白，所以去掉
了 —— 参考站自身的尺寸仍记录在 `docs/ui-spec.md`）。

### 西文与数字字体

列表与表格里的西文与数字用的是参考站那份 Harding，随包分发在
`frontend/public/fonts/Harding-Regular.ttf`（中文不受影响，仍走系统雅黑 ——
Harding 不含中文字形，会一路回退到 `--font-family-base`）。两个容易踩的点记在
`frontend/src/styles/theme.css` 的字体段：字体 URL 必须写成根绝对路径（打包后
CSS 在 `/assets/` 下，相对路径取不到图），以及 antd 会给 `.ant-tag` 硬写
`font-family`、把继承来的列表字体顶掉，标签要单独覆盖一次。

**Harding 是商业授权字体**（参考站自托管的那份），本项目内部自用不受影响，
但对外分发前需要自行确认授权范围。

### 主题系统

2026-09-30 起界面有 **12 套内置配色**（浅色 4 / 深色 8），入口是**数据看板工具栏
右上角那枚按钮**（点开是一个主题网格弹层）与**系统设置页的「界面主题」面板**
（同一个选择器组件，两种尺寸）。原来的「深色质感档位」（`data-darkstyle`）已取消，
「深空纯黑」升格成一套普通主题；老键在 store 里做一次性迁移。

一条轴：`<html data-theme="<主题 id>">` 就是全部状态。`styles/theme.css` 里
`:root` 是 `light` 主题本尊，其余 11 套各是一个 `:root[data-theme='<id>']` 块，
**每块把随底色而变的令牌写全**（清单见 `frontend/scripts/lib/theme-css.mjs` 的
`REQUIRED_TOKENS`）—— 不许靠继承：漏一个令牌会静默继承浅色主题的值，
不报错、不失败，只是某个字在某套主题下发虚。哪些 id 存在由
`src/utils/themes.ts` 的注册表说了算。

配色来源（都是 MIT 许可，偏差逐条记在各自主题块的注释里）：

| 主题 id | 上游 | 主题 id | 上游 |
|---|---|---|---|
| `light` / `dark` | 参考站 llm.ohub.vip 实测 | `nord` | Nord |
| `oled` | 本站扩展（深灰的表面换纯黑） | `dracula` | Dracula |
| `latte` / `mocha` | Catppuccin Latte / Mocha | `tokyo-night` | Tokyo Night |
| `solarized-light` / `solarized-dark` | Solarized | `gruvbox-light` / `gruvbox-dark` | Gruvbox |

取色规则：表面三件套与主色逐字取上游官方色值；上游没有中间调边框色时允许
在两端色之间做 sRGB 插值（注明比例）；**上游值达不到 WCAG AA 就改**，
偏差必须记进注释 —— 这不是例外而是常态（Solarized Light 的官方正文只有 4.15:1、
Solarized Dark 的 base0 只有 4.32:1、Dracula 的正红在深色卡片上只有 3.6:1）。

三条守卫：

- `frontend/scripts/check-theme-contrast.mjs`：逐套实算 WCAG 比值
  （文字 ≥ 4.5:1、非文本 ≥ 3:1、边框 ≥ 1.15:1），并打 INFO 提示「族档位令牌
  在族内取值不一致」（可能是某个主题有意微调，也可能是改了一套忘了另一套）；
- `frontend/scripts/check-contracts.mjs` 的「主题契约」一节：注册表 / CSS 块 /
  `index.html` 预涂清单 / 预览色样 / 光标资源 / antd 令牌映射六处一致性，
  外加一条漂移守卫 —— **组件里不许出现 `[data-theme=…]` 选择器**
  （多主题下那种分支要么漏 10 套主题，要么每加一套都要回来改）；
- `scripts/verify-themes.mjs`：真浏览器逐套确认「声明有没有真的作用到页面上」
  （属性、页面底色、浏览器外壳色、刷新保持、弹层点击、控制台无异常）。

antd 的令牌（主色、语义色、背景、边框、文字灰阶）**不再在 `App.vue` 里手抄**：
`src/utils/antdTheme.ts` 维护「antd token → CSS 变量」的映射，运行时从计算样式读取。
12 套主题 × 13 个值手抄就是 156 份会各自漂移的副本，而漂移的表现是
「按钮颜色和卡片不是同一套主题」，不报错、不失败。

**新增一套主题的步骤**（缺一步 `npm run check` 就会报出来）：

1. `styles/theme.css`：复制一个同族主题块，改值，注释写清上游色名与偏差
   （比值先跑 `node scripts/check-theme-contrast.mjs --verbose` 看实测）；
2. 同一文件末尾的 `[data-swatch='<id>']` 加四项预览色（页面底/卡片/边框/主色实心块）；
3. `src/utils/themes.ts` 的 `THEMES` 加一项（id / 名字 / 上游 / 明暗 / 说明 / 外壳色）；
4. `frontend/index.html` 预涂脚本的 `THEME_IDS` 数组加一项（漏了会「刷新闪一下」）；
5. `cd frontend && npm run gen:cursors` 生成该主题的两个光标文件（提交入库）；
6. `npm run check && npm run test && npm run type-check`，必要时跑
   `node scripts/verify-themes.mjs <base> <新 id>` 单独确认这一套。

### 自定义光标

鼠标光标是本项目自己的品牌元素（参考站没有）：默认箭头与交互手型**每个主题
各有一套**，22 份图形在 `frontend/public/cursor-{arrow,hand}-<主题 id>.svg`。
两族的**形状**不同（浅色是长而窄的锐角轮廓、深色是短而阔的圆角轮廓），
颜色取自各主题「主色实心块」的那一对变量（填充 = 块底色、描边 = 压在上面的文字色）。

只有 `light` / `dark` 那四份是手写的（它们就是形状模板，正文里记着尺寸实测）；
其余 18 份由 `frontend/scripts/gen-theme-cursors.mjs --write`（`npm run gen:cursors`）
从对应族的模板重着色生成 —— 光标是当图片加载的、没有 CSS 级联，SVG 里写不了
`var()`，颜色只能是字面量，11 套主题手抄一份必然漂，而且漂了没有任何报错。
`--check` 模式与 `check-contracts.mjs` 会逐字节比对，所以「改了主题主色、
忘了重新生成光标」会当场变红。`oled` 复用 `dark` 的两个文件
（实心块那一对相同，注册表里用 `cursorSource` 显式声明）。

所有主题的热区坐标（箭头 `2 2`、手型 `9 2`）必须逐字相同，否则切主题时点击
落点会跳。输入框 I 型、拖拽、禁用、帮助四类保留系统光标。

要改光标形状或补齐 antd 升级后新增的交互组件，看
`frontend/src/styles/theme.css` 的「自定义光标」段 —— 那里记着清单的来源、
为什么必须 `!important`、为什么文件名带主题后缀（这几个资源没有
ETag/Last-Modified，缓存只能靠 URL 判断），以及 `npm run check` 里盯住哪几条。
形状的生成/核对工具在 `.shots/`（不入库）：`gen-cursor-dark.mjs` 生成深色路径、
`measure-cursors.mjs` 用浏览器 `getBBox()` 量实际尺寸、`contrast.mjs` 算对比度
—— 注释里引用的数字都出自这三个脚本。

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
> 用例（分组路由、流式截断、重试退避……）以**宿主 node 进程**跑 mock
> （≥18 即可，`bash scripts/ensure-mock-upstream.sh` 自动拉起）。

| 脚本 | 验证内容 |
|---|---|
| `test-regression.sh` | 全部管理接口 + 四种协议端点连通性 |
| `test-ratelimit.sh` | 密钥级 RPM 放行/拒绝、`Retry-After` |
| `test-key-group-refs.py` | 密钥分组白名单存 ID、不存在引用创建即 400、`/v1/models` 按模型白名单过滤、删分组检查密钥引用 |
| `test-payload.sh` | 报文留存三档模式与凭据脱敏 |
| `test-pricing-filter.sh` | 渠道列表的「未定价」计数 |
| `test-pricing-rules.sh` | 时段倍率优先于固定倍率、跨午夜窗口、最后一条生效 |
| `test-multiplier-precision.py` | 倍率四位小数端到端往返：设 0.1875 → API 读回 → **直接查 Postgres 列**确认 0.1875 未被截断；边界值 0.0001 / 1.2345 / 0.1234 / 0.5；并如实记录 5 位小数打 API 时库会按 `numeric(10,4)` 截断（前端两处已拦） |
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
形态与模型过滤）、删除语义、压缩协商、日志筛选与同源校验。

`scripts/verify-all.sh` 会按顺序跑完可离线执行的用例并汇总，最后打印
`ALL_PASS`；日常改完代码跑它一次就够。（`test-coldstart.sh` 要重跑
`install.sh` 重启服务，不在其中。）

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

### 光标审计：`scripts/audit-cursors.mjs`

需要先起一个带调试端口的 Chrome（用法见文件顶部）：把七个路由连同弹窗、
抽屉、下拉里每个元素的**计算光标**统计一遍，确认没有一处退回系统光标。
它存在的理由很具体 —— **CSS 的 cursor 不进截图**，少写一个选择器、antd 升级
换了 class 名、光标图片 404，表现都只是那一处悄悄变回系统箭头，不报错也不失败：

```bash
node scripts/audit-cursors.mjs http://127.0.0.1:5173   # 开发态
node scripts/audit-cursors.mjs                          # 默认打 127.0.0.1:8888
node scripts/audit-cursors.mjs http://127.0.0.1:8888 light        # 只审浅色
node scripts/audit-cursors.mjs http://127.0.0.1:8888 all          # 全部 12 套（慢）
node scripts/audit-cursors.mjs http://127.0.0.1:8888 solarized-dark   # 点名一套
```

默认跑两套默认主题（浅色 / 深色），`all` 跑全部 12 套。每个用例靠预置
`localStorage` 里的 `llm-relay-theme` 切主题，导航前注入，所以首屏那批元素也在
审计范围内。**页面上出现任何「不是本主题那一套」的文件名都判失败**，单独归为
「串主题」一类打印 —— 只检查「是不是自定义光标」的话，某个主题漏配光标令牌时
会继承 `:root`（浅色）那套，审计报告会一模一样全绿，而「绿得看不出区别」的检查
等于没检查。这条判断本身有反向验证：`.shots/verify-cross-theme-detection.mjs`
会手动把深色页面的光标变量改写成浅色那套，确认审计真的报出串主题的元素。

2026-09-30 起主题清单与期望文件名都从 `frontend/src/styles/theme.css` 解析
（`frontend/scripts/lib/theme-css.mjs`，与前端几个脚本共用一份解析器）：
硬编码 `['light','dark']` 的话，新增主题不会自动纳入审计，而「忘了加」
在这类脚本里是完全静默的。

### 主题端到端：`scripts/verify-themes.mjs`

静态脚本能比对声明，但「声明有没有真的作用到页面上」只有渲染后才知道：
选择器写错、属性拼错、打包没带上，表现都只是页面退回默认配色，不报错。

```bash
node scripts/verify-themes.mjs                    # 默认 127.0.0.1:8888，逐套跑 12 套
node scripts/verify-themes.mjs http://127.0.0.1:5173 nord   # 只验一套（调试用）
```

逐套断言四件事：`data-theme` 属性与 `localStorage` 一致、页面底色（计算样式）
等于主题块声明的值、`meta theme-color` 等于注册表里的外壳色、导航期间控制台无异常；
然后用**真实鼠标事件**点开看板那枚按钮，确认弹层里有全部主题卡（且分成浅色/深色
两组）、点一张卡真的换主题、弹层不自动关闭、刷新后仍是刚选的那套、快捷按钮
能切回浅色族。截图落到 `.shots/theme-<id>.png`（不入库，用来肉眼确认配色观感）。

### 表格列宽守卫：`check-table-widths.mjs` 与 `check-log-columns.mjs`

列宽 2026-09-23 起由脚本按当前页内容量出来（见 `docs/ui-spec.md` 第 17 条），
于是「列宽对不对」不再能从源码里一眼看出来：

- **check-table-widths.mjs**（静态，不需要浏览器）：每个表的 `scroll.x` 与各列
  声明宽度要对得上。运行时表（列宽来自 `colW`）它查的是「**每一列都绑了那个
  表达式**」—— 混进一个裸数字，那一列就不会随测量联动，表现为它永远停在声明
  值上。这个脚本此前只认数字字面量，请求日志表改成 `colW.xxx` 之后**整张表被
  静默跳过**，「没报错」于是被读成「没问题」，所以现在它会把「跳过」明确打出
  来并给出退出码；
- **check-log-columns.mjs**（需要调试端口的 Chrome）：静态检查只能确认「测量
  代码接对了」，而「量出来的宽度是否真的装得下内容」只有渲染后才知道 —— 这类
  问题不报错，内容被省略号截掉、页面一切正常。它按「3 视口 × 3 种每页条数」
  逐格断言三件事：锚点元素没有溢出（内容没被截断）、单元格自身没有横向溢出、
  横向滚到最右端时右侧固定列不遮挡内容。每页条数是从源码读出的 localStorage
  键名写进去的，并断言行数真的等于这一轮的 pageSize —— 否则整套矩阵会跑在同
  一个行数上，看着全绿其实只测了一种（第一版就是这么错的）。

```bash
node scripts/check-table-widths.mjs                                # 静态，秒出
node scripts/check-log-columns.mjs                                 # 默认 3 视口 × 20/50/100 条
node scripts/check-log-columns.mjs http://127.0.0.1:8888 1440 20   # 单组合
```

### 界面动效与细节的回归脚本（`.shots/`，不入库）

它们都需要一个带调试端口的 Chrome，用真实数据造场景，所以不进
`verify-all.sh`：

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
node .shots/verify-ws-heartbeat.mjs 8899 60 # 心跳帧本身：60 秒窗口内按 25 秒节拍到达、
                                            # 是空帧、不超上限（走服务的临时实例）
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
                                            # （「有 N 条新日志」挂起机制 2026-09-25 已移除）；
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

## 发布流程（本仓库维护者）

**发布 = 一条命令。** 服务器侧没有「部署」这个动作 —— 实例通过自动更新
自己拿新版本，人只负责把版本号命名出来：

```powershell
pwsh -File scripts/release.ps1 patch     # 或 minor / major / v0.1.5 / --dry-run
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

发布之后不必自己去点「检测更新」：服务端**每 5 分钟**自己检测一次
（`update.StartCheckLoop`，见 `internal/update` 里 `BackgroundCheckInterval`
的说明；进程启动一分钟后开始），结果写进与手动检测同一份缓存，
侧栏版本徽标跟着 5 分钟问一次自己的后端（命中缓存，不打 GitHub），
所以新版本发布后最多十几分钟徽标上就会出现黄点。观察这一环：

```bash
journalctl -u llm-relay -f | grep -E '后台版本检测|检测到新版本'   # 启动一行 + 发现新版本时一行
```

日志刻意只记「事件」（首次就绪 / 发现新版本 / 开始失败 / 恢复成功）：
国内直连不通 GitHub 要靠代理是常态，每 5 分钟一条失败 WARN 会把真正重要的
日志淹掉。

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
