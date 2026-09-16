# UI 规格书（实测自 llm.ohub.vip）

> 数据来源：Chrome DevTools Protocol 抓取登录后真实 DOM 的 `getComputedStyle`。
> 抓取脚本：`scripts/capture-layout.mjs`，原始数据：`docs/layout-dashboard.json`。
> 视口：1249 x 1277，页面标题「数据看板 - OHub」。

## 一、全局设计令牌（CSS 变量，实测原值）

| 令牌 | 值 |
|---|---|
| `--color-primary` | `#c87864` |
| `--color-secondary` / `--color-highlight` | `#afbeaf` |
| `--color-bg` | `#f8f5ee` |
| `--color-fg` | `#ffffff` |
| `--color-fg-shadow` | `0 0 .5rem 0 rgba(0,0,0,.1)` |
| `--color-text` | `#303030` |
| `--color-text-secondary` | `rgba(48,48,48,.65)` |
| `--color-border` | `#d9d9d9` |
| `--color-red` | `#ea4343` |
| `--color-orange` | `#f59e0b` |
| `--color-green` | `#10b37d` |
| `--color-blue` | `#06b6d4` |
| `--color-purple` | `#8b5cf5` |
| `--color-gray` | `#6b7280` |
| `--color-icon` | `#06b6d4` |
| `--color-icon-hover-bg` | `rgba(6,182,212,.12)` |
| `--scrollbar-thumb` | `#dcdcdc` |
| `--scrollbar-track` | `transparent` |
| `--scrollbar-width` | `.5rem` |
| `--ease-expo` | `cubic-bezier(.2, 1, .3, 1)` |
| `--font-family-base` | `Harding, STSong, SimSun, Songti SC, 宋体, Hiragino Sans GB, STHeiti, WenQuanYi Micro Hei, -apple-system, ..., serif`（衬线） |

## 二、布局骨架（实测尺寸，px）

```
main-layout            flex, bg #f8f5ee, 全屏
├── site-nav           header  高 64, padding 0 25, gap 16, bg #f8f5ee
│   ├── logo / brand
│   ├── nav-announcement   高 32, 圆角 999, 边框 1px #d9d9d9,
│   │                      padding 0 8, 文字 rgba(48,48,48,.65)
│   └── nav-actions    nav, flex, gap 8, 高 32（控制台 / 模型 / 文档 / 头像）
└── main-layout-body   position: relative
    └── console-layout flex
        ├── console-sidebar  aside 宽 224, padding 8, bg #f8f5ee, flex
        │   └── console-menu-list  nav, flex, gap 4
        │       └── console-menu-item  button 208x32, 圆角 999,
        │                              padding 0 8, gap 8, font-size 14.4
        └── console-content  section flex:1
            └── *-container  padding 8
```

**关键数字**：顶栏 64、侧边栏 224、菜单项高 32 圆角 999、内容区 padding 8。

## 三、卡片 / 面板（全站统一）

所有面板（`dashboard-toolbar`、`panel-block`、`summary-card`、`chart-card`、`heatmap-panel`、`detail-panel`）共用同一套规格：

| 属性 | 值 |
|---|---|
| background | `#ffffff` |
| border | `1px solid #d9d9d9` |
| border-radius | `8px` |
| box-shadow | `rgba(0,0,0,0.1) 0px 0px 8px 0px` |
| padding | `8px` |
| margin-bottom | `8px` |
| font-size / line-height | `14px` / `16.1px` |

## 四、栅格

全部使用 CSS Grid，**gap 恒为 8px**：

| 区块 | 规格 |
|---|---|
| `overview-row` | grid，左 `summary-grid` (501) + 右 `heatmap-panel` (501) |
| `summary-grid` | grid 2x2，每格 `summary-card` 246x68，flex，gap 16 |
| `chart-grid` | grid，`chart-card` 501x332（画布区 483x288） |
| `heatmap-body` | grid，gap `4px 8px` |

## 五、排版

| 元素 | 规格 |
|---|---|
| `section-title` (h2) | font-size **16px**，font-weight **700**，color **#c87864**，margin-bottom 8 |
| 面板正文 | font-size 14px，line-height 16.1px |
| body | font-size 16px，color #303030 |
| 菜单项 | font-size 14.4px，line-height 16.56px |

## 六、表格

| 元素 | 规格 |
|---|---|
| `ant-table-cell` (th) | 高 **39px**，背景 **#f8f5ee**（表头用页面底色而非白底） |
| 数据行 | 高 **39px** |
| 表格容器 | 圆角 8px 8px 0 0 |
| 分页 | `ant-pagination-mini`，高 24 |

## 七、控件

| 控件 | 规格 |
|---|---|
| `ant-picker` / `range-picker` | 高 **24px**（small 尺寸），圆角 6px，白底 |
| `ant-btn-primary` | 高 32，圆角 6px，bg **#c87864**，白字，padding `4px 15px` |
| 选中态胶囊 | bg `rgba(200,120,100,0.2)`，color `#c87864` |

## 八、数据看板信息架构（实测）

- **工具栏**：时间范围（今天 / 近3天 / 近7天 / 近30天）+ 刷新
- **概览四卡**：请求数量、消费金额、Token 量、成功率
- **请求热力图**：按日期 x 小时分布
- **图表区**：消耗分布、消耗趋势、模型调用分析、模型消耗占比
- **模型用量明细表**：模型 / 请求数 / 成功·失败 / 成功率 / Token（输入/输出/缓存）/ 消费金额

## 九、与本站的差异约定

1. 「消费金额」列**保留**，展示为**预估金额**（按录入单价折算），不做任何扣减。
2. 新增「模型定价」页：单价全部手工录入，支持固定倍率与**按时段倍率**
   （如工作日 9:00-12:00、14:00-18:00 双倍），时段按服务器本地时区判断。
   列表会提示「渠道白名单里有、但还没定价」的模型 —— 这类模型的费用恒为 0。
3. 剔除登录、用户、订单、工单、兑换、礼品、邮件、公告等模块。
4. 保留「请求日志」的筛选、详情抽屉、导出（接口与脚本仍在，界面按钮 2026-09-16 去掉）与计费过程还原。
5. 新增站点专属鼠标光标（参考站用的是系统光标）：默认箭头与交互手型换成
   `frontend/public/cursor-arrow.svg` 与 `cursor-hand.svg`（主色填充 + 白描边），
   输入框 I 型、拖拽、禁用、帮助四类刻意保留系统光标 —— 它们是功能性提示，
   自绘反而更难认。选择器清单来自对本站七个路由的 computed style 审计
   （不是照抄参考站），完整理由与踩坑记录见 `frontend/src/styles/theme.css`
   的「自定义光标」段。
6. 请求日志的「首字耗时 / 总共耗时」两列合并为一列「任务耗时」：单元格里竖排
   两行（左侧一条绿色竖条 + `首字` / `总耗时`），数值仍按耗时分级着色
   （≤5s 绿、5-15s 橙、>15s 红，判据与悬停说明见 `components/RequestLogPanel.vue` 的
   `latencyClass` / `durTitle`）。合并的理由是这两个数回答的是同一个问题
   「这次调用等了多久」，分列时扫列表要在两处之间来回看，而两列合计 200px
   换来的信息只有两个数；合并后列宽 120px。
7. 请求日志的列顺序按「读一行日志的顺序」重排：
   请求时间 → 模型 → 渠道 → 词元 → 任务耗时 → 费用 → 状态 → 密钥 → 操作。
   其中：
   - 「分组」列**移除** —— 同一行的模型、密钥两个胶囊本来就是按所属分组着色
     （`tagColorOf`），分组名仍在详情抽屉里，按分组看整批请求用工具栏的筛选；
   - 「渠道」列加上渠道图标（与渠道页、渠道下拉同一个 `ChannelIcon`），
     排障时要一眼认出「这条走的是哪条渠道」；
   - 「状态」「密钥」移到末尾：它们回答的是「结果如何」，夹在模型与渠道之间
     会打断前面那条「什么时候 → 哪个模型 → 哪条渠道 → 花了多少」的阅读线。
   列宽同时按实测收窄（第 10 条把「缓存命中」并入词元之后合计 1036，
   取「表头文字宽」与「内容最宽」的较大者 + 16px 内边距）：整张表在 1440 视口下
   装进容器，否则横向滚动时固定在右侧的「操作」列会把最后一列（现在是密钥）
   切掉半个字。
8. 列表（表格 / `.ant-list` / `.ant-tag`）里的**西文与数字**用参考站那份
   **Harding**，随包分发在 `frontend/public/fonts/Harding-Regular.ttf`；
   中文**不用**参考站的做法（它的中文是宋体），仍走 `--font-family-base`
   （Windows 上是微软雅黑）—— Harding 与后面几个系统衬线族都不含中文字形，
   中文会自动落到链尾，不需要额外拆元素。
   回退链 `'Harding', Cambria, 'Book Antiqua', 'Palatino Linotype', Georgia,
   'Times New Roman', var(--font-family-base)`：在参考站页面上同页量过字宽
   （14px，「日期 / 词元 / 耗时 / 模型名 / 百分比 / 金额」六串的平均偏差），
   Georgia 2.30 < Cambria 3.05 < Book Antiqua 3.12 < Palatino 5.47 < Times 7.45，
   但 Georgia 是旧式数字（3/4/5/7/9 下沉，一列数字会上下跳），
   Book Antiqua 又只有装了 Office 的机器才有（本机没有，此前整条链实际落在
   Georgia 上），所以按「字宽接近 + lining 数字 + 系统自带概率」排成上面这个顺序。
   这是商业授权字体：内部自用无碍，对外分发本项目前需自行确认授权。
9. 请求日志**并入数据看板**（2026-09-16），「请求日志」不再是一个页面：
   - 删掉热力图与四张图表（消耗趋势 / 消耗分布 / 模型调用分析 / 模型消耗占比）；
   - 四张概览卡从 2x2 改成**一排四张**（占满整行）；
   - 概览卡下面是请求日志列表（面板外壳 + 表格 + 分页；面板标题栏与导出按钮
     2026-09-16 已去掉，表体因此多出一整行），列与列宽见第 7 条与第 10 条（`scroll.x=1036`）；
   - 筛选栏只有一套，四个条件（时间范围 / 分组 / 渠道 / 模型）同时作用于卡片与列表。
     时间范围改为看板原有的四档（今天 / 近3天 / 近7天 / 近30天），
     去掉了日志页原来的「近 1 小时」与「不限时间」；
   - 窗口由后端 `resolveRange()` 单点计算（`/stats/*` 与 `/logs` 共用），
     不再由前端按本地零点算 `since` —— 后者与看板的「今天」是两套算法；
   - 请求日志的四个筛选持久化键从 `logs-*` 改为 `dashboard-*`（合成一页即一套视角）；
   - 旧地址 `/console/logs` 保留为**带 query 的跳转**（`?trace_id=` / `?status_class=`
     这类深链原来就指着它）；
   - 后端的 `/stats/timeseries|models|channels|heatmap` 四个接口**保留**
     （有测试、有文档的 API 面），只是前端不再调用；`echarts` 依赖与
     `EChart.vue` / `utils/chartTheme.ts` 一并删除；
   - 代价：概览卡与筛选栏也占这 100vh 里的位置，表格可视行数从 17 行
     降到 14 行（1440x900 实测，第 15 行露出大部分）。`TABLE_BODY_Y` 因此从
     `calc(100vh - 201px)` 改为 `calc(100vh - 286px)`（去掉面板标题栏后又收窄
     40px，可视行数从 13 行回到 14 行；取 286 而不是实测的 284：那 2px 会让
     `.console-content` 多出隐形内滚），算式与逐项实测值见
     `frontend/src/components/RequestLogPanel.vue`。
10. 「词元」列改成**上下两排**、「缓存命中」列**移除**（2026-09-16）：
   - 表头 `词元（输入/输出/缓存）` → `词元`（括号里的说明挪到单元格悬停提示，
     顺带把这一列从 170px 收窄到 150 —— 原来撑宽列的是表头那串 154px 的括号）；
   - 单元格竖排两排、字号 12px：上排 `↑ 输入` `↓ 输出`，下排 `▣ 缓存词元` `命中率`；
     四个数放一格，是因为它们回答的是同一个问题「这次调用用了多少词元」，
     而原来两列合计 260px，扫一行要在两处之间来回看。
     箭头朝向按语义定（向上=送到模型、向下=模型返回），**与参考站截图里
     「↓ 在前、↑ 在后」的字形顺序刻意相反** —— 那个顺序读不出哪边是哪边；
   - 命中率的口径没变（`cached / (未命中 + 命中 + 缓存写入)`，与看板一致），
     只是从独立一列挪进词元格；`cached_tokens = 0` 的旧行因此显示成 `▣ 0 -`，
     与合并前两列的表现逐字一致，没有新增规则；
   - 箭头与缓存盒是**内联 SVG**（`stroke="currentColor"`，12px，线宽 1.5）：
     `↓` `↑` 这两个字符既不是西文也不是中文，会落到字体链尾的系统符号字体，
     线宽与大小随机器变（同第 8 条的字体问题）；图标颜色挂在「图标 + 数值」
     这一组上，图标与数字必然同色；
   - 配色**沿用项目现有语义色**（输入陶土 / 输出紫 / 缓存绿），
     没有照抄参考站那份（绿输入 / 紫输出 / 蓝缓存）：
     改色相等于改掉 theme.css 里已定档的语义（输入是花钱的那部分、缓存是省下来的）；
   - 行高不变（实测 40.25 / 41.25 两档）：两排 15px 用 -4px 上下负边距压成 22px，
     与「任务耗时」列同一手法 —— 行高一变，固定表体的可视行数与滚动位置都要跟着变；
   - 词元与任务耗时这两格的**居中**要用「块级 + `justify-content: center`」
     （渠道那格同理，一并改了）：全局规则是 `.ant-table-tbody > tr > td
     { text-align: center }`，而块级的 grid / flex 会撑满单元格、内容从左边起排
     （实测右空隙比左空隙大 42px / 27px / 14px）；但也不能改成 inline-block /
     inline-grid —— inline 级盒子要参与行盒，单元格那 14px 的行高会跟它叠起来，
     行高从 41px 涨到 49px。居中之后实测三格左右空隙相等，行高仍是 41.25；
   - 列的声明总宽 1146 → 1036，1440 视口下容器 1182，剩余宽度由 antd 按比例
     分给各列（约 +13%），不手工调整与本次需求无关的列宽。

11. **新日志扫光**（2026-09-16）：实时推送把一行新日志插到列表最上面时，
    沿这一行的**下分割线**从左向右扫过一道彩虹亮带，约 2 秒后从右端消失。
    - 触发面刻意很窄：**只有**实时推送带来的新行会闪。刷新页面、改筛选、
      翻页、点「刷新」都不闪 —— 那些操作整屏都在换，闪一排彩虹没有信息量；
      而「凭空多出来一行」不提示的话用户根本不会注意到列表变了。
      两条实时路径都覆盖：没筛选时新行直接前插（`onLive`），
      有筛选时走安静重取（`load({silent:true})`，用重取前后的 id 差集认出新行）；
    - 亮带是表格外面那一层（`.log-table`）里的绝对定位元素，位置由 JS 量出来
      （`top/left/width` 都取自那一行的 `getBoundingClientRect`），所以它天生
      等于「整行宽 + 贴行底边」；那一层 `position: relative` + `overflow: hidden`，
      跑出容器的部分被裁掉，不会撑出滚动条，也不影响固定列的吸附
      （sticky 单元格最近的滚动祖先是 `.ant-table-body`，比它更近）。
      **为什么不画在 `tr` 的 `::after` 上**（第一版就是这么写的，1440 视口下看着
      一切正常）：`tr` 里一旦出现非单元格子元素（`::after` 就算一个），Chrome 在
      `table-layout: fixed` 下就不再把它多出来的宽度分给各列 —— 整张表从
      「铺满容器」塌回声明宽度，而表头是另一张表、照旧铺满，于是**右侧空出一条**，
      一直持续到动画结束。实测 1600 视口：每格 200/200/167/193/155/116/82/129/93 →
      155/155/130/…、行右边界 1575 → 1277（空 298px）。隔离验证过：去掉伪元素、
      只留 `position: relative`，列宽全程正常；只把 `position` 改成 `static`、
      留着伪元素，照样塌 —— 起因是那个伪元素。挂到单元格上也不行：固定列是
      `position: sticky`，它里面的绝对定位伪元素会落到行外（实测 `left:0` 的盒子
      跑成 x 1575→2909；改成右对齐又落在 148→1482，而行是 241→1575）。
      所以 `is-new` 这个类**不带任何样式**，只是「这一行正在做入场动画」的标记，
      `frontend/scripts/check-contracts.mjs` 会盯着它不被挂回 `tr`；
    - 动的是 **`background-position-x`**（`-50% → 150%`，带子占行宽 30%），
      不是元素的 `transform`。换成一个 translateX 的小元素跑出行外会撑大容器的
      可滚动溢出区；背景位移的盒子始终在行内（实测动画全程 `scrollWidth`
      === `clientWidth`，动画前后一致）；
    - `z-index: 3` 是为了盖住左右两个固定列（它们是 `position: sticky` +
      不透明底、`z-index: 2`）；
    - 缓动用 **`linear`**，不用项目里的 `--ease-expo`：那个曲线 0.3 秒就走完全程，
      剩下的时间停在右端不动，观感是「闪一下」。这里的时长本身就是需求；
    - 亮带高 3px（分割线是单元格的 1px 下边框，且算在行高内）：盖住它再往上
      压 2px，看起来才是一道光，而不是一条细线；
    - **彩虹是有意的用色例外**：项目其余部分严格走陶土主色系，这一处按站主要求
      用彩虹。色相取 antd 色板，两端 alpha 为 0（进出都是渐隐，不是硬边色块）；
    - 行的标记是「正在做入场动画的行 id 集合」，2.4 秒后清掉（2s 动画 + 余量）；
      不给行数据加 UI 字段 —— 那些字段会跟着进详情、进导出；
      首屏列表落地之前一律不标记（`loadedOnce`）：实测刷新页面的 4.5 秒窗口里，
      恰好入库的一条真实请求会因「rows 还是空的、无从比对」被当成新增，
      表现为「一刷新就闪」，与这条约定不符；
    - 「减弱动态效果」不用在这里重复处理：`theme.css` 末尾那条全局规则
      （`prefers-reduced-motion: reduce` 下把所有 `animation-duration` 压到
      `0.01ms`）会让它变成瞬时，终态在 150%（本来就不可见），功能不受影响。

