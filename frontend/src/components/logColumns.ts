// 日志表列宽系统的**纯逻辑**（2026-09-27 从 RequestLogPanel.vue 抽出）：
// 列键、分摊权重、以及「余量怎么摊 / 内容宽怎么夹」两个纯函数。
//
// 抽出来的动机：这套算法有三条必须同时成立的不变量（见 allocateWidths），
// 原先埋在组件里测不到 —— 列宽一坏要靠肉眼对截图才发现。独立成模块后
// logColumns.spec.ts 把不变量钉进测试；组件里的 COL_BOUNDS / CONTENT_MEASURE
//（契约检查锚定的两块）留在原地，distribute / remeasure 只剩 DOM 读写。
//
// 约定：本模块不碰 DOM、不碰 Vue 响应式 —— 那是组件那一层的事。

/** 日志表的 10 列（顺序即渲染顺序） */
export const COL_KEYS = [
  'time', 'model', 'channel', 'tokens', 'elapsed', 'cost', 'speed', 'status', 'key', 'action'
] as const
export type ColKey = (typeof COL_KEYS)[number]

/** 列名（2026-10-10 列显隐功能用）：勾选面板里的文案与表头必须同源，
 *  否则改了表头忘了改面板，用户会看到两个名字指同一列。 */
export const COL_LABELS: Record<ColKey, string> = {
  time: '请求时间',
  model: '模型',
  channel: '渠道',
  tokens: '词元',
  elapsed: '任务耗时',
  cost: '费用',
  speed: '速度',
  status: '状态',
  key: '密钥',
  action: '操作'
}

/** 不允许隐藏的列：请求时间是行的身份，操作是打开详情的唯一入口。
 *  两者都能关掉的话，用户会得到一张「看不出这行是什么、也点不开」的表。 */
export const COL_LOCKED: readonly ColKey[] = ['time', 'action']

/** 单元格左右内边距（antd 小表格 8+8）与右侧呼吸余量（给省略号与边框）。
 *  内容宽要加这一组才是「列需要的宽」；下限的加法必须与测量同一套
 * （COL_BOUNDS 里 channel 一栏的注释有完整的踩坑记录）。 */
export const CELL_PAD = 16
export const CELL_BREATH = 6

/**
 * 宽屏余量分摊的**权重**（2026-09-27 感官升级）。
 *
 * 等额分摊（每列 +leftover/10）对短列是浪费：status / action 两列是
 * COL_BOUNDS 里写死的固定宽（[64,64]），内容是一枚胶囊、一个图标按钮，
 * 拉宽它们只会让一枚小胶囊周围空出一圈，而最需要宽度的模型/渠道/密钥
 * 三列（唯一会 ellipsis 截断的三列）只多拿到同样的一份。于是改为加权：
 *   · 固定窄列（status / action）份额为 0 —— 永远按声明宽渲染；
 *   · 三个会截断的长文本列（model / channel / key）权重 ×2；
 *   · 其余数值列权重 1。
 */
export const DISTRIBUTE_WEIGHT: Record<ColKey, number> = {
  time: 1,
  model: 2,
  channel: 2,
  tokens: 1,
  elapsed: 1,
  cost: 1,
  speed: 1,
  status: 0,
  key: 2,
  action: 0
}

/** 全表的 Σw（11 = 3×2 + 5×1 + 2×0）。
 *
 *  注意它不再直接参与分摊：allocateWidths 现在按**传入的列子集**现算 Σw
 *  （列显隐会关掉几列，分母必须跟着变）。这条常量留下是为了让
 *  logColumns.spec.ts 能钉住权重表本身 —— 第一版注释把合计写成 13
 *  （多算了两个 ×0 列），正是那条断言第一次运行就抓出来的。 */
export const WEIGHT_SUM = COL_KEYS.reduce((s, k) => s + DISTRIBUTE_WEIGHT[k], 0)

/**
 * 把容器比「声明宽合计」多出来的余量按权重摊进各列（纯函数版 distribute）。
 *
 * 三条不变量（logColumns.spec.ts 逐条钉住，坏任何一条都有肉眼可见的回归：
 * 假横向滚动 / 集中空洞 / 内容截断）：
 *   1. Σ份额 = leftover ⇒ 各列宽之和 ≈ 容器可用宽（浮点容差内），
 *      scroll.x 取合计恰好不与容器打架，表格不出现假滚动条；
 *   2. 实际宽 = 声明宽 + 份额 ≥ 声明宽（只增不减）⇒ 内容不会被截断；
 *   3. 份额 ∝ 权重 ⇒ 余量去长文本列，固定窄列（权重 0）原样渲染。
 *
 * 容器比声明合计还窄（leftover ≤ 0）时不摊：那是横向滚动区，
 * 列按声明宽渲染，滚动边界与声明一致。
 *
 * `keys`（2026-10-10 加，默认全部列）是「本次参与分摊的列」——
 * 列显隐功能关掉几列后，被关掉的列不渲染也不该分余量，
 * 三条不变量在**可见列这个子集**上仍然成立（Σ份额 = leftover）。
 * 返回的仍是完整记录（隐藏列拿 0 份额），这样 colW 的读写不用到处判空。
 */
export function allocateWidths(
  avail: number,
  baseW: Readonly<Record<ColKey, number>>,
  keys: readonly ColKey[] = COL_KEYS
): Record<ColKey, number> {
  const declared = keys.reduce((sum, k) => sum + baseW[k], 0)
  const weightSum = keys.reduce((s, k) => s + DISTRIBUTE_WEIGHT[k], 0)
  const leftover = avail - declared
  // 单位份额 = leftover / Σw，各列拿「单位份额 × 自己的权重」，
  // Σ(份额) = leftover × Σw / Σw = leftover，合计仍恰好等于容器可用宽
  const unit = leftover > 0 && weightSum > 0 ? leftover / weightSum : 0
  const out = {} as Record<ColKey, number>
  for (const key of COL_KEYS) {
    const share = keys.includes(key) ? unit * DISTRIBUTE_WEIGHT[key] : 0
    out[key] = baseW[key] + share
  }
  return out
}

/**
 * 量到的内容宽夹进该列的 [下限, 上限]（纯函数版 remeasure 的取宽一步）。
 * 上限是软上限：超出交给 ellipsis + 悬停 title（见组件里 COL_BOUNDS 的说明）。
 */
export function clampColumnWidth(measured: number, pad: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, measured + pad + CELL_PAD + CELL_BREATH))
}
