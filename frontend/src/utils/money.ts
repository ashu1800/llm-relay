// 金额展示：把「一串数字 + 它的币种」渲染出来。
//
// 这里**不做任何汇率换算**，这是刻意的：上游按什么币种开账单，库里就存那个
// 币种的数字（见 model.Channel.Currency）。人民币和美元不能相加，所以「合计」
// 只在同一币种内成立 —— 看板按币种分别统计，日志按各自渠道的币种显示。
//
// 全站只保留这一份符号表：金额符号散落在各页面里写过一次（'$' 硬编码在
// 看板、日志、计价器三处），改口径时漏掉一处就会出现「同一个数两种符号」。
export const CURRENCY_SYMBOL: Record<string, string> = {
  CNY: '¥',
  USD: '$'
}

/** 币种符号。认不出的币种直接显示代码本身，不猜也不退回 $ */
export function symbolOf(currency?: string | null): string {
  const code = String(currency ?? '').trim().toUpperCase()
  if (!code) return ''
  return CURRENCY_SYMBOL[code] ?? code + ' '
}

/**
 * 金额数字的小数位：**按量级分档，全站只有这一份规则**（2026-09-24 UI 审评收口）。
 *
 * 分档与理由：
 *   ≥ 0.01 → 4 位。单次调用常常只花几厘，固定 2 位会把一大片显示成 0.00；
 *            而 4 位足以区分它们（0.0375 / 0.0412），第 5 位起就是噪声 ——
 *            价格表本身也只精确到「每百万词元」的厘位。
 *   < 0.01 → 6 位。这一档再砍就真的会看见 0.00 了（库里最便宜的一单是 0.000938）。
 *   0      → '0.00'，不是 '0.0000'：四个零看起来像「算过但是零」。
 *
 * 以前这里是 `n < 1 ? 6 : 4`，于是 0.037510 会拖着两个没有信息量的尾零；
 * 更要紧的是**别处各自写了一套**：日志列表 toFixed(6)、日志详情 toFixed(8)、
 * 分组预算 toFixed(2)、日报 toFixed(4)。同一个数字在四处是四个样子，
 * 用户核对「这一单到底多少钱」时对不上。现在它们都走这里。
 */
export function amountText(v: string | number | null | undefined): string {
  const n = Number(v ?? 0)
  if (!Number.isFinite(n) || n === 0) return '0.00'
  return n.toFixed(Math.abs(n) < 0.01 ? 6 : 4)
}

/** 带符号的金额，如 ¥12.3400、$0.000938 */
export function moneyText(v: string | number | null | undefined, currency?: string | null): string {
  return symbolOf(currency) + amountText(v)
}

/**
 * 单笔费用的列表/详情写法：没有金额时返回 `-` 而不是 `¥0.00`。
 *
 * 为什么与 moneyText 分开：看板那张卡回答的是「这段时间一共花了多少」，
 * 0 就是 0，写 ¥0.00 是对的；而日志里的一行回答的是「这一笔花了多少」，
 * 0 的真实含义是「没计价 / 免费渠道 / 失败请求」—— 写成 ¥0.00 会被读成
 * 「这次很便宜」，那是错的。规则同一份，空值语义各自表达。
 *
 * decimals 可选：传了就固定这个位数，不再按量级分档。
 *
 * 为什么需要它：请求日志的费用列（站主 2026-10-27 要求）要**默认四位**。
 * 按量级分档在那里是反效果 —— 同一列里 0.0375 显示四位、0.0038 显示六位，
 * 数字对不齐，竖着扫一列的时候很别扭。固定四位后整列是同一个量纲。
 *
 * 代价要说清楚：小于 0.00005 的费用会显示成 0.0000。这一档本来就看不见
 * （最便宜的一单约 0.000938，四位下是 0.0009），真要精确值可以去日志详情。
 * 其余调用方（看板合计、详情）不传这个参数，继续用分档规则。
 *
 * decimals 是**展示精度**，不是取整开关：传 0 会得到 `¥12`（把分以下全丢掉），
 * 那不是任何地方的诉求。目前唯一的调用方是日志列表的费用列，传 4。
 */
export function costText(
  v: string | number | null | undefined,
  currency?: string | null,
  decimals?: number
): string {
  const n = Number(v ?? 0)
  if (!Number.isFinite(n) || n <= 0) return '-'
  if (decimals !== undefined) {
    return symbolOf(currency) + n.toFixed(decimals)
  }
  return symbolOf(currency) + amountText(n)
}

// 多币种的展示顺序：按这张表定序，而不是按 Object.keys 的顺序 ——
// 后者取决于后端 map 的遍历顺序，会让卡片和图例在两次刷新之间自己换位置。
const ORDER = ['CNY', 'USD']

/** costs 字段（币种 -> 金额字符串）里出现过的币种，按固定顺序排列 */
export function currencyKeys(costs?: Record<string, string> | null): string[] {
  const keys = Object.keys(costs ?? {})
  const rank = (c: string) => {
    const i = ORDER.indexOf(c)
    return i < 0 ? ORDER.length : i
  }
  return keys.sort((a, b) => (rank(a) !== rank(b) ? rank(a) - rank(b) : a.localeCompare(b)))
}

/** 主币种：金额卡片的大数字用它，其余币种并排列在下面 */
export function primaryCurrency(costs?: Record<string, string> | null): string {
  return currencyKeys(costs)[0] ?? ''
}

/** 把 costs 渲染成一行「¥12.34 / $5.67」；没有金额时返回空串。
 *  目前无人调用（原来只有看板的热力图提示用），保留是因为它是 costs 的
 *  「一次性展示」入口：卡片只用主币种 + 提示里列其余币种（见 DashboardView），
 *  两处合起来才是完整语义，删掉这个函数并不能减少多少东西。 */
export function costsText(costs?: Record<string, string> | null, sep = ' / '): string {
  const map = costs ?? {}
  return currencyKeys(map)
    .map((c) => moneyText(map[c], c))
    .join(sep)
}

export type CostParts = {
  head: string
  tail: string
}

/**
 * 将费用文本拆解为两段：主体部分（符号+整数+前两位小数）与尾部细微小数（后两位或更多）。
 * 供阶梯排印（Hierarchical Typography）降低视觉信噪比。
 * 样例：
 *   '$12.3456' -> { head: '$12.34', tail: '56' }
 *   '¥0.0038'  -> { head: '¥0.00', tail: '38' }
 *   '¥0.00'    -> { head: '¥0.00', tail: '' }
 *   '-'        -> { head: '-', tail: '' }
 */
export function splitCostParts(text: string): CostParts {
  if (!text || text === '-') return { head: text || '-', tail: '' }
  const dotIndex = text.indexOf('.')
  if (dotIndex === -1) return { head: text, tail: '' }
  const splitIdx = dotIndex + 3
  if (text.length <= splitIdx) {
    return { head: text, tail: '' }
  }
  return {
    head: text.slice(0, splitIdx),
    tail: text.slice(splitIdx)
  }
}
