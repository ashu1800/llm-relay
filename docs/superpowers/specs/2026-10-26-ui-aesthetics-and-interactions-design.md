# UI 质感与交互体验全方位提升设计方案（2026-10-26）

## 一、背景与设计宗旨

LLM Relay 是面向开发者的专业大模型 API 网关，具备 24 小时高频吞吐、多协议转换、毫秒级时延监控和 12 套完整主题的特性。当前管理界面已经具备极高的工程严整性（严格遵循 WCAG AA 对比度、纯函数判据与单测保证、断网与降级守卫），但在视觉层次、材质厚度、数据排版韵律与深度交互爽感上，仍偏向于朴素的平面卡片表单。

本方案旨在**不破坏既有设计系统与对比度红线**的前提下，将界面质感全面升级为具备**“精工仪器感、物理触觉反馈、工程级数据美学与极速开发者人机工效”**的现代化控制台。

---

## 二、目标、非目标与边界约束

### 1. 目标（Goals）
- **视觉厚度与材质**：增加微高光倒角、极细毛玻璃遮罩、点阵工程底纹，摆脱纯平面卡片感。
- **动态生命力与微触觉**：让新日志顺滑展开、按键获得物理按压微缩回弹、状态灯与示波器拥有模拟余辉光晕。
- **信息密度与排版美学**：长模型名分层解析排版（继承分组色）、带宽双轨（Rx/Tx）重叠面积波形图、耗时瀑布自适应标尺化。
- **极致操作爽感**：全局 `Ctrl+K` 命令面板、渠道拖拽排序物理悬浮、详情页一键生成终端 cURL 复现命令（支持智能骨架降级）、主题视口涟漪扩散过渡。

### 2. 非目标（Non-Goals）
- 不引入重型第三方 UI 库、图表库（如 ECharts/D3）或厚重动效库。
- 不增加额外网络开销，所有数据可视化与动画均基于本地状态与纯 CSS / 内联轻量 SVG 实现。
- 不破坏既有的 12 套主题系统与色彩令牌体系，所有高光与投影均基于 CSS 变量自适应。
- 不打破“模型胶囊与密钥胶囊颜色同源于分组”的核心视觉规范。

### 3. 约束（Constraints）
- 必须 100% 通过 `npm run check`（主题对比度红线、文字色令牌契约、无字面 `vw` 宽度、无非法未注册组件）。
- 必须尊重 `prefers-reduced-motion` 无障碍配置，动画与缩放可无感知回退。
- 必须保持纯函数抽离原则（复杂数据格式化与解析必须有配套 `.spec.ts` 单元测试）。

---

## 三、系统设计与工程落地方案

### 维度一：视觉材质与空间层次

#### 1. 面板顶沿“微倒角高光”（Subtle Bevel Highlight）
- **受影响组件**：`PanelCard.vue`、`PageToolbar.vue`、`StatCard.vue`。
- **实现机制**：
  在 `PanelCard` 与 `PageToolbar` 顶部引入微光修饰层：
  ```css
  box-shadow: inset 0 1px 0 0 color-mix(in oklab, var(--color-fg) 80%, white 20%),
              var(--color-fg-shadow);
  ```
  深色主题下自适应为高亮反光沿（`rgba(255, 255, 255, 0.08)`），营造类似精加工铝合金或高强度玻璃仪表面板边缘的光学倒角，令卡片具备实体分量感。

#### 2. 模态弹窗与详情抽屉的超细毛玻璃沉降（Glassmorphism & Depth）
- **受影响组件**：`MainLayout.vue`（全局覆盖 antd 遮罩样式）。
- **实现机制**：
  ```css
  .ant-modal-mask,
  .ant-drawer-mask {
    backdrop-filter: blur(6px) saturate(120%);
    -webkit-backdrop-filter: blur(6px) saturate(120%);
    background: color-mix(in oklab, var(--color-bg) 60%, transparent);
  }
  ```
  底层实时滚动的监控数据在弹窗或抽屉升起时呈现光学虚化，既能保留背景的“机器正在运转”的现场感，又明确了当前焦点的绝对层级。

#### 3. 极简工程微点阵底纹（Subtle Dot Matrix Texture）
- **受影响组件**：`MainLayout.vue`（`.main-layout-body` 或 `.console-content`）。
- **实现机制**：
  使用纯 CSS 在页面底层添加 24px×24px 极淡径向渐变点阵：
  ```css
  background-image: radial-gradient(
    color-mix(in oklab, var(--color-text) 4%, transparent) 1px,
    transparent 1px
  );
  background-size: 24px 24px;
  ```
  浅色主题呈现极度低调的绘图纸微点阵，深色主题（OLED/Nord/Tokyo Night 等）则呈现雷达网格感，彻底告别单调平铺。

---

### 维度二：动效生命力与触觉反馈

#### 1. 实时日志插入的平滑展开（Log Row Smooth Insertion）
- **受影响组件**：`RequestLogPanel.vue`。
- **实现机制**：
  新日志插入时，配合已有的 `is-new` 类名，通过 CSS 动画为新行增加关键帧入场过渡：
  ```css
  @keyframes row-fade-in {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }
  ```
  配合现有的横向扫光动效，消除原本新行直接“硬切插入”导致的突兀跳动。

#### 2. 按钮与操作项的微压缩物理回弹（Tactile Micro-press）
- **受影响组件**：全局公共样式 `theme.css` / 组件局部按钮。
- **实现机制**：
  为 `.table-icon-btn`、`.console-menu-item`、`button.ant-btn` 等交互元素增加微动效：
  ```css
  .tactile-btn:active,
  .table-icon-btn:active {
    transform: scale(0.95);
    transition: transform 0.06s ease;
  }
  ```
  提供类似于微动开关的坚脆按压确认感。

#### 3. 状态指示灯与示波器的余辉呼吸（Phosphor Glow & Organic Heartbeat）
- **受影响组件**：`PulseBar.vue`、`ChannelLifeDot.vue`。
- **实现机制**：
  - `PulseBar` 扫光时增加两端微羽化并带柔和微光发散（`filter: drop-shadow(0 0 4px var(--pulse-color))`）。
  - 通道健康状态圆点在正常状态下保持静止，而在异常（熔断/故障）状态下增加 2 秒一次的低频柔和呼吸晕影（`box-shadow: 0 0 6px var(--color-red)`），让异常状态在视野边缘即被察觉。

#### 4. 数字滚轮补间视觉张力（Tabular Numerals & Flash Transition）
- **受影响组件**：`AnimatedNumber.vue`、`StatCard.vue`。
- **实现机制**：
  确保全部大号数字强制应用 `font-variant-numeric: tabular-nums`，在数据滚动插值时消除字符等宽抖动；配合 `StatCard` 现有的 `flash` 机制，在数字变更的一瞬间注入微弱的色温上升与淡化。

---

### 维度三：数据排版与信息美学

#### 1. 长模型名称的语义字阶排版（Model Name Typographic Hierarchy）
- **受影响组件**：`RequestLogPanel.vue`、`GroupTag.vue`。
- **设计裁决（保留分组色彩体系）**：
  - 严格保持模型列与所属分组同色系的视觉约定。
  - 不使用固定灰度文字令牌，而是通过纯函数 `parseModelName(name: string)` 将模型名分解为前缀（vendor）、核心（core）、后缀（suffix）：
    - `prefix`（如 `anthropic/`、`deepseek-ai/`、`gpt-`）：`opacity: 0.65; font-weight: 400; font-size: 0.9em;`；
    - `core`（如 `claude-3-7-sonnet`、`v3`）：`opacity: 1; font-weight: 600;`；
    - `suffix`（如 `-20250219`、`-preview`）：`opacity: 0.75; font-weight: 400; font-size: 0.85em;`。
  - 文字颜色统一 `color: inherit` 继承 `GroupTag` 派生的色调，既获得清晰的字阶节奏，又 100% 符合 WCAG 对比度检查。
  - 配套单测：`modelName.spec.ts`。

#### 2. 双轨收发带宽仪表（Rx / Tx Dual-wave Area SVG）
- **受影响组件**：`BandwidthMeter.vue`、`components/bandwidth.ts`。
- **设计逻辑**：
  保持零第三方依赖，纯内联 SVG（宽 96px，高 22px）。
  将原单一的 `total` 曲线升级为上行（Tx）与下行（Rx）双层重叠透明面积图：
  - 下行（Rx，接收）：青色/主题线 + 15% 渐变填充；
  - 上行（Tx，发送）：琥珀/橙色线 + 15% 渐变填充；
  - 两条波形共用同一个统一时间刻度与统一峰值基准 `curveCeiling(points)`，保证物理比例绝对真实。
  - 配套单测：`bandwidth.spec.ts` 中补充双轨路径计算断言。

#### 3. 耗时瀑布的自适应时间标尺（Adaptive Latency Waterfall Ruler）
- **受影响组件**：`RequestLogPanel.vue`（详情抽屉耗时明细区）。
- **设计裁决（相对时间轴自适应）**：
  - 保留现有的 3 行结构（TCP 握手、首字等待、正文传输）。
  - 在进度条下方叠加一条 0%、25%、50%、75%、100% 的等分刻度线，刻度值通过 `fmtMs(total_ms * ratio)` 动态生成。
  - 无论是 150ms 的超快请求还是 40s 的长流式请求，刻度永远自适应展开，绝不越界堆叠。

---

### 维度四：深度交互与人机工程体验

#### 1. 全局极速命令面板（Command Palette / `Ctrl+K`）
- **受影响组件**：新建 `components/CommandPalette.vue`，挂载在 `MainLayout.vue`。
- **设计裁决**：
  - **快捷键**：全局监听 `Ctrl+K`（Windows/Linux）与 `Cmd+K`（macOS）。
  - **功能划分**：
    1. **页面直达**：快速跳转看板、密钥、渠道、分组、代理、系统设置；
    2. **主题秒切**：实时搜索 12 套主题名称，Enter 立即切换；
    3. **排障深链**：一键直达“仅失败请求”或“追踪链路”；
    4. **模型直达**：搜索模型名称，选择后跳转至 `/console/channels?q=<model>`。
  - **渠道联动**：`ChannelsView.vue` 增加对 `route.query.q` 的响应，自动填充搜索框并过滤渠道列表，完美消解“同一模型分布在多个渠道”的歧义。
  - **数据缓存**：组件打开时按需拉取渠道与模型列表并作短时缓存，轻量且无污染。
  - **键盘完全可达**：`↑` / `↓` 切换、`Enter` 执行、`Esc` 退出。
  - 配套单测：`commandPalette.spec.ts`。

#### 2. 渠道拖拽排序的物理悬浮感（Physics-based Drag & Drop）
- **受影响组件**：`ChannelsView.vue`。
- **实现机制**：
  针对 SortableJS 的 `.sortable-drag`、`.sortable-ghost`、`.sortable-chosen` 进行微观样式升级：
  - 被抓起的行：增加 1.01 倍微放大，深阴影浮空感，背景半透明透底；
  - 放置目标占位区（Ghost）：呈现微弱主色发光虚线边框；
  - 松手时具有自然的弹性复位过渡。

#### 3. 日志排障“一键生成终端 cURL”（Instant Repro Snippet）
- **受影响组件**：`RequestLogPanel.vue`。
- **设计裁决（详情已载入 + 智能降级骨架）**：
  - cURL 按钮置于详情抽屉顶部，直接读取已完成异步请求的 `detail.value`，为纯同步操作。
  - 纯函数 `buildCurlCommand(detail: RequestLogDetail): string`：
    - 根据入站协议（OpenAI `/v1/chat/completions`、Anthropic `/v1/messages`、Gemini `/v1beta/models/...`）拼装目标端点；
    - 请求头携带 `Authorization: Bearer <YOUR_API_KEY>`（或对应协议的 Key 头）；
    - **报文降级**：
      - 若报文已留存：格式化填入原始 JSON；
      - 若报文未留存（`errors` 模式下的成功日志）：自动填入该协议的最小有效测试 Prompt 骨架，并在 cURL 首行追加注释 `# 该请求未留存报文，已填充模板请求体`。
    - 复制完成后轻提示 `message.success('已复制 cURL 复现命令')`。
  - 配套单测：`curl.spec.ts`。

#### 4. 主题切换的视口涟漪扩散（View Transitions Ripple）
- **受影响组件**：`ThemePicker.vue`、`stores/theme.ts`。
- **实现机制**：
  - 在支持 `document.startViewTransition` 的现代浏览器中，以点击色块的屏幕坐标 `(clientX, clientY)` 为中心，计算屏幕对角线最大半径，执行全屏圆形扩散遮罩动画（`clip-path: circle(0px at x y) -> circle(maxRadius at x y)`）。
  - 不支持时无感平滑回退至普通切换。

---

## 四、验证方案

1. **类型与契约守卫**：
   - 执行 `npm run type-check` 保证 TypeScript 零报错；
   - 执行 `npm run check` 确保 12 套主题对比度全部通过 WCAG AA（≥4.5:1），所有 `--text-*` 与 `--color-*` 契约合规。
2. **单元测试矩阵**：
   - 为新抽离的纯逻辑编写 Vitest 测试（`modelName.spec.ts`、`commandPalette.spec.ts`、`curl.spec.ts`、`bandwidth.spec.ts`）；
   - 执行 `npm run test` 确保全部测试用例绿灯通过。
3. **构建与嵌入验证**：
   - 执行 `npm run build` 确保预压缩产物与打包体积正常。
