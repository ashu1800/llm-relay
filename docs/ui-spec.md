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

1. 「消费金额」列**保留**，展示为**预估金额**（按官方单价折算），不做任何扣减。
2. 新增「模型定价」页，展示单价来源（官方 / LiteLLM / 手动）、上次同步时间。
3. 剔除登录、用户、订单、工单、兑换、礼品、邮件、公告等模块。
4. 保留「请求日志」的筛选、详情抽屉、导出与计费过程还原。
