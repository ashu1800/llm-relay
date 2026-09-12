// 分组身份的视觉标识（全站所有「显示分组」的地方都从这里取色）。
//
// 与模型胶囊的区别（这条区别是刻意的）：
//   - 模型颜色**只**由模型名派生，任何模型名都有颜色；
//   - 分组颜色可以由用户在分组管理里指定，没指定才按分组名派生。
//     分组是「我自己配的东西」，给它一个我挑的颜色是合理的；
//     模型名是外部来的，不能被配置。
//
// 所以这里返回两种模式：
//   - auto = true：给 oklch 的 h/c 分量，明度由 CSS 按主题决定；
//   - auto = false：给一个 #rrggbb 基色，透明度/明度交给 CSS 的 color-mix，
//     这样深色主题下能自动把深色提亮，不必在 JS 里判断主题。
//
// 对比度约束与模型胶囊一致：胶囊里始终写着分组名，颜色只是辅助，
// 色盲用户与黑白打印同样能分辨。
import { hash32 } from './hash'

export interface GroupStyle {
  /** 展示名：就是分组名本身 */
  label: string
  /** oklch 色相角（0-360），auto 模式下有效 */
  hue: number
  /** oklch 彩度；0 表示中性灰（拿不到分组名时用） */
  chroma: number
  /** 自定义基色（#rrggbb）；空表示自动配色 */
  color: string
  /** 是否为自动配色 */
  auto: boolean
}

/** 未知分组的彩度：看得出色相，又不至于溢出 sRGB 色域 */
const FALLBACK_CHROMA = 0.13

/** #rgb / #rrggbb 才认，其它（含后端以后可能放宽的写法）一律退回自动配色。 */
export function normalizeHexColor(raw?: string | null): string {
  const s = (raw || '').trim().toLowerCase()
  if (!/^#([0-9a-f]{3}|[0-9a-f]{6})$/.test(s)) return ''
  // #abc 展开成 #aabbcc：统一成一种写法，比较与显示都不会有两种形态
  if (s.length === 4) {
    return '#' + s[1] + s[1] + s[2] + s[2] + s[3] + s[3]
  }
  return s
}

/** 取分组样式。任何分组名都有样式，不会没有颜色。 */
export function groupStyle(name?: string | null, color?: string | null): GroupStyle {
  const label = (name || '').trim() || '未知分组'
  const custom = normalizeHexColor(color)
  if (custom) {
    return { label, hue: 0, chroma: 0, color: custom, auto: false }
  }
  const bare = label.toLowerCase()
  return { label, hue: hash32(bare) % 360, chroma: FALLBACK_CHROMA, color: '', auto: true }
}

/** 把样式转成可内联的 CSS 变量，颜色本身仍由 CSS 决定（便于适配深色主题）。 */
export function groupVars(s: GroupStyle): Record<string, string> {
  if (!s.auto) {
    return { '--gt-color': s.color }
  }
  return { '--gt-h': String(Math.round(s.hue)), '--gt-c': String(s.chroma) }
}
