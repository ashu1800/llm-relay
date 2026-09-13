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
 * 金额数字：小于 1 时保留 6 位（单次调用常常是几厘，固定 4 位会全变成 0.0000），
 * 否则 4 位。与看板原有的口径一致。
 */
export function amountText(v: string | number | null | undefined): string {
  const n = Number(v ?? 0)
  if (!Number.isFinite(n) || n === 0) return '0.0000'
  return n.toFixed(n < 1 ? 6 : 4)
}

/** 带符号的金额，如 ¥12.3400、$0.000938 */
export function moneyText(v: string | number | null | undefined, currency?: string | null): string {
  return symbolOf(currency) + amountText(v)
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

/** 把 costs 渲染成一行「¥12.34 / $5.67」；没有金额时返回空串 */
export function costsText(costs?: Record<string, string> | null, sep = ' / '): string {
  const map = costs ?? {}
  return currencyKeys(map)
    .map((c) => moneyText(map[c], c))
    .join(sep)
}