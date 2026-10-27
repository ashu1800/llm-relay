// 模型定价页的判据（纯函数，规则 10：判定逻辑不写在模板里）。
//
// 这一页要回答的问题只有三类，全部可以脱离 DOM 测：
//   1. 这行的价格填没填、填得对不对（保存前的拦截）；
//   2. 界面上怎么把它说清楚（摘要、缺失提示）；
//   3. 哪些模型还没定价（漏配的可视化 —— 漏配的后果是账单记 0 元，
//      账面上完全看不出异常，只能靠界面点名）。

/** 一个模型的价格（与后端 model.ModelPrice 对应）。金额走字符串：numeric(18,8)。 */
export interface ModelPrice {
  model_name: string
  usd_input_per_1m: string
  usd_output_per_1m: string
  usd_cache_read_per_1m: string
  usd_cache_write_per_1m: string
  rmb_input_per_1m: string
  rmb_output_per_1m: string
  rmb_cache_read_per_1m: string
  rmb_cache_write_per_1m: string
}

/** 表单里的编辑态：八个单价都是字符串（空串 = 未填）。 */
export type PriceForm = Pick<
  ModelPrice,
  | 'usd_input_per_1m'
  | 'usd_output_per_1m'
  | 'usd_cache_read_per_1m'
  | 'usd_cache_write_per_1m'
  | 'rmb_input_per_1m'
  | 'rmb_output_per_1m'
  | 'rmb_cache_read_per_1m'
  | 'rmb_cache_write_per_1m'
>

export const USD_FIELDS = [
  { key: 'usd_input_per_1m', label: '输入' },
  { key: 'usd_output_per_1m', label: '输出' },
  { key: 'usd_cache_read_per_1m', label: '缓存读' },
  { key: 'usd_cache_write_per_1m', label: '缓存写' }
] as const

export const RMB_FIELDS = [
  { key: 'rmb_input_per_1m', label: '输入' },
  { key: 'rmb_output_per_1m', label: '输出' },
  { key: 'rmb_cache_read_per_1m', label: '缓存读' },
  { key: 'rmb_cache_write_per_1m', label: '缓存写' }
] as const

/** 空表单。 */
export function emptyForm(): PriceForm {
  return {
    usd_input_per_1m: '',
    usd_output_per_1m: '',
    usd_cache_read_per_1m: '',
    usd_cache_write_per_1m: '',
    rmb_input_per_1m: '',
    rmb_output_per_1m: '',
    rmb_cache_read_per_1m: '',
    rmb_cache_write_per_1m: ''
  }
}

/**
 * 从一行记录取出可编辑的表单值。
 *
 * 与 emptyForm 分开是因为后端把「未填」存成 0：直接回填 "0" 会让用户
 * 以为那是自己填过的价格，而「没配」与「配成 0」在界面上必须能分开
 * （前者标「未定价」，后者是真实的免费）。所以 0 一律回填空串。
 */
export function formFrom(mp: Partial<ModelPrice> | null | undefined): PriceForm {
  const out = emptyForm()
  if (!mp) return out
  for (const f of [...USD_FIELDS, ...RMB_FIELDS]) {
    const raw = mp[f.key]
    const s = String(raw ?? '').trim()
    // 0 / 0.00 / 0.00000000 都算「没填」
    out[f.key] = s === '' || Number(s) === 0 ? '' : s
  }
  return out
}

/** 把表单转成提交载荷（空串原样传，后端按 0 处理）。
 *
 * 走同一份字段清单：八个字段逐行手抄的话，加一档要改四处
 * （emptyForm / formFrom / formError / 这里），漏掉那处的表现是
 * 「界面填了、保存也成功，但那一档永远存不进去」。 */
export function payloadFrom(name: string, form: PriceForm): Record<string, string> {
  const out: Record<string, string> = { model_name: name }
  for (const f of [...USD_FIELDS, ...RMB_FIELDS]) {
    out[f.key] = String(form[f.key] ?? '').trim()
  }
  return out
}

/**
 * 校验一个金额输入。返回错误文案，空串表示通过。
 *
 * 允许空（未填）；填了就必须是一个非负的十进制数。
 * 不用 Number() 判定合法性：`Number('1e5')` 是合法的 100000，
 * 而「每百万词元 1e5 元」几乎一定是手滑（Number('') 还是 0，
 * 那是另一个坑）。这里要求纯十进制写法。
 */
const DECIMAL_RE = /^\d+(\.\d+)?$/

export function amountError(raw: string): string {
  const s = String(raw ?? '').trim()
  if (!s) return ''
  if (!DECIMAL_RE.test(s)) return '请填写非负的十进制数字（如 1.25）'
  return ''
}

/** 校验整张表单，返回第一条错误（空串 = 全部通过）。 */
export function formError(form: PriceForm): string {
  for (const f of [...USD_FIELDS, ...RMB_FIELDS]) {
    const err = amountError(form[f.key])
    if (err) {
      const cur = f.key.startsWith('usd') ? '美元' : '人民币'
      return `${cur}${f.label}价：${err}`
    }
  }
  return ''
}

/** 一档（美元或人民币）是否填了价。
 *
 * 「填了 0」算**没填** —— 这条与后端 `PriceSet.IsZero()` 对齐。
 *
 * 为什么必须一致：后端把四档全 0 判定为「该币种没配价」，引擎据此
 * 走「没价」分支（不写价、费用记 0）。如果前端把 "0" 算成「已定价」，
 * 界面显示「仅美元 / $0」，而引擎认为根本没配 —— 同一个模型在界面上
 * 和账面上是两个状态。钱不会算错（都是 0），但这正是本仓库最忌讳的
 * 「界面说了 A、行为是 B」。
 *
 * 顺带也统一了 formFrom 的回填：库里存 0 的档回填空串，
 * 用户不会看到一堆自己没填过的 "0"。 */
export function tierPriced(form: PriceForm, currency: 'usd' | 'rmb'): boolean {
  const fields = currency === 'usd' ? USD_FIELDS : RMB_FIELDS
  return fields.some((f) => {
    const s = String(form[f.key] ?? '').trim()
    // 用 Number 而不是字符串判 0：'0'、'0.0'、'00.000' 都是零
    return s !== '' && Number(s) !== 0
  })
}

/**
 * 一个模型在界面上该显示的状态。
 *
 *   both     两档都配了价
 *   usd-only 只配了美元价
 *   rmb-only 只配了人民币价
 *   none     完全没配价（费用会被记成 0）
 */
export type PriceState = 'both' | 'usd-only' | 'rmb-only' | 'none'

export function priceState(form: PriceForm): PriceState {
  const usd = tierPriced(form, 'usd')
  const rmb = tierPriced(form, 'rmb')
  if (usd && rmb) return 'both'
  if (usd) return 'usd-only'
  if (rmb) return 'rmb-only'
  return 'none'
}

/**
 * 摘要文案：一行说清「这个模型卖多少钱」。
 *
 * 只显示输入/输出两档：缓存读写是少数场景，四个数字挤一行反而看不清。
 * 完整四档在编辑弹窗里。
 */
export function priceSummary(form: PriceForm): string {
  const parts: string[] = []
  if (tierPriced(form, 'usd')) {
    parts.push(`$${or0(form.usd_input_per_1m)} / $${or0(form.usd_output_per_1m)}`)
  }
  if (tierPriced(form, 'rmb')) {
    parts.push(`¥${or0(form.rmb_input_per_1m)} / ¥${or0(form.rmb_output_per_1m)}`)
  }
  return parts.length ? parts.join(' · ') : '未定价'
}

function or0(v: string | undefined): string {
  const s = String(v ?? '').trim()
  return s === '' ? '0' : s
}

/**
 * 结合渠道实际用到的币种，提示「这一档还没填」。
 *
 * 为什么要结合渠道：一个模型只在美元渠道里用，就不该提示「人民币价没填」——
 * 那种提示会让用户去填一个永远用不上的数字（填错了也不影响账单，
 * 但会让人以为自己配错了）。反过来，模型在人民币渠道里用却只配了
 * 美元价，那笔调用就是 0 元，必须点名。
 *
 * usedCurrencies 来自后端（模型 -> 它在渠道里实际用到的币种数组）。
 * 空数组（没有渠道在用）时不做任何提示。
 *
 * 这里对输入做一次形状收敛，而不是信任调用方：后端曾经把同一字段返回成
 * map[string]bool（{"CNY":true}），前端按数组调 .includes() 直接抛
 * TypeError、整个弹窗崩掉 —— 而两侧代码各自都合法，类型检查看不出来。
 * 判据函数是最后一道边界，让它在任何形状下都只回答「缺哪一档」。
 */
export function missingTierWarning(form: PriceForm, usedCurrencies: string[]): string {
  // 容忍 map / null / undefined：只关心「出现过哪些币种」
  const currencies: string[] = Array.isArray(usedCurrencies)
    ? usedCurrencies
    : usedCurrencies && typeof usedCurrencies === 'object'
      ? Object.keys(usedCurrencies).filter((k) => Boolean((usedCurrencies as Record<string, unknown>)[k]))
      : []
  const usesUSD = currencies.includes('USD')
  const usesRMB = currencies.includes('CNY') || currencies.includes('RMB')
  const hasUSD = tierPriced(form, 'usd')
  const hasRMB = tierPriced(form, 'rmb')

  const missing: string[] = []
  if (usesRMB && !hasRMB) missing.push('人民币')
  if (usesUSD && !hasUSD) missing.push('美元')
  if (!missing.length) return ''
  return `有${missing.join('与')}渠道在用这个模型，但对应币种的单价没填 —— 那些调用会被记成 0 元`
}

/** 币种代码（后端的 CNY）在界面上的写法。 */
export function currencyLabel(code: string): string {
  if (code === 'CNY' || code === 'RMB') return '人民币'
  if (code === 'USD') return '美元'
  return code
}
