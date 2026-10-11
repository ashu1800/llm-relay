// 颜色解析与 WCAG 对比度计算 —— check-theme-contrast.mjs（令牌红线）与
// check-contracts.mjs（组件里把填充色当正文色用的守卫）共用这一份。
//
// 为什么抽出来：两处都要「把 var(--color-x) 解析成实色再算比值」。
// 各写一份的后果不是报错，而是两边算法慢慢分叉 —— 一处算了 alpha 合成、
// 另一处没算，于是同一个颜色在两个脚本里得出不同的比值，谁都说不清哪个对。
// 只做机械计算，不做任何判断（阈值与合法性由各自的脚本决定）。

/** 解析 #rgb / #rrggbb / #rrggbbaa / rgb() / rgba()；解析不了返回 null。 */
export function parseColor(input) {
  if (!input) return null
  const s = String(input).trim()
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
export function luminance({ r, g, b }) {
  const f = (v) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
}

/** 合成：alpha 前景压在实色底上 */
export function over(fg, bg) {
  const a = fg.a
  return {
    r: Math.round(fg.r * a + bg.r * (1 - a)),
    g: Math.round(fg.g * a + bg.g * (1 - a)),
    b: Math.round(fg.b * a + bg.b * (1 - a)),
    a: 1
  }
}

/** 两个实色的 WCAG 对比度（1..21） */
export function contrast(fg, bg) {
  const l1 = luminance(fg)
  const l2 = luminance(bg)
  const hi = Math.max(l1, l2)
  const lo = Math.min(l1, l2)
  return (hi + 0.05) / (lo + 0.05)
}

/** 把 `#rrggbb` 还原成可读的字符串（报错信息里用） */
export function colorText(c) {
  if (!c) return '(解析失败)'
  const h = (v) => v.toString(16).padStart(2, '0')
  return `#${h(c.r)}${h(c.g)}${h(c.b)}`
}
