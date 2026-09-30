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
   *
   * 口径与多主题之前逐字一致，因此三套老主题的地址栏颜色不会变：
   *   浅色族 → 该主题的 --color-primary（有品牌色可用，地址栏就是品牌的）
   *   深色族 → 该主题的 --color-bg（深色下用高饱和品牌色会非常扎眼）
   * 这个值由 frontend/scripts/check-contracts.mjs 对着 CSS 逐字比对，
   * 所以这里不可能出现一个「自己编的」色值。
   */
  shell: string
  /**
   * 光标资源复用哪个主题的文件。默认就是自己（`-<id>.svg`）。
   * 只有 oled 用得上：它的实心块那一对与 dark 完全相同，再生成一份
   * 字节一样的文件没有意义 —— 契约脚本按这个字段校验文件名的归属。
   */
  cursorSource?: string
  /** 上游调色板出处，写进 docs 与主题块注释用。 */
  sourceUrl: string
}

// 顺序即界面顺序：浅色组在前、深色组在后（组件按 tone 分组渲染，
// 组内保持这里的声明顺序）。新增主题请加在这一段的末尾，
// 并同步：theme.css 的主题块、index.html 的预涂清单、光标资源 ——
// 四处齐不齐由 `npm run check` 盯着（契约脚本内部就会跑一次光标比对），
// 想单独核对光标同步用 `node scripts/gen-theme-cursors.mjs --check`
// （注意别写成 `npm run gen:cursors -- --check`：那个 npm 脚本本身就是 --write，
//  展开后会变成 `--write --check`，脚本按「有 --write 就写入」处理，等于白核对）。
export const THEMES: ThemeDef[] = [
  {
    id: 'light',
    name: '暖白',
    upstream: '本站默认',
    tone: 'light',
    desc: '陶土主色配米白底，本站原本的观感（参考站实测配色）。',
    shell: '#c87864',
    sourceUrl: 'https://llm.ohub.vip'
  },
  {
    id: 'latte',
    name: '拿铁浅棕',
    upstream: 'Catppuccin Latte',
    tone: 'light',
    desc: '低饱和 pastel 暖调，米灰底上一切都很轻。',
    shell: '#8839ef',
    sourceUrl: 'https://catppuccin.com/palette/'
  },
  {
    id: 'solarized-light',
    name: '日光浅',
    upstream: 'Solarized Light',
    tone: 'light',
    desc: '2011 年的经典低对比配色，纸感米黄底，护眼取向。',
    shell: '#268bd2',
    sourceUrl: 'https://ethanschoonover.com/solarized/'
  },
  {
    id: 'gruvbox-light',
    name: '复古浅',
    upstream: 'Gruvbox Light',
    tone: 'light',
    desc: '暖黄旧纸质感，配深褐与铁锈色，复古终端味。',
    shell: '#af3a03',
    sourceUrl: 'https://github.com/morhetz/gruvbox'
  },
  {
    id: 'dark',
    name: '深灰',
    upstream: '本站默认深色',
    tone: 'dark',
    desc: '中性深灰表面，长时间盯盘不刺眼（参考站 dark 实测配色）。',
    shell: '#202020',
    sourceUrl: 'https://llm.ohub.vip'
  },
  {
    id: 'oled',
    name: '深空纯黑',
    upstream: '本站扩展',
    tone: 'dark',
    desc: '表面换纯黑：纯黑像素不发光，OLED 屏省电、夜间更沉浸。',
    shell: '#0a0a0a',
    cursorSource: 'dark',
    sourceUrl: 'https://llm.ohub.vip'
  },
  {
    id: 'mocha',
    name: '摩卡紫',
    upstream: 'Catppuccin Mocha',
    tone: 'dark',
    desc: '社区人气最高的 pastel 深色配色，柔和不刺眼。',
    shell: '#1e1e2e',
    sourceUrl: 'https://catppuccin.com/palette/'
  },
  {
    id: 'nord',
    name: '极地夜',
    upstream: 'Nord',
    tone: 'dark',
    desc: '冷调蓝灰，饱和度低、对比克制，北欧极夜观感。',
    shell: '#2e3440',
    sourceUrl: 'https://www.nordtheme.com/docs/colors-and-palettes'
  },
  {
    id: 'dracula',
    name: '暗夜紫',
    upstream: 'Dracula',
    tone: 'dark',
    desc: '深紫底配霓虹紫粉，风格强烈，夜里辨识度最高。',
    shell: '#282a36',
    sourceUrl: 'https://github.com/dracula/dracula-theme'
  },
  {
    id: 'tokyo-night',
    name: '东京夜',
    upstream: 'Tokyo Night',
    tone: 'dark',
    desc: '东京夜色蓝调：深蓝底配天蓝与紫，编辑器的常见门面。',
    shell: '#1a1b26',
    sourceUrl: 'https://github.com/enkia/tokyo-night-vscode-theme'
  },
  {
    id: 'solarized-dark',
    name: '日光深',
    upstream: 'Solarized Dark',
    tone: 'dark',
    desc: '深青底的低对比经典配色，长时间阅读负担小。',
    shell: '#002b36',
    sourceUrl: 'https://ethanschoonover.com/solarized/'
  },
  {
    id: 'gruvbox-dark',
    name: '复古深',
    upstream: 'Gruvbox Dark',
    tone: 'dark',
    desc: '暖灰底配橘黄，像老式终端的琥珀屏。',
    shell: '#282828',
    sourceUrl: 'https://github.com/morhetz/gruvbox'
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
