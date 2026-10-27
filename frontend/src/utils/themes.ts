// 内置主题注册表（2026-09-30 主题系统扩容）。
//
// 这里只有**元数据**（id / 名字 / 明暗族 / 外壳色 / 出处），没有任何色值 ——
// 颜色令牌的唯一真源是 styles/theme.css 里各主题那一块。这样安排是因为
// 「同一份色值写两处」必然会漂移：theme.ts 里那段长注释记的就是早期
// 在 JS 里又抄了一份色值、结果改了 CSS 不生效的坑。
// 想知道某套主题长什么样，看 CSS；想知道它叫什么、属于哪一族、从哪个上游来，看这里。
//
// 一条轴：<html data-theme="<id>"> 就是全部状态。原来那个「深色质感档位」
// （data-darkstyle=classic|oled）已经取消：它只对「经典深灰」生效，
// 多主题下会变成「选一个只影响某一套主题的开关」，比主题本身更难解释。
// 现在 OLED 就是一套普通主题（id=oled），老键在 store 里做一次性迁移。

/** 明暗族。决定 antd 用哪套算法、预览分组、以及在设置页里的归属。 */
export type ThemeTone = 'light' | 'dark'
export type ThemeMode = 'light' | 'dark' | 'auto'

export interface ThemeDef {
  /** 同时也是 CSS 里 `:root[data-theme='<id>']` 的锚点，以及 localStorage 的值。 */
  id: string
  /** 界面上的中文名。 */
  name: string
  /** 上游配色项目的名字，写在主题卡的第二行。 */
  upstream: string
  tone: ThemeTone
  /** 一句话说明，弹层里悬停可见、设置页里直接显示。 */
  desc: string
  /**
   * 浏览器外壳色（地址栏/状态栏）。meta[name=theme-color] 用它。
   */
  shell: string
  /**
   * 光标资源复用哪个主题的文件。默认就是自己（`-<id>.svg`）。
   */
  cursorSource?: string
  /** 上游调色板出处，写进 docs 与主题块注释用。 */
  sourceUrl: string
}

export const THEMES: ThemeDef[] = [
  {
    id: 'light',
    name: '暖白',
    upstream: '本站默认',
    tone: 'light',
    desc: '陶土主色配米白底，明亮温暖的浅色界面。',
    shell: '#c87864',
    sourceUrl: 'https://llm.ohub.vip'
  },
  {
    id: 'dark',
    name: '深灰',
    upstream: '本站默认深色',
    tone: 'dark',
    desc: '中性深灰表面，长时间盯盘不刺眼。',
    shell: '#202020',
    sourceUrl: 'https://llm.ohub.vip'
  }
]

export const LIGHT_IDS = THEMES.filter((t) => t.tone === 'light').map((t) => t.id)
export const DARK_IDS = THEMES.filter((t) => t.tone === 'dark').map((t) => t.id)
export const THEME_IDS = THEMES.map((t) => t.id)

/** 没有任何偏好、也没跟随到系统偏好时用哪一套（同时也是「默认」角标的归属）。 */
export const DEFAULT_LIGHT = 'light'
export const DEFAULT_DARK = 'dark'

/**
 * 校验一个任意字符串是不是主题 id。
 *
 * localStorage 里的值可以是任何历史遗留内容（更早版本存过 'auto'、
 * 手改的脏数据、装了新版本又被降级回去时留下的新 id）。取到非法值会让
 * data-theme 指向一个没有样式块的锚点 —— 页面直接退回「无主题」状态，
 * 所以凡是从存储或属性里读回来的值都必须先过这里。
 */
export function isThemeId(v: unknown): v is string {
  return typeof v === 'string' && THEME_IDS.includes(v)
}

/** 按 id 取主题；调用方保证 id 合法（isThemeId 校验过）。 */
export function themeById(id: string): ThemeDef {
  const found = THEMES.find((t) => t.id === id)
  // 兜底而不是抛异常：主题取不到只该退化成默认配色，不该让页面白屏
  return found ?? THEMES[0]
}
