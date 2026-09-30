// 主题样式表的解析器 —— check-contracts / check-theme-contrast / gen-theme-cursors
// 三个脚本共用这一份，避免「三份正则各解析一遍主题块」这种迟早会漂的结构。
//
// 只做机械解析，不做任何判断：谁合法、什么该报错，由各自的脚本决定。
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

export const FRONTEND_DIR = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
export const SRC_DIR = join(FRONTEND_DIR, 'src')
export const THEME_CSS = join(SRC_DIR, 'styles', 'theme.css')
export const REGISTRY_TS = join(SRC_DIR, 'utils', 'themes.ts')
export const INDEX_HTML = join(FRONTEND_DIR, 'index.html')
export const PUBLIC_DIR = join(FRONTEND_DIR, 'public')

export const readThemeCss = () => readFileSync(THEME_CSS, 'utf8')

/**
 * 取一个选择器块的内容。从 selectorStart 往后找第一个 `{`，
 * 到第一个行首 `}` 结束（本文件所有块的收尾都写在行首，见 theme.css 的排版）。
 * @returns {string|null}
 */
function blockBody(src, selectorStart) {
  const open = src.indexOf('{', selectorStart)
  if (open < 0) return null
  const end = src.indexOf('\n}', open)
  if (end < 0) return null
  return src.slice(open + 1, end)
}

/** 抽一个块里的全部 `--x: value` 定义。允许值跨行（字体族那种长值）。 */
function readVars(body) {
  const vars = new Map()
  for (const m of body.matchAll(/^\s*(--[\w-]+)\s*:\s*([\s\S]*?);/gm)) {
    vars.set(m[1], m[2].replace(/\s+/g, ' ').trim())
  }
  return vars
}

/**
 * 解析 theme.css 里的主题块。
 *
 * `:root {` 就是 `light` 主题本尊（浅色默认主题不另开一个块，否则同一套浅色
 * 会有两份定义）；其余主题是 `:root[data-theme='<id>']`。
 *
 * @returns {Map<string, {id: string, selector: string, body: string,
 *   vars: Map<string,string>, colorScheme: string|null}>} 按 id 索引
 */
export function parseThemes(src = readThemeCss()) {
  const themes = new Map()

  const rootIdx = src.indexOf(':root {')
  if (rootIdx >= 0) {
    const body = blockBody(src, rootIdx)
    if (body !== null) {
      themes.set('light', {
        id: 'light',
        selector: ':root',
        body,
        vars: readVars(body),
        colorScheme: readColorScheme(body)
      })
    }
  }

  for (const m of src.matchAll(/:root\[data-theme='([^']+)'\]\s*\{/g)) {
    const body = blockBody(src, m.index)
    if (body === null) continue
    themes.set(m[1], {
      id: m[1],
      selector: `:root[data-theme='${m[1]}']`,
      body,
      vars: readVars(body),
      colorScheme: readColorScheme(body)
    })
  }

  return themes
}

function readColorScheme(body) {
  const m = body.match(/^\s*color-scheme\s*:\s*([^;]+);/m)
  return m ? m[1].trim() : null
}

/**
 * 解析 `[data-swatch='<id>']` 预览色样表（主题卡里那块迷你预览的取色来源）。
 * 色值是纯展示用的字面量，所以必须由脚本盯住它们与主题块逐字一致 ——
 * 否则「卡片上看到的样子」和「点下去得到的样子」可以不是一回事。
 */
export function parseSwatches(src = readThemeCss()) {
  const out = new Map()
  for (const m of src.matchAll(/\[data-swatch='([^']+)'\]\s*\{/g)) {
    const body = blockBody(src, m.index)
    if (body === null) continue
    out.set(m[1], readVars(body))
  }
  return out
}

/**
 * 解析注册表 `src/utils/themes.ts` 里的 THEMES 数组。
 *
 * 不用 TS 解析器：这一段的形状很固定（每个对象一行 id、一行 tone、一行 shell），
 * 按对象切块后取字段即可，脚本也不该为了读一份声明式数据引入编译链。
 * @returns {{id: string, tone: string, shell: string, cursorSource: string|null}[]}
 */
export function parseRegistry(src = readFileSync(REGISTRY_TS, 'utf8')) {
  const start = src.indexOf('export const THEMES')
  const end = src.indexOf('\n]', start)
  const region = start >= 0 && end > start ? src.slice(start, end) : ''
  const items = []
  for (const chunk of region.split(/\n  \},/)) {
    const id = chunk.match(/\bid:\s*'([^']+)'/)
    if (!id) continue
    const tone = chunk.match(/\btone:\s*'([^']+)'/)
    const shell = chunk.match(/\bshell:\s*'([^']+)'/)
    const cursorSource = chunk.match(/\bcursorSource:\s*'([^']+)'/)
    items.push({
      id: id[1],
      tone: tone ? tone[1] : null,
      shell: shell ? shell[1] : null,
      cursorSource: cursorSource ? cursorSource[1] : null
    })
  }
  return items
}

/**
 * 解析 index.html 预涂脚本里的主题 id 清单。
 *
 * 这段脚本必须在打包产物（module script）之前跑起来，所以它**不能** import
 * 注册表，只能自己带一份数组 —— 于是这份清单与注册表的一致性必须由脚本盯住，
 * 否则表现是「某个主题刷新时先闪一下别的配色」。
 * @returns {Set<string>} 合法主题 id
 */
export function parseInlineList(src = readFileSync(INDEX_HTML, 'utf8')) {
  const start = src.indexOf('var THEME_IDS')
  if (start < 0) return new Set()
  const open = src.indexOf('[', start)
  const end = src.indexOf(']', open)
  if (open < 0 || end < 0) return new Set()
  const out = new Set()
  for (const m of src.slice(open + 1, end).matchAll(/'([\w-]+)'/g)) out.add(m[1])
  return out
}

/** 从块里读一个颜色令牌（仅接受 #rgb/#rrggbb，供对比度脚本取字面值）。 */
export function readColor(vars, name) {
  const v = vars.get(name)
  return v === undefined ? null : v
}

/**
 * 每套主题块**必须写全**的令牌清单（2026-09-30 立）。
 *
 * 为什么是「写全」而不是「继承 :root，只写不一样的」：
 * 主题块漏写一个令牌时，它会静默继承 :root（浅色）的值 —— 深色主题里
 * 出现一个浅色主题的边框色，不报错、不失败、测试也测不到，
 * 只有人眼在某一块屏幕上才可能发现。而「靠继承拿到同一个值」和「忘了写」
 * 在源码上长得一模一样，无法靠阅读区分。
 *
 * 这份清单就是 check-contracts.mjs 用来逐项比对的判据；新增任何
 * 「随底色而变」的令牌时，在这里加一行即可（组件里不许按主题名分支，
 * 所以每个新增的主题相关令牌都必然出现在这里）。
 */
export const REQUIRED_TOKENS = [
  // 表面与文字
  '--color-primary',
  '--color-bg',
  '--color-fg',
  '--color-fg-shadow',
  '--color-text',
  '--color-text-secondary',
  '--color-text-placeholder',
  '--color-text-tertiary',
  '--color-border',
  '--scrollbar-thumb',
  // 语义色（填充/图表用）与正文语义色（文字用）
  '--color-red',
  '--color-orange',
  '--color-green',
  '--color-blue',
  '--color-purple',
  '--color-gray',
  '--color-icon',
  '--color-icon-hover-bg',
  '--text-green',
  '--text-amber',
  '--text-red',
  '--text-purple',
  '--text-terracotta',
  '--text-blue',
  '--text-gray',
  // 主色的几个「专门档位」：文字版、实心块对、品牌标记、提示浮层
  '--text-primary-ink',
  '--brand-mark-bg',
  '--brand-mark-fg',
  '--solid-primary-bg',
  '--solid-primary-fg',
  '--tooltip-bg',
  '--tooltip-fg',
  '--focus-ring-halo',
  // 组件档位（2026-09-30 从组件里的主题名分支收口而来）
  '--ch-icon-bg-l',
  '--ch-icon-ink-l',
  '--tag-tint-l',
  '--tag-custom-mix-color',
  '--tag-custom-mix-amount',
  '--pill-ink-mix-color',
  '--pill-ink-mix-amount',
  '--card-hover-shadow',
  '--scroll-hint-color',
  // 光标资源
  '--cursor-arrow',
  '--cursor-hand'
]

/**
 * 族档位令牌：同一个族（浅/深）内这几个值理应相同，族之间才该不同。
 * check-theme-contrast.mjs 只在族内不一致时打 INFO 提示
 * （可能是某个主题有意微调，也可能是改了一套忘了另一套）。
 */
export const FAMILY_TOKENS = [
  '--tag-tint-l',
  '--tag-custom-mix-color',
  '--tag-custom-mix-amount',
  '--pill-ink-mix-color',
  '--pill-ink-mix-amount',
  '--card-hover-shadow',
  '--scroll-hint-color'
]
