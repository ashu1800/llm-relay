// 主题对比度红线（2026-09-30 随 12 套主题一起立）。
//
// 为什么需要它：主题从 2 套涨到 12 套之后，「每个值都量过对比度」这件事
// 不可能靠人肉保证 —— 上游配色（Solarized、Tokyo Night 这类）本来就是
// 低对比取向，它们的正文色直接拿来用会低于 WCAG AA。而这个失败模式
// 不报错、不失败、截图也看不出（只是某些字「有点发虚」），
// 于是它必须变成 CI 里一条会红的断言。
//
// 口径：
//   文字类 ≥ 4.5:1（WCAG 2.1 AA 正文）
//   非文本（图标、主色块、焦点环）≥ 3:1（WCAG 1.4.11）
//   边框 ≥ 1.15:1 —— 不是 WCAG 条款，是「边框看得见」的工程下限
//   （现有三套主题实测 1.30~1.62，留足余量）
//
// 半透明色（rgba）按「叠在目标底色上合成后的实色」计算：
// --color-text-secondary 是 alpha 0.70 的灰，它读不读得清取决于它压在哪层底上。
//
// 用法：node scripts/check-theme-contrast.mjs [--verbose]
import { FAMILY_TOKENS, parseRegistry, parseThemes } from './lib/theme-css.mjs'

const VERBOSE = process.argv.includes('--verbose')

// ---- 颜色解析与 WCAG 计算 ----

/** 解析 #rgb / #rrggbb / #rrggbbaa / rgb() / rgba()；解析不了返回 null。 */
function parseColor(input) {
  if (!input) return null
  const s = input.trim()
  const hex = s.match(/^#([0-9a-fA-F]{3,8})$/)
  if (hex) {
    let h = hex[1]
    if (h.length === 3) h = h.split('').map((c) => c + c).join('')
    if (h.length !== 6 && h.length !== 8) return null
    return {
      r: parseInt(h.slice(0, 2), 16),
      g: parseInt(h.slice(2, 4), 16),
      b: parseInt(h.slice(4, 6), 16),
      a: h.length === 8 ? parseInt(h.slice(6, 8), 16) / 255 : 1
    }
  }
  const fn = s.match(/^rgba?\(([^)]+)\)$/)
  if (fn) {
    const parts = fn[1].split(/[,\s/]+/).filter(Boolean).map(Number)
    if (parts.length < 3 || parts.some((n) => Number.isNaN(n))) return null
    return { r: parts[0], g: parts[1], b: parts[2], a: parts.length > 3 ? parts[3] : 1 }
  }
  return null
}

/** WCAG 相对亮度 */
function luminance({ r, g, b }) {
  const f = (v) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
}

/** 合成：alpha 前景压在实色底上 */
function over(fg, bg) {
  const a = fg.a
  return {
    r: Math.round(fg.r * a + bg.r * (1 - a)),
    g: Math.round(fg.g * a + bg.g * (1 - a)),
    b: Math.round(fg.b * a + bg.b * (1 - a)),
    a: 1
  }
}

function contrast(fg, bg) {
  const l1 = luminance(fg)
  const l2 = luminance(bg)
  const hi = Math.max(l1, l2)
  const lo = Math.min(l1, l2)
  return (hi + 0.05) / (lo + 0.05)
}

/** 主色 20% 透明压在页面底上 —— 侧栏菜单选中态的背景（见 --color-primary-a20） */
function primaryA20(primary, bg) {
  return over({ ...primary, a: 0.2 }, bg)
}

// ---- 断言表 ----
//
// 每一项： [说明, 前景令牌, 背景令牌, 阈值]
// 背景可以写成 'menu:bg' 表示「主色 20% 叠在该令牌之上」这种派生底。
const TEXT_MIN = 4.5
const NON_TEXT_MIN = 3.0
const BORDER_MIN = 1.15

const CHECKS = [
  ['正文 on 页面底', '--color-text', '--color-bg', TEXT_MIN],
  ['正文 on 卡片', '--color-text', '--color-fg', TEXT_MIN],
  ['次要文字 on 页面底', '--color-text-secondary', '--color-bg', TEXT_MIN],
  ['次要文字 on 卡片', '--color-text-secondary', '--color-fg', TEXT_MIN],
  ['占位文字 on 卡片', '--color-text-placeholder', '--color-fg', TEXT_MIN],
  ['主色文字 on 卡片', '--text-primary-ink', '--color-fg', TEXT_MIN],
  ['主色文字 on 菜单选中底', '--text-primary-ink', 'menu:--color-bg', TEXT_MIN],
  ['语义绿 on 卡片', '--text-green', '--color-fg', TEXT_MIN],
  ['语义琥珀 on 卡片', '--text-amber', '--color-fg', TEXT_MIN],
  ['语义红 on 卡片', '--text-red', '--color-fg', TEXT_MIN],
  ['语义紫 on 卡片', '--text-purple', '--color-fg', TEXT_MIN],
  ['语义陶土 on 卡片', '--text-terracotta', '--color-fg', TEXT_MIN],
  ['语义蓝 on 卡片', '--text-blue', '--color-fg', TEXT_MIN],
  ['语义灰 on 卡片', '--text-gray', '--color-fg', TEXT_MIN],
  ['实心块文字 on 实心块底', '--solid-primary-fg', '--solid-primary-bg', TEXT_MIN],
  ['品牌标记字 on 品牌标记底', '--brand-mark-fg', '--brand-mark-bg', TEXT_MIN],
  ['提示文字 on 提示底', '--tooltip-fg', '--tooltip-bg', TEXT_MIN],
  ['主色 on 页面底（非文本）', '--color-primary', '--color-bg', NON_TEXT_MIN],
  ['主色 on 卡片（非文本）', '--color-primary', '--color-fg', NON_TEXT_MIN],
  ['图标色 on 卡片（非文本）', '--color-icon', '--color-fg', NON_TEXT_MIN],
  // 侧栏的图标直接画在页面底上（不是卡片里），这条对应那一处
  ['图标色 on 页面底（非文本）', '--color-icon', '--color-bg', NON_TEXT_MIN],
  ['焦点环 on halo（非文本）', '--text-primary-ink', '--focus-ring-halo', NON_TEXT_MIN],
  ['边框 on 页面底', '--color-border', '--color-bg', BORDER_MIN],
  ['边框 on 卡片', '--color-border', '--color-fg', BORDER_MIN]
]

// 族档位令牌：这些值在同一个族里应当一致（浅色族一套、深色族一套）。
// 不一致不算失败 —— 主题有权自己微调 —— 但要打出来让人确认是「有意为之」
// 还是「改了一套忘了另一套」。清单在 lib/theme-css.mjs 里定义（与契约脚本共用）。

// ---- 跑 ----

const themes = parseThemes()
// 绝对下限，而不是只判「非空」：解析器结构变了、只解析出 1 套主题时，
// 后面照样会打印「1 套主题 × 24 项断言全绿」—— 全绿但什么都没验。
const MIN_THEMES = 12
if (themes.size < MIN_THEMES) {
  console.error(`只解析到 ${themes.size} 套主题（应至少 ${MIN_THEMES} 套）—— theme.css 的结构变了，还是漏了主题块？`)
  process.exit(1)
}

let failed = 0
let checks = 0

console.log('=== 主题对比度红线 ===')
console.log(
  `  口径：文字 ≥ ${TEXT_MIN}:1（AA 正文）、非文本 ≥ ${NON_TEXT_MIN}:1（1.4.11）、边框 ≥ ${BORDER_MIN}:1`
)

for (const [id, theme] of themes) {
  const vars = theme.vars
  const bad = []
  const lines = []

  const resolve = (name) => {
    if (name.startsWith('menu:')) {
      const base = vars.get(name.slice(5))
      const primary = vars.get('--color-primary')
      const b = parseColor(base)
      const p = parseColor(primary)
      if (!b || !p) return { color: null, label: `menu(${base} + 主色 20%)` }
      return { color: primaryA20(p, b), label: `菜单选中底(${base} + 主色 20%)` }
    }
    const raw = vars.get(name)
    const bgName = name
    return { color: parseColor(raw), label: bgName, raw }
  }

  for (const [label, fgName, bgName, min] of CHECKS) {
    checks++
    const fgRaw = vars.get(fgName)
    const fgParsed = parseColor(fgRaw)
    const bg = resolve(bgName)
    if (!fgParsed || !bg.color) {
      failed++
      bad.push(`${label}：解析不了色值（${fgName}=${fgRaw ?? '缺'} / ${bg.label}=${bg.raw ?? '缺'}）`)
      continue
    }
    const base = bg.color
    // 前景若带 alpha（如 --color-text-secondary 的 0.70），先合成到底色上再算
    const eff = fgParsed.a < 1 ? over(fgParsed, base) : fgParsed
    const ratio = contrast(eff, base)
    const ok = ratio >= min
    if (!ok) {
      failed++
      bad.push(`${label}：${ratio.toFixed(2)}:1 < ${min}:1`)
    }
    lines.push(`${ok ? '·' : '!'} ${label} ${ratio.toFixed(2)}:1`)
  }

  const head = `${id.padEnd(16)} ${theme.selector}`
  if (bad.length === 0) {
    console.log(`  PASS  ${head}`)
    if (VERBOSE) for (const l of lines) console.log(`          ${l}`)
  } else {
    console.log(`  FAIL  ${head}`)
    for (const b of bad) console.log(`          ! ${b}`)
    if (VERBOSE) for (const l of lines) console.log(`          ${l}`)
  }
}

// ---- 族档位一致性（INFO，非失败）----
//
// 只在**同一个族内部**比：浅色族一套值、深色族一套值，这是设计如此。
// 族内出现第二种取值才值得打出来 —— 那通常意味着「改了一套忘了另一套」，
// 但也可能是某个主题有意微调（例如某个配色的卡片特别暗），所以只提示不判失败。
console.log('')
console.log('=== 族档位令牌一致性（族内，INFO）===')
const toneOf = new Map(parseRegistry().map((t) => [t.id, t.tone]))
for (const token of FAMILY_TOKENS) {
  const notes = []
  for (const tone of ['light', 'dark']) {
    const byValue = new Map()
    for (const [id, theme] of themes) {
      if ((toneOf.get(id) ?? 'light') !== tone) continue
      const v = theme.vars.get(token)
      const key = v === undefined ? '(未定义)' : v
      byValue.set(key, [...(byValue.get(key) || []), id])
    }
    if (byValue.size > 1) {
      const detail = [...byValue.entries()].map(([v, ids]) => `${v} ← ${ids.join('/')}`).join('；')
      notes.push(`${tone === 'light' ? '浅色族' : '深色族'}有 ${byValue.size} 种取值 —— ${detail}`)
    }
  }
  if (notes.length === 0) {
    console.log(`  OK    ${token}：两族各自同值`)
  } else {
    console.log(`  INFO  ${token}：${notes.join(' | ')}`)
  }
}

console.log('')
if (failed) {
  console.error(`对比度检查不通过：${failed} 项不达标（共 ${checks} 项）`)
  process.exit(1)
}
console.log(`对比度检查通过：${themes.size} 套主题 × ${checks / themes.size} 项断言全绿`)
