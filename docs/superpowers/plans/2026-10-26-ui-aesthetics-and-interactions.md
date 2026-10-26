# UI 质感与交互体验全方位提升实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 LLM Relay 网关前端全面落地精工仪器感视觉材质、物理微触觉反馈、数据排版韵律与深度极客交互体验（包括全局 `Ctrl+K` 命令面板、长模型名分层排版、双轨带宽面积波形图、自适应瀑布标尺、一键生成终端 cURL 与主题涟漪扩散）。

**Architecture:** 保持全仓零重型外部依赖的原则，数据计算与转换严格收敛为纯函数并配齐 Vitest 单元测试；样式层深度利用既有 12 套主题 CSS 变量与现代浏览器原生 API（`backdrop-filter`, `oklab color-mix`, `startViewTransition`），视觉与无障碍严格遵循既有 WCAG AA 对比度与契约守卫。

**Tech Stack:** Vue 3 (Composition API / `<script setup>`), TypeScript, Ant Design Vue 4, Vitest, CSS / SVG.

**Spec:** `docs/superpowers/specs/2026-10-26-ui-aesthetics-and-interactions-design.md`

## Global Constraints

- **文字颜色规范**：文字只使用 `--text-*` 令牌；`--color-*` 仅用于填充、边框或图标；模型胶囊内部继承 `GroupTag` 派生的色系。
- **弹窗与浮层宽度**：弹窗宽度强制使用 `modalWidth(N)` 计算，模板中禁止出现字面量 `vw`。
- **字号阶梯**：字号必须走 `var(--font-size-*)` 阶梯，禁止出现硬编码 px 字号。
- **对比度红线**：所有文字必须通过 `node scripts/check-theme-contrast.mjs` 的 WCAG AA（≥4.5:1）校验，覆盖全部 12 套主题。
- **判据纯函数**：复杂排版、数据转换、刻度计算逻辑必须抽离到 `*.ts` 纯函数并配有完整的 `*.spec.ts` 单元测试。
- **动效无障碍**：所有动效与缩放过渡需保证在 `prefers-reduced-motion` 下无感平滑回退。

## Review Focus

1. **未留存报文的 cURL 生成**：在 `errors` 报文留存模式下，成功请求的 `inbound_body` 为 `null`，cURL 必须降级为对应协议的合法最小 Prompt 骨架，并带有清晰注释，避免生成语法断裂命令。
2. **长模型名极端边界解析**：面对无前缀、无版本后缀、纯数字、仅含路径斜杠等畸形或自建模型名时，`parseModelName` 纯函数必须安全兜底，不截断、不漏字。
3. **极值耗时的标尺计算**：面对 0ms 瞬间本地错误或超 60s 的大模型长流式响应，`latencyTicks` 刻度比例计算必须保持在 `0 ~ 100%` 闭区间内，不溢出、不重叠。
4. **双轨带宽极值波形计算**：单方向流量为零（仅 Rx 或仅 Tx）或全零流量时，双层波形与填充多边形必须贴底且坐标合法，不产生 `NaN` 或异常绘制。
5. **命令面板键盘循环导航与特殊字符**：输入特殊字符（如正则元字符 `[`、`*`、`+`）时搜索不崩溃，键盘上下键在列表首尾时必须正确循环或夹持边界，按 Esc 正确退回焦点。

---

### Task 1: 视觉材质与空间层次基础（倒角微高光、毛玻璃遮罩、工程点阵底纹）

**Files:**
- Modify: `frontend/src/styles/theme.css`
- Modify: `frontend/src/components/PanelCard.vue`
- Modify: `frontend/src/components/PageToolbar.vue`
- Modify: `frontend/src/components/MainLayout.vue`

**Interfaces:**
- Produces: 全局 `--panel-bevel-highlight` 变量与 `.ant-modal-mask`, `.ant-drawer-mask` 毛玻璃滤镜；`.console-content` 工程微点阵底纹。

- [ ] **Step 1: 在 `theme.css` 中增加面板倒角微高光与毛玻璃遮罩规则**

在 `frontend/src/styles/theme.css` 的 `:root` 与全局样式区添加：
- `--panel-bevel-highlight: inset 0 1px 0 0 color-mix(in oklab, var(--color-fg) 80%, white 20%);`
- 针对 `.ant-modal-mask` 与 `.ant-drawer-mask` 增加 `backdrop-filter: blur(6px) saturate(120%); -webkit-backdrop-filter: blur(6px) saturate(120%); background: color-mix(in oklab, var(--color-bg) 60%, transparent);`

- [ ] **Step 2: 在 `PanelCard.vue` 与 `PageToolbar.vue` 中应用倒角微高光**

在两者的根卡片样式上叠加 `box-shadow: var(--panel-bevel-highlight), var(--color-fg-shadow);`。

- [ ] **Step 3: 在 `MainLayout.vue` 的内容容器添加极简工程点阵底纹**

在 `.console-content` 上添加 24px×24px 极淡径向渐变点阵：
```css
background-image: radial-gradient(
  color-mix(in oklab, var(--color-text) 3.5%, transparent) 1px,
  transparent 1px
);
background-size: 24px 24px;
```

- [ ] **Step 4: 运行契约与对比度验证**

Run: `cd frontend && npm run check`
Expected: 全部 PASS

- [ ] **Step 5: 提交代码**

```bash
git add frontend/src/styles/theme.css frontend/src/components/PanelCard.vue frontend/src/components/PageToolbar.vue frontend/src/components/MainLayout.vue
git commit -m "style: add panel bevel highlight, glassmorphism mask and dot-matrix background"
```

---

### Task 2: 交互触觉回弹与状态指示余辉光晕

**Files:**
- Modify: `frontend/src/styles/theme.css`
- Modify: `frontend/src/components/PulseBar.vue`
- Modify: `frontend/src/components/ChannelLifeDot.vue`
- Modify: `frontend/src/components/RequestLogPanel.vue`

**Interfaces:**
- Produces: 交互元素 `:active` 触觉回弹；`PulseBar` 微光羽化与扩散；异常状态指示灯呼吸光晕；新日志行入场淡入。

- [ ] **Step 1: 在 `theme.css` 注入全局轻微触觉回弹动效**

对 `.table-icon-btn`, `.console-menu-item`, `.ant-btn` 增加 active 微缩：
```css
@media (prefers-reduced-motion: no-preference) {
  .table-icon-btn:active,
  .console-menu-item:active,
  .ant-btn:active {
    transform: scale(0.95);
    transition: transform 0.06s ease;
  }
}
```

- [ ] **Step 2: 增强 `PulseBar.vue` 与 `ChannelLifeDot.vue` 的状态余辉**

- `PulseBar.vue`：在 `.pulse-spark` 上添加 `filter: drop-shadow(0 0 3px var(--pulse-color));`，让扫过的光带具有模拟荧光屏的温润光晕。
- `ChannelLifeDot.vue`：在状态为失败/异常时，增加柔和呼吸晕影（`box-shadow: 0 0 6px var(--color-red)` 配合关键帧呼吸）。

- [ ] **Step 3: 优化 `RequestLogPanel.vue` 中新日志插入动效**

在表格行 `tr.is-new` 配合 `@keyframes row-fade-in` 增加轻微透明度过渡：
```css
@keyframes row-fade-in {
  from { opacity: 0; transform: translateY(-3px); }
  to { opacity: 1; transform: translateY(0); }
}
```

- [ ] **Step 4: 运行验证**

Run: `cd frontend && npm run check && npm run test`
Expected: PASS

- [ ] **Step 5: 提交代码**

```bash
git add frontend/src/styles/theme.css frontend/src/components/PulseBar.vue frontend/src/components/ChannelLifeDot.vue frontend/src/components/RequestLogPanel.vue
git commit -m "style: add tactile micro-press feedback and state glow effects"
```

---

### Task 3: 长模型名语义字阶排版（Model Name Typographic Hierarchy）

**Files:**
- Create: `frontend/src/utils/modelName.ts`
- Create: `frontend/src/utils/modelName.spec.ts`
- Modify: `frontend/src/components/GroupTag.vue`

**Interfaces:**
- Produces: `parseModelName(name: string): { prefix?: string; core: string; suffix?: string }`
- Consumes: 在 `GroupTag.vue` 内部，当作为模型展示或传递分段时，渲染分层 `<span>`。

- [ ] **Step 1: 编写 `parseModelName` 失败测试**

在 `frontend/src/utils/modelName.spec.ts` 编写覆盖各种格式的用例：
```ts
import { describe, it, expect } from 'vitest'
import { parseModelName } from './modelName'

describe('parseModelName', () => {
  it('解析带厂商前缀与日期后缀的标准模型', () => {
    expect(parseModelName('anthropic/claude-3-7-sonnet-20250219')).toEqual({
      prefix: 'anthropic/',
      core: 'claude-3-7-sonnet',
      suffix: '-20250219'
    })
  })
  it('解析只有核心名的模型', () => {
    expect(parseModelName('gpt-4o')).toEqual({
      prefix: undefined,
      core: 'gpt-4o',
      suffix: undefined
    })
  })
  it('解析带前缀无后缀的模型', () => {
    expect(parseModelName('deepseek-ai/DeepSeek-V3')).toEqual({
      prefix: 'deepseek-ai/',
      core: 'DeepSeek-V3',
      suffix: undefined
    })
  })
  it('处理空串与异常输入', () => {
    expect(parseModelName('')).toEqual({ core: '' })
  })
})
```

- [ ] **Step 2: 验证测试失败**

Run: `cd frontend && npx vitest run src/utils/modelName.spec.ts`
Expected: FAIL（模块未定义）

- [ ] **Step 3: 实现 `parseModelName`**

在 `frontend/src/utils/modelName.ts` 中实现：
识别 `/` 前缀，识别末尾日期（如 `-20250219` 或 `-latest` 或 `-preview`），其余部分为 `core`。

- [ ] **Step 4: 验证测试通过**

Run: `cd frontend && npx vitest run src/utils/modelName.spec.ts`
Expected: PASS

- [ ] **Step 5: 在 `GroupTag.vue` 中支持模型名分层渲染**

在 `GroupTag.vue` 中支持 `model` 属性或插槽结构，若提供模型名，则按 `prefix` (`opacity: 0.65; font-size: 0.9em;`), `core` (`font-weight: 600;`), `suffix` (`opacity: 0.75; font-size: 0.85em;`) 进行渲染，保持继承外层 `currentColor`。

- [ ] **Step 6: 契约与单测校验并提交**

Run: `cd frontend && npm run check && npm run test`
Expected: PASS

```bash
git add frontend/src/utils/modelName.ts frontend/src/utils/modelName.spec.ts frontend/src/components/GroupTag.vue
git commit -m "feat: add parseModelName and hierarchical typography in GroupTag"
```

---

### Task 4: 双轨收发带宽仪表（Rx / Tx Dual-wave Area SVG）

**Files:**
- Modify: `frontend/src/components/bandwidth.ts`
- Modify: `frontend/src/components/bandwidth.spec.ts`
- Modify: `frontend/src/components/BandwidthMeter.vue`

**Interfaces:**
- Produces: `dualBandwidthPoints(points, w, h, ceiling): { rxLine: string; rxArea: string; txLine: string; txArea: string }`

- [ ] **Step 1: 编写双轨波形计算的失败测试**

在 `frontend/src/components/bandwidth.spec.ts` 中添加用例：
验证在给定一组 `BandwidthPoint[]` 时，能够分别算出 `rxLine`、`rxArea`、`txLine`、`txArea` 的有效 SVG 路径字符串，且在全 0 时面积贴底、极值时不越界。

- [ ] **Step 2: 验证测试失败**

Run: `cd frontend && npx vitest run src/components/bandwidth.spec.ts`
Expected: FAIL

- [ ] **Step 3: 在 `bandwidth.ts` 中实现双轨计算逻辑**

抽取纯函数 `dualBandwidthPoints`，分别提取 `rx_bytes_per_sec` 与 `tx_bytes_per_sec`，利用现有的 `curveCeiling` 作为统一归一化高度基准。

- [ ] **Step 4: 验证测试通过**

Run: `cd frontend && npx vitest run src/components/bandwidth.spec.ts`
Expected: PASS

- [ ] **Step 5: 在 `BandwidthMeter.vue` 中升级内联 SVG 结构**

在 SVG 中渲染两个 `<polygon>`（Rx 青色半透明填充，Tx 琥珀色半透明填充）与两条 `<polyline>`，鼠标悬停展开层中增强 Rx / Tx 对比指示。

- [ ] **Step 6: 契约与测试校验并提交**

Run: `cd frontend && npm run check && npm run test`
Expected: PASS

```bash
git add frontend/src/components/bandwidth.ts frontend/src/components/bandwidth.spec.ts frontend/src/components/BandwidthMeter.vue
git commit -m "feat: implement dual-wave Rx/Tx area graph in BandwidthMeter"
```

---

### Task 5: 耗时瀑布自适应时间标尺（Adaptive Latency Waterfall Ruler）

**Files:**
- Modify: `frontend/src/components/latency.ts`
- Modify: `frontend/src/components/latency.spec.ts`
- Modify: `frontend/src/components/RequestLogPanel.vue`

**Interfaces:**
- Produces: `latencyTicks(totalMs: number): { ratio: number; label: string }[]`

- [ ] **Step 1: 编写刻度生成纯函数的失败测试**

在 `frontend/src/components/latency.spec.ts` 添加用例：
```ts
it('latencyTicks 生成 0%, 25%, 50%, 75%, 100% 相对等分刻度', () => {
  const ticks = latencyTicks(200)
  expect(ticks).toHaveLength(5)
  expect(ticks[0].label).toBe('0ms')
  expect(ticks[4].label).toBe('200ms')
})
it('处理 totalMs <= 0 的安全降级', () => {
  const ticks = latencyTicks(0)
  expect(ticks).toHaveLength(2)
})
```

- [ ] **Step 2: 验证测试失败**

Run: `cd frontend && npx vitest run src/components/latency.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现 `latencyTicks` 纯函数**

在 `frontend/src/components/latency.ts` 中实现：使用 `fmtMs` 生成等分比例的时间文本，并附加 `ratio: 0 / 0.25 / 0.5 / 0.75 / 1`。

- [ ] **Step 4: 验证测试通过**

Run: `cd frontend && npx vitest run src/components/latency.spec.ts`
Expected: PASS

- [ ] **Step 5: 在 `RequestLogPanel.vue` 详情抽屉中渲染标尺刻度线**

在时序条区域下方叠加一条微细刻度线，通过 `ticks` 绝对定位刻度点和数值，使用 `--color-text-tertiary` 柔和渲染。

- [ ] **Step 6: 验证与提交**

Run: `cd frontend && npm run check && npm run test`
Expected: PASS

```bash
git add frontend/src/components/latency.ts frontend/src/components/latency.spec.ts frontend/src/components/RequestLogPanel.vue
git commit -m "feat: add adaptive latency waterfall ruler ticks"
```

---

### Task 6: 日志排障一键生成终端 cURL（Instant Repro cURL）

**Files:**
- Create: `frontend/src/utils/curl.ts`
- Create: `frontend/src/utils/curl.spec.ts`
- Modify: `frontend/src/components/RequestLogPanel.vue`

**Interfaces:**
- Produces: `buildCurlCommand(options: CurlBuildOptions): string`
- Consumes: `RequestLogPanel.vue` 详情抽屉

- [ ] **Step 1: 编写 cURL 生成函数的失败测试**

在 `frontend/src/utils/curl.spec.ts` 中针对 OpenAI、Anthropic、Gemini 以及有报文和无报文（降级骨架）编写测试：
```ts
describe('buildCurlCommand', () => {
  it('生成标准 OpenAI Chat cURL 命令', () => {
    const cmd = buildCurlCommand({
      origin: 'http://127.0.0.1:8888',
      protocol: 'openai-chat',
      model: 'gpt-4o',
      body: '{"messages":[{"role":"user","content":"hi"}]}'
    })
    expect(cmd).toContain('http://127.0.0.1:8888/v1/chat/completions')
    expect(cmd).toContain('Authorization: Bearer <YOUR_API_KEY>')
  })
  it('当 body 为空时自动注入测试 Prompt 骨架与注释', () => {
    const cmd = buildCurlCommand({
      origin: 'http://127.0.0.1:8888',
      protocol: 'openai-chat',
      model: 'gpt-4o',
      body: ''
    })
    expect(cmd).toContain('# 注：该请求未留存原始报文')
    expect(cmd).toContain('"messages"')
  })
})
```

- [ ] **Step 2: 验证测试失败**

Run: `cd frontend && npx vitest run src/utils/curl.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现 `buildCurlCommand`**

在 `frontend/src/utils/curl.ts` 中根据各协议端点规则拼装多行带有 `\` 换行的终端 cURL 格式。

- [ ] **Step 4: 验证测试通过**

Run: `cd frontend && npx vitest run src/utils/curl.spec.ts`
Expected: PASS

- [ ] **Step 5: 在 `RequestLogPanel.vue` 详情抽屉顶部挂载「复制 cURL」按钮**

在详情抽屉标题栏右侧工具区增加一个按钮，点击后读取 `detail` 并调用 `writeClipboard(buildCurlCommand(...))`，成功后提示 `message.success('已复制终端 cURL 复现命令')`。

- [ ] **Step 6: 验证与提交**

Run: `cd frontend && npm run check && npm run test`
Expected: PASS

```bash
git add frontend/src/utils/curl.ts frontend/src/utils/curl.spec.ts frontend/src/components/RequestLogPanel.vue
git commit -m "feat: add instant terminal curl repro generator in log details"
```

---

### Task 7: 渠道拖拽排序的物理悬浮与平滑吸附

**Files:**
- Modify: `frontend/src/views/ChannelsView.vue`

**Interfaces:**
- Produces: SortableJS 拖拽状态样式微动效（`.sortable-drag`, `.sortable-ghost`, `.sortable-chosen`）

- [ ] **Step 1: 在 `ChannelsView.vue` 补充 Sortable 拖拽激活样式**

在 `ChannelsView.vue` scoped 样式中添加：
```css
:deep(.sortable-drag) {
  opacity: 0.95;
  transform: scale(1.01) rotate(0.4deg);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.15);
  background: var(--color-fg) !important;
  cursor: grabbing !important;
}
:deep(.sortable-ghost) {
  opacity: 0.35;
  border: 1px dashed var(--color-primary) !important;
}
```

- [ ] **Step 2: 验证样式与构建**

Run: `cd frontend && npm run check`
Expected: PASS

- [ ] **Step 3: 提交代码**

```bash
git add frontend/src/views/ChannelsView.vue
git commit -m "style: enhance drag-and-drop physics feedback in ChannelsView"
```

---

### Task 8: 全局极速命令面板（Command Palette / `Ctrl+K`）与渠道联动

**Files:**
- Create: `frontend/src/components/commandPalette.ts`
- Create: `frontend/src/components/commandPalette.spec.ts`
- Create: `frontend/src/components/CommandPalette.vue`
- Modify: `frontend/src/components/MainLayout.vue`
- Modify: `frontend/src/views/ChannelsView.vue`

**Interfaces:**
- Produces: `filterCommands(query: string, items: CommandItem[]): CommandItem[]`
- Produces: `<CommandPalette />` 组件，全局监听快捷键并支持键盘可达操作
- Consumes: `ChannelsView.vue` 响应 `route.query.q` 自动填充过滤

- [ ] **Step 1: 编写命令过滤纯函数的失败测试**

在 `frontend/src/components/commandPalette.spec.ts` 测试菜单直达、主题模糊匹配、模型匹配过滤：
```ts
describe('filterCommands', () => {
  it('匹配页面导航项', () => {
    const res = filterCommands('看板', mockItems)
    expect(res[0].id).toBe('nav-dashboard')
  })
  it('匹配主题名称', () => {
    const res = filterCommands('nord', mockItems)
    expect(res.some(i => i.id === 'theme-nord')).toBe(true)
  })
  it('特殊字符不崩溃', () => {
    expect(() => filterCommands('[*.+', mockItems)).not.toThrow()
  })
})
```

- [ ] **Step 2: 验证测试失败**

Run: `cd frontend && npx vitest run src/components/commandPalette.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现 `commandPalette.ts`**

实现安全的大小写不敏感包含与模糊搜索匹配，导出结构化命令列表生成器与过滤纯函数。

- [ ] **Step 4: 验证测试通过**

Run: `cd frontend && npx vitest run src/components/commandPalette.spec.ts`
Expected: PASS

- [ ] **Step 5: 创建 `CommandPalette.vue` 浮层组件**

实现弹窗展示、搜索输入框、分组展示（导航/主题/操作/模型）、`↑`/`↓` 键盘索引、`Enter` 执行动作并关闭、`Esc` 退出、打开时自动聚焦、关闭时还焦。

- [ ] **Step 6: 在 `MainLayout.vue` 挂载 `CommandPalette` 并监听全局快捷键**

在 `MainLayout.vue` 监听 `keydown`：当按下 `(e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k'` 时阻止默认事件并打开面板。并在侧边栏或顶栏增加小标签（如 `Ctrl K` 徽标）。

- [ ] **Step 7: 在 `ChannelsView.vue` 中支持 `route.query.q` 联动**

在 `ChannelsView.vue` 挂载时，若 `route.query.q` 存在，自动将其赋给表格的搜索框 `searchKeyword.value = String(route.query.q)`，使得从命令面板跳来的模型能直接在渠道列表中过滤展示。

- [ ] **Step 8: 契约与单元测试全绿验证**

Run: `cd frontend && npm run check && npm run test`
Expected: PASS

- [ ] **Step 9: 提交代码**

```bash
git add frontend/src/components/commandPalette.ts frontend/src/components/commandPalette.spec.ts frontend/src/components/CommandPalette.vue frontend/src/components/MainLayout.vue frontend/src/views/ChannelsView.vue
git commit -m "feat: implement global Ctrl+K command palette with channel filter integration"
```

---

### Task 9: 主题切换视口涟漪扩散（View Transitions Ripple）

**Files:**
- Modify: `frontend/src/components/ThemePicker.vue`
- Modify: `frontend/src/stores/theme.ts`

**Interfaces:**
- Produces: 点击色块时以其物理坐标为中心执行平滑的圆形全屏展开剪裁过渡。

- [ ] **Step 1: 封装带坐标扩散的视口过渡逻辑**

在 `ThemePicker.vue` 中，当触发主题选择时：
检查 `document.startViewTransition` 是否存在：
若存在，记录点击事件的 `e.clientX` 与 `e.clientY`，计算至视口四个角的最远距离作为最大半径；
调用 `document.startViewTransition(() => themeStore.setTheme(id))`，在 `ready` 状态后执行圆形 clip-path 展开动画；
若不支持，直接执行 `themeStore.setTheme(id)` 降级。

- [ ] **Step 2: 验证全套前端验证脚本**

Run: `cd frontend && npm run verify`
Expected: `type-check`, `check`, `test`, `build` 全部 100% 成功通过！

- [ ] **Step 3: 提交代码**

```bash
git add frontend/src/components/ThemePicker.vue frontend/src/stores/theme.ts
git commit -m "feat: implement view transition ripple effect on theme change"
```

---

### Task 10: 最终系统集成与验收测试

- [ ] **Step 1: 运行后端全部测试确保无附带影响**
Run: `$env:GOPROXY="https://goproxy.cn,direct"; go test ./...` in `backend`
Expected: 全部 PASS

- [ ] **Step 2: 运行前端全套 verify**
Run: `npm run verify` in `frontend`
Expected: 全部 PASS，类型检查 0 错误，12 套主题对比度全部合格

- [ ] **Step 3: 最终 Git 状态与产物审查**
检查 `git status` 与 `git log`，确保代码风格与提交信息规整。
