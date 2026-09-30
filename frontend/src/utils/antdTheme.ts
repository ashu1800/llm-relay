// Ant Design Vue 的设计令牌 ←→ 本站 CSS 令牌的映射（2026-09-30 主题系统扩容时抽出）。
//
// 为什么不再在 App.vue 里手写这些色值：
//
// 多主题之前，App.vue 里那 13 个 antd 令牌其实**逐字等于** theme.css 里的 13 个令牌
// （colorPrimary = --solid-primary-bg、colorSuccess = --color-green、
// colorBgContainer = --color-fg …）。两个主题时还抄得动，12 套主题 × 13 个值
// 就是 156 份会各自漂移的副本 —— 而漂移的表现是「按钮颜色和卡片不是同一套主题」，
// 不报错、不失败，只是看着不对劲。
//
// 现在改成从**计算样式**里读 CSS 自定义属性：色值仍然只有 theme.css 一份，
// 这个文件只负责「哪个令牌对哪个令牌」。读的名字如果 CSS 里没有，
// check-contracts.mjs 会直接判失败（见「主题契约」一节），
// 所以不存在「读了个不存在的变量、静默退回 antd 默认色」这种失败模式。
//
// 纯函数 + 注入读取器：这样这个映射能在 happy-dom 里单测
// （测试环境拿不到真实计算样式，注入桩即可）。
import type { ThemeTone } from '@/utils/themes'

/**
 * antd 令牌 → 本站 CSS 令牌。左列是 Ant Design Vue 的 token 名，
 * 右列是 styles/theme.css 里的变量名。
 *
 * 注释里的对比度依据见 theme.css 各主题块（那些数字是 check-theme-contrast.mjs
 * 算出来的，不是手抄的）。
 */
export const ANTD_VAR_MAP = {
  // 主色：实心按钮的底色。刻意不是 --color-primary —— #c87864 上压白字
  // 只有 3.32:1，达不到正文级文字的 4.5:1；--solid-primary-bg 是专为
  // 「主色铺底 + 上面写字」挑的那一档，两套主题的方向也相反：
  // 浅色族深底白字、深色族浅底深字。
  colorPrimary: '--solid-primary-bg',
  colorInfo: '--solid-primary-bg',
  colorSuccess: '--color-green',
  colorWarning: '--color-orange',
  colorError: '--color-red',
  // 实心按钮上的文字色，与 colorPrimary 成对使用（只换一个会得到比原来更差的结果）
  colorTextLightSolid: '--solid-primary-fg',
  colorTextBase: '--color-text',
  colorBgBase: '--color-bg',
  colorBgContainer: '--color-fg',
  colorBorder: '--color-border',
  // 文字灰阶必须显式映射：antd 默认的 rgba(0,0,0,.25) 占位文字压白底只有 1.84:1。
  // 这三档的值与 theme.css 同源，且都由对比度脚本盯着。
  colorTextSecondary: '--color-text-secondary',
  colorTextPlaceholder: '--color-text-placeholder',
  colorTextTertiary: '--color-text-tertiary'
} as const

export type AntdTokenKey = keyof typeof ANTD_VAR_MAP

/** 从计算样式里读一个 CSS 自定义属性；读不到（CSS 没加载、拼错）返回空串。 */
export type CssVarReader = (name: string) => string

/**
 * 组装 antd 的 token 覆盖表。
 *
 * 只返回会随主题变化的部分；与主题无关的两项（圆角、字体）在调用处固定写死，
 * 放进来反而会让人以为它们也跟主题走。
 */
export function buildAntdTokens(read: CssVarReader): Record<AntdTokenKey, string> {
  const out = {} as Record<AntdTokenKey, string>
  for (const key of Object.keys(ANTD_VAR_MAP) as AntdTokenKey[]) {
    out[key] = read(ANTD_VAR_MAP[key])
  }
  return out
}

/**
 * 运行时读取器：从 <html> 的计算样式里取变量。
 *
 * 必须是「先由 store 把 data-theme 写到 <html> 上、再读」——
 * 自定义属性按元素解析，读的是 documentElement 上此刻生效的那一套。
 * store 的 watch(immediate) 在应用启动时同步写完属性，之后的切换也在
 * 同一个 tick 内完成，所以调用处（App.vue 的 computed）读到的永远是新主题的值。
 */
export function readCssVar(name: string): string {
  if (typeof document === 'undefined') return ''
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  if (!value && import.meta.env.DEV) {
    // 开发期只提醒一次：生产环境这条路走不通就说明 CSS 没打进去，
    // 而那是契约脚本该在 CI 里拦住的事，不该靠运行时报错发现。
    warnMissing(name)
  }
  return value
}

const warned = new Set<string>()
function warnMissing(name: string) {
  if (warned.has(name)) return
  warned.add(name)
  console.warn(
    `[theme] CSS 变量 ${name} 读不到值 —— antd 这一项会退回它自己的默认色。` +
      `请检查 styles/theme.css 里该主题块是否定义了它（npm run check 会拦这一类）。`
  )
}

/** 明暗族 → antd 算法。它不是 token，所以单独一个纯函数，方便调用处组合。 */
export function isDarkTone(tone: ThemeTone): boolean {
  return tone === 'dark'
}
