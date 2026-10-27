// 渲染期契约检查：把每个页面的 DataState 用法和组件的 props 声明对一遍。
//
// 为什么需要这个：DataState 的根节点是 Fragment（多个 v-if 分支），
// Vue 无法把未声明的属性透传到子节点，只会打印一条开发期警告 ——
// 而那条警告在构建产物里被剥掉，测试也不看控制台，于是
// ProxiesView 传的 :empty / empty-text 错了很久都没人发现（空态文案从来没显示过）。
//
// 这里直接读源码做静态比对，不依赖浏览器环境。
// 2026-09-30 起还有一节「主题契约」：注册表、theme.css、预涂清单、光标资源、
// antd 令牌映射五处的一致性都在这里比对（多主题之后靠人眼盯不住）。
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
// 主题相关：解析器与光标生成器都只有一个实现，脚本之间共用
import {
  PUBLIC_DIR,
  REQUIRED_TOKENS,
  parseInlineList,
  parseRegistry,
  parseSwatches,
  parseThemes
} from './lib/theme-css.mjs'
import { renderAll } from './gen-theme-cursors.mjs'
// 颜色解析与 WCAG 计算与 check-theme-contrast.mjs 共用一份实现（lib/color.mjs）
import { contrast, parseColor } from './lib/color.mjs'

// 用 fileURLToPath 而不是 url.pathname：后者会保留百分号编码，
// 而本仓库路径里含中文（「不忘初心」），拼出来的路径必然是 ENOENT
const SRC = join(dirname(fileURLToPath(import.meta.url)), '..', 'src')

let failed = 0
function check(name, cond, detail) {
  if (cond) {
    console.log(`  PASS  ${name}`)
  } else {
    console.log(`  FAIL  ${name}${detail ? ' —— ' + detail : ''}`)
    failed++
  }
}

/**
 * 去掉注释后的源码。
 *
 * 少数几条断言是"禁止出现某种写法"（比如禁止 `:global(A) :deep(B)`）—— 而这类坑的
 * 说明恰恰会写在旁边的注释里，把那段写法当反例抄一遍。对整份文件做正则时，
 * 注释里的反例就会被判成违规（实测踩过：注解自己把断言坑了）。
 * 所以凡是否定式断言，都先过这一道。
 *
 * keepLines = true 时**保留换行**（注释内容换成等量空格），给「按行报错」的
 * 断言用：整段删掉的话，报出来的行号指向的是剥离后的源码，拿它去原文件里找
 * 会差好几行（实测差 5~8 行，找到的是另一条规则）。判定逻辑两者一致。
 */
function stripComments(src, keepLines = false) {
  const drop = (m) => (keepLines ? m.replace(/[^\n]/g, ' ') : '')
  return src
    .replace(/\/\*[\s\S]*?\*\//g, drop)
    .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
    .replace(/<!--[\s\S]*?-->/g, drop)
}

// 同时收集 .vue 与 .ts：CSS 变量的定义有一部分在 utils/*Style.ts 里
// （通过 :style 绑定的对象字面量），只扫 .vue 会把它们误判成「未定义」。
function walk(dir, out = []) {
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (p.endsWith('.vue') || p.endsWith('.ts')) out.push(p)
  }
  return out
}

// 从组件里抽出声明的 prop 名（含 withDefaults 里的默认值对象）
function declaredProps(file) {
  const src = readFileSync(file, 'utf8')
  const block = src.match(/defineProps<\{([\s\S]*?)\}>\(\)/)
  if (!block) return new Set()
  const names = new Set()
  for (const m of block[1].matchAll(/^\s*(\w+)\??:/gm)) names.add(m[1])
  // kebab-case 写法也是同一个 prop
  const kebab = new Set()
  for (const n of names) kebab.add(n.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase()))
  return new Set([...names, ...kebab])
}

// 抽出模板里某个组件的所有使用点及其属性名
function usages(src, component) {
  const out = []
  const re = new RegExp(`<${component}\\b([\\s\\S]*?)>`, 'g')
  for (const m of src.matchAll(re)) {
    const attrs = new Set()
    const body = m[1]
    for (const a of body.matchAll(/(?:^|\s)(:?)([@a-zA-Z][\w-]*)\s*=/g)) {
      const raw = a[2]
      if (raw.startsWith('@') || raw === 'v-bind') continue
      attrs.add(raw)
      attrs.add(raw.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase()))
    }
    out.push(attrs)
  }
  return out
}

console.log('=== DataState 契约 ===')
const dsProps = declaredProps(join(SRC, 'components/DataState.vue'))
check('DataState 声明了 hasData', dsProps.has('has-data'))
check('DataState 声明了 empty', dsProps.has('empty'))
check('DataState 声明了 emptyText', dsProps.has('empty-text'))

for (const file of walk(SRC)) {
  if (file.endsWith('DataState.vue')) continue
  const src = readFileSync(file, 'utf8')
  if (!src.includes('<DataState')) continue
  const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
  for (const [i, attrs] of usages(src, 'DataState').entries()) {
    const bad = [...attrs].filter((a) => {
      const camel = a.replace(/-([a-z])/g, (_, c) => c.toUpperCase())
      return !dsProps.has(a) && !dsProps.has(camel)
    })
    check(`${rel} 第 ${i + 1} 处 DataState 的属性都有效`, bad.length === 0, bad.length ? `未声明: ${bad.join(', ')}` : '')
  }
}

// 空态必须真的可达：传了 empty 的地方要有一个能变 true 的表达式
console.log('')
console.log('=== 空态可达性 ===')
for (const file of walk(SRC)) {
  const src = readFileSync(file, 'utf8')
  if (!src.includes('<DataState')) continue
  const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
  const hasEmpty = /:empty=/.test(src)
  const hasEmptyText = /empty-text=/.test(src)
  // 传了 empty-text 却没传 empty 时，文案永远不会出现
  check(`${rel} 传了 empty-text 就应同时传 empty`, !hasEmptyText || hasEmpty, hasEmptyText && !hasEmpty ? 'empty-text 永远不会渲染' : '')
}

console.log('')
console.log('=== 主题令牌定义完整性 ===')
const theme = readFileSync(join(SRC, 'styles/theme.css'), 'utf8')
// 收集所有 --x 定义。来源有两类：
//   1. CSS 里写的 `--x: ...`
//   2. JS 里通过 :style 绑定的（例如 modelStyle.ts / groupStyle.ts 返回 { '--mt-h': ... }）
// 两类都要算，否则会把合理用法误报成「未定义」。
const defined = new Set([...theme.matchAll(/^\s*(--[\w-]+)\s*:/gm)].map((m) => m[1]))
const referenced = new Set()
for (const file of walk(SRC)) {
  const src = readFileSync(file, 'utf8')
  // 定义可以在行首，也可以跟在选择器后面（`.tone-red { --tone-color: ... }`），
  // 所以这里不锚定行首，只要前面是 `{` 或 `;` 或换行就算定义
  for (const m of src.matchAll(/(?:^|[{;\n])\s*(--[\w-]+)\s*:/gm)) defined.add(m[1])
  // JS 对象字面量里的 CSS 变量键：'--gt-h': value / "--wl-cols": value
  // 也支持函数返回样式对象的写法（见 utils/modelStyle.ts）
  for (const m of src.matchAll(/['"](--[\w-]+)['"]\s*:/g)) defined.add(m[1])
  for (const m of src.matchAll(/var\((--[\w-]+)/g)) referenced.add(m[1])
  // utils/antdTheme.ts 是「以字符串形式引用 CSS 令牌」的地方：它把 antd 的
  // token 名映射到 CSS 变量名，读取发生在运行时（getComputedStyle），
  // 源码里一个 var(...) 都没有。不数进来的话，那几个令牌会被下面的
  // 「已定义但未被引用」误报成死代码。
  if (/antdTheme\.ts$/.test(file)) {
    for (const m of src.matchAll(/'(--[\w-]+)'/g)) referenced.add(m[1])
  }
}
for (const m of theme.matchAll(/var\((--[\w-]+)/g)) referenced.add(m[1])
// utils/*Style.ts 里 key 有时由变量拼接，注释与实现里会同时出现名字。
// 这两组命名空间（--gt-* 分组、--mt-* 模型）由 JS 注入，扫一遍源文件即可。
for (const file of walk(SRC)) {
  if (!/modelStyle|groupStyle/.test(file)) continue
  const src = readFileSync(file, 'utf8')
  for (const m of src.matchAll(/(--(?:mt|gt)-[a-z]+)/g)) defined.add(m[1])
}

const missing = [...referenced].filter((r) => !defined.has(r))
check('所有 var(--token) 都有定义', missing.length === 0, missing.length ? `未定义: ${missing.join(', ')}` : '')

// 每个被引用的令牌都必须在 :root 或某个组件作用域里有定义 ——
// 反过来说，定义了却从未引用的令牌是死代码，这里只报告不判失败
// （保留一些语义化别名是合理的）。
const unused = [...defined].filter((d) => !referenced.has(d) && !d.startsWith('--tone-'))
if (unused.length) console.log(`  INFO  已定义但未被引用（可能是预留或死代码）: ${unused.join(', ')}`)

// ---- 主题契约（2026-09-30 立）----
//
// 12 套主题 × 40 多个令牌，靠人眼盯不住，而这里盯的都是「漏了就静默出错」的东西：
//   · 注册表 / CSS / 预涂清单三处 id 集合不一致 —— 某个主题选了没样式，
//     或者刷新时先闪一下别的配色（预涂脚本带的是旧清单）
//   · 某个主题块漏写令牌 —— 它会静默继承 :root（浅色）的值：不报错、不失败
//   · 色样预览与主题块颜色不一致 —— 卡片上看到的样子与点下去得到的样子不是一回事
//   · 光标与实心块配色不同步 —— 光标停在上一套主题的颜色上（相近两色很难发现）
//   · 组件里又冒出按主题名分支的选择器 —— 加一套主题就要改 N 处，漏一处没人发现
console.log('')
console.log('=== 主题契约 ===')
{
  const themes = parseThemes(theme)
  const registry = parseRegistry()
  const swatches = parseSwatches(theme)
  const inlineTones = parseInlineList()

  // 1. 三处 id 集合一致
  const cssIds = [...themes.keys()]
  const regIds = registry.map((r) => r.id)
  const missingInCss = regIds.filter((id) => !cssIds.includes(id))
  const extraInCss = cssIds.filter((id) => !regIds.includes(id))
  check(
    'theme.css 的主题块与注册表一一对应',
    missingInCss.length === 0 && extraInCss.length === 0,
    [
      missingInCss.length ? `CSS 缺: ${missingInCss.join(', ')}` : '',
      extraInCss.length ? `CSS 多: ${extraInCss.join(', ')}` : ''
    ]
      .filter(Boolean)
      .join('；')
  )
  // 0. 绝对下限。上面这些比较都是「两个集合互为子集」，两边同时失手（解析器
  //    结构变了、都返回空）时会全部零迭代、零失败 —— 看起来全绿，其实一套主题
  //    都没查过。所以先钉一个绝对数量。
  check(
    '解析到的主题块不少于 12 套，且与注册表数量一致',
    cssIds.length >= 12 && cssIds.length === regIds.length,
    `CSS ${cssIds.length} 套 / 注册表 ${regIds.length} 套`
  )
  check("浅色主题由 :root 承载，不存在 :root[data-theme='light'] 块", !theme.includes(":root[data-theme='light']"))

  // 2. 每个主题块写全必需令牌（清单在 lib/theme-css.mjs，与对比度脚本共用）
  for (const [id, t] of themes) {
    const miss = REQUIRED_TOKENS.filter((tok) => !t.vars.has(tok))
    check(
      `${id} 块写全了必需令牌（${REQUIRED_TOKENS.length} 项）`,
      miss.length === 0,
      miss.length ? `缺 ${miss.length} 项: ${miss.join(', ')}` : ''
    )
    // 反向断言：**其余主题块**里也不许出现清单之外的令牌。
    // 出现了说明「这个令牌随主题变，但没人保证另外 11 套主题也写了它」——
    // 漏写的那些块会静默继承 :root（浅色）的值，正是上面那条要防的同一个坑，
    // 只是从「清单漏登记」这一侧进来的。新令牌该做的是加进 REQUIRED_TOKENS。
    //
    // 只管非 :root 的块：:root 是基准，尺寸/字体/由其他令牌派生的别名
    // （--focus-ring、--latency-*、--color-primary-a20 等）都写在那里，
    // 它们本来就不随主题变，也不需要在 12 个块里各写一遍。
    if (id === 'light') continue
    const extra = [...t.vars.keys()].filter((tok) => !REQUIRED_TOKENS.includes(tok))
    check(`${id} 块没有清单之外的令牌`, extra.length === 0, extra.length ? `多 ${extra.join(', ')}（加进 REQUIRED_TOKENS，或改成不随主题变）` : '')
    // 块内重复定义：解析器用 Map 存令牌，后写的会静默覆盖先写的（值以最后一行为准），
    // 所以「同一块里写了两遍」这种多半是改主题时留下的残迹，必须报出来 ——
    // 按 key 比对的那些断言（缺了/多了）都看不见重复。
    const names = [...t.body.matchAll(/^\s*(--[\w-]+)\s*:/gm)].map((m) => m[1])
    const dups = [...new Set(names.filter((n, i) => names.indexOf(n) !== i))]
    check(`${id} 块内没有重复定义`, dups.length === 0, dups.length ? `重复: ${dups.join(', ')}` : '')
  }

  // 3. color-scheme 与外壳色：两处都由注册表说，但值必须真的来自 CSS
  for (const r of registry) {
    const t = themes.get(r.id)
    if (!t) continue
    check(
      `${r.id} 的 color-scheme 与注册表 tone 一致`,
      t.colorScheme === r.tone,
      `CSS ${t.colorScheme ?? '缺'} / 注册表 ${r.tone}`
    )
    const tok = r.tone === 'light' ? '--color-primary' : '--color-bg'
    const cssColor = (t.vars.get(tok) || '').toLowerCase()
    check(
      `${r.id} 注册表 shell 等于 ${tok}（浅色取主色、深色取页面底）`,
      (r.shell || '').toLowerCase() === cssColor,
      `注册表 ${r.shell} / CSS ${cssColor}`
    )
  }

  // 4. index.html 的预涂清单：它跑在打包产物之前，import 不了注册表，只能抄一份
  {
    const missing = regIds.filter((id) => !inlineTones.has(id))
    const extra = [...inlineTones].filter((id) => !regIds.includes(id))
    check(
      'index.html 预涂清单的 id 集合与注册表一致',
      missing.length === 0 && extra.length === 0,
      [
        missing.length ? `预涂缺: ${missing.join(', ')}` : '',
        extra.length ? `预涂多: ${extra.join(', ')}` : ''
      ]
        .filter(Boolean)
        .join('；')
    )
  }

  // 5. 主题卡的预览色样：写死是有意的（预览不随当前主题变），所以要盯着它与主题块一致
  for (const r of registry) {
    const t = themes.get(r.id)
    const sw = swatches.get(r.id)
    if (!t || !sw) {
      check(`${r.id} 有 [data-swatch] 预览色样`, false, t ? '缺色样' : '缺主题块')
      continue
    }
    for (const [swTok, themeTok] of [
      ['--sw-page', '--color-bg'],
      ['--sw-card', '--color-fg'],
      ['--sw-border', '--color-border'],
      ['--sw-accent', '--solid-primary-bg']
    ]) {
      const a = (sw.get(swTok) || '').toLowerCase()
      const b = (t.vars.get(themeTok) || '').toLowerCase()
      check(`${r.id} 色样 ${swTok} == 主题块的 ${themeTok}`, a === b, `色样 ${a || '缺'} / 主题块 ${b || '缺'}`)
    }
  }

  // 6. 光标：每套主题的文件名、热区、配色都要与自己的实心块对得上
  const readCursorToken = (value) => {
    const m = (value || '').match(/^url\('([^']+)'\)\s+(\d+)\s+(\d+)\s*,\s*([a-z-]+)$/)
    return m ? { url: m[1], hotspot: `${m[2]} ${m[3]}`, fallback: m[4] } : null
  }
  const KINDS = [
    { label: '箭头', tok: '--cursor-arrow', suffix: 'arrow', hotspot: '2 2', fallback: 'auto' },
    { label: '手型', tok: '--cursor-hand', suffix: 'hand', hotspot: '9 2', fallback: 'pointer' }
  ]
  for (const r of registry) {
    const t = themes.get(r.id)
    if (!t) continue
    // cursorSource 是显式共用（oled 与 dark 的实心块那一对相同），否则必须是自己那一份
    const source = r.cursorSource || r.id
    const fill = (t.vars.get('--solid-primary-bg') || '').toLowerCase()
    const stroke = (t.vars.get('--solid-primary-fg') || '').toLowerCase()
    for (const k of KINDS) {
      const tag = `${r.id} ${k.label}光标`
      const c = readCursorToken(t.vars.get(k.tok))
      if (!c) {
        check(`${tag} 的 ${k.tok} 可解析（url + 热区 + 兜底）`, false, t.vars.get(k.tok) || '缺')
        continue
      }
      check(`${tag} 指向 cursor-${k.suffix}-${source}.svg`, c.url === `/cursor-${k.suffix}-${source}.svg`, c.url)
      check(`${tag} 用根绝对路径`, c.url.startsWith('/'), c.url)
      check(`${tag} 热区是 ${k.hotspot}`, c.hotspot === k.hotspot, c.hotspot)
      check(`${tag} 兜底是 ${k.fallback}`, c.fallback === k.fallback, c.fallback)

      const file = `cursor-${k.suffix}-${source}.svg`
      let svg = null
      try {
        svg = readFileSync(join(PUBLIC_DIR, file), 'utf8')
      } catch {
        check(`${tag} 文件 ${file} 存在`, false, '读不到文件')
        continue
      }
      const attrOf = (name) => {
        const m = svg.match(new RegExp(`\\b${name}="(#[0-9a-fA-F]{3,8})"`))
        return m ? m[1].toLowerCase() : null
      }
      check(`${file} 的 fill 等于本主题实心块底色 ${fill}`, attrOf('fill') === fill, `实测 ${attrOf('fill')}`)
      check(`${file} 的 stroke 等于本主题实心块文字色 ${stroke}`, attrOf('stroke') === stroke, `实测 ${attrOf('stroke')}`)
      check(`${file} 的 fill 与 stroke 不是同一个色`, attrOf('fill') !== attrOf('stroke'))
      check(`${file} 带固有尺寸`, /<svg[^>]*\bwidth="\d+"[^>]*\bheight="\d+"/.test(svg.replace(/\s+/g, ' ')))
      const wh = svg.replace(/\s+/g, ' ').match(/<svg[^>]*\bwidth="(\d+)"[^>]*\bheight="(\d+)"/)
      check(`${file} 不超过 32x32（Firefox 上限）`, !!wh && +wh[1] <= 32 && +wh[2] <= 32, wh ? `实测 ${wh[1]}x${wh[2]}` : '读不到尺寸')
      const fills = [...svg.matchAll(/\bfill="(#[0-9a-fA-F]{3,8})"/g)]
      check(`${file} 只有一处 fill`, fills.length === 1, `实测 ${fills.length} 处`)
      const comments = svg.match(/<!--[\s\S]*?-->/g) || []
      check(`${file} 注释里没有连续减号`, !comments.some((c) => c.slice(4, -3).includes('--')))
    }
  }

  // 7. 脚本生成的光标必须与主题色同步（改主题色忘了重新生成时，这份内容会不一样）
  const generated = renderAll(theme)
  for (const [name, { content }] of generated) {
    let disk = null
    try {
      disk = readFileSync(join(PUBLIC_DIR, name), 'utf8')
    } catch {
      /* 下面的断言会报出来 */
    }
    check(`public/${name} 与主题色同步（npm run gen:cursors）`, disk === content)
  }
  // 反向：主题已经删掉、文件还留着的旧光标（只提示，不影响正确性）
  const expected = new Set(['cursor-arrow-light.svg', 'cursor-hand-light.svg', 'cursor-arrow-dark.svg', 'cursor-hand-dark.svg', ...generated.keys()])
  const orphans = readdirSync(PUBLIC_DIR).filter((f) => /^cursor-(arrow|hand)-.+.svg$/.test(f) && !expected.has(f))
  if (orphans.length) console.log(`  INFO  public/ 下有不再被任何主题使用的光标文件: ${orphans.join(', ')}`)

  // 8. antd 令牌映射读的 CSS 变量必须真的存在（否则 antd 会静默退回自己的默认色）
  const antdSrc = readFileSync(join(SRC, 'utils/antdTheme.ts'), 'utf8')
  const antdVars = [...antdSrc.matchAll(/'(--[\w-]+)'/g)].map((m) => m[1])
  check('antdTheme.ts 里映射了至少 10 个令牌', antdVars.length >= 10, `实测 ${antdVars.length} 个`)
  const antdMissing = antdVars.filter((v) => !defined.has(v))
  check('antdTheme.ts 映射的令牌都在 theme.css 里有定义', antdMissing.length === 0, antdMissing.length ? `未定义: ${antdMissing.join(', ')}` : '')

  // 9. 漂移守卫：按主题名分支的写法只允许出现在这两个文件里
  //
  // theme.css 是主题块本身（那些选择器就该长这样），theme.ts 是唯一写这个属性的地方。
  // 组件里再冒出 [data-theme=...] 就说明有人开了一条并行分支 —— 多主题下
  // 那条分支要么漏了 10 套主题，要么每加一套都要回来改，两种都会静默出错。
  const THEME_ATTR_ALLOWED = ['styles/theme.css', 'stores/theme.ts']
  for (const file of walk(SRC)) {
    const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
    if (THEME_ATTR_ALLOWED.includes(rel)) continue
    const src = stripComments(readFileSync(file, 'utf8'))
    const hit = src.match(/\[data-theme\s*[\^$*~|]?=/)
    check(`${rel} 里没有按主题名分支的选择器`, !hit, hit ? '改用主题令牌（见 theme.css 的组件档位令牌）' : '')
  }

  // 老键只该被「读一次做迁移」的地方提到：store 与它的用例（用例必须写这个字面量
  // 才测得了迁移），以及 index.html 的预涂脚本（它也要认这个键，否则首帧闪一下）。
  const legacyKeyUsers = [join(SRC, '..', 'index.html'), ...walk(SRC)].filter((f) =>
    readFileSync(f, 'utf8').includes('llm-relay-darkstyle')
  )
  check(
    '老键 llm-relay-darkstyle 只出现在迁移相关的位置（store / 它的用例 / 预涂脚本）',
    legacyKeyUsers.every((f) => /stores[\\/]theme(\.spec)?\.ts$/.test(f) || /index\.html$/.test(f)),
    legacyKeyUsers.map((f) => f.slice(SRC.length + 1)).join(', ')
  )

  // 10. 老档位迁移的**判据**必须两处一致（不只是「都提到了那个键」）。
  //
  // 预涂脚本（index.html）与 store 各写了一份这个判断，而它们必须表达同一件事：
  // 只有「上次停在深色」才迁移成 oled。写歪一边的表现是首帧一套、打包产物起来后
  // 另一套 —— 正是那一段注释声称要避免的跳变，而它只在刷新的一瞬间可见。
  // 比对方式：取出各自那行判断，把变量名抹掉（saved / stored 叫什么无所谓），
  // 只比「出现了哪些字面量」与「&&/||/=== 各几个」。
  {
    const ruleOf = (src) => {
      const line = src.split('\n').find((l) => l.includes("'oled'") && l.includes('===') && l.includes('&&'))
      if (!line) return null
      const lits = [...line.matchAll(/'([^']+)'/g)].map((m) => m[1]).sort().join(',')
      const count = (sub) => line.split(sub).length - 1
      return `${lits}|&&${count('&&')}|${count('||')}|||${count('===')}`
    }
    const a = ruleOf(readFileSync(join(SRC, '..', 'index.html'), 'utf8'))
    const b = ruleOf(readFileSync(join(SRC, 'stores/theme.ts'), 'utf8'))
    check(
      '预涂脚本与 store 的老档位迁移判据同形（变量名可不同）',
      !!a && a === b,
      `index.html ${a ?? '找不到'} / theme.ts ${b ?? '找不到'}`
    )
  }
}


console.log('')
console.log('=== 新日志入场动效契约 ===')
{
  // 这套效果只在「实时推送插进来一行」时触发：绑定被删掉、类名被改名、
  // 关键帧被删、档位被改名，都不会报错也不会失败，只是那一行不再有动静 ——
  // 没人会收到通知。所以静态盯住这几件事。
  //
  // 2026-09-17：从「一道彩虹扫光」扩成「三档可切换」，
  // 实体（效果层）从 RequestLogPanel.vue 搬去了 components/NewLogEffect.vue，
  // 所以下面这些检查也分了两个文件看：面板负责触发与量位置，效果层负责画。
  //
  // 同日站主原本还想要一档「卡通猫趴在表头上扒拉」，做到一半决定不做，
  // 相关组件（NappingCat / useDraggableCat）已整体删除 —— 要恢复看 git 历史，
  // 那套代码当时的契约检查也一并在历史里。
  const panel = readFileSync(join(SRC, 'components/RequestLogPanel.vue'), 'utf8')
  const fxComp = readFileSync(join(SRC, 'components/NewLogEffect.vue'), 'utf8')
  const preview = readFileSync(join(SRC, 'components/LogFxPreview.vue'), 'utf8')
  const fxUtil = readFileSync(join(SRC, 'utils/effects.ts'), 'utf8')
  const fxStore = readFileSync(join(SRC, 'stores/logFx.ts'), 'utf8')

  check('请求日志表格绑定了 row-class-name', /<a-table[\s\S]*?:row-class-name="rowClassName"/.test(panel))
  check('行 class 里带 is-new', /['"]is-new['"]/.test(panel))
  check('样式里定义了 @keyframes row-sweep', /@keyframes\s+row-sweep\b/.test(fxComp))
  check('扫光是表格外那一层里的 .log-sweep', /class="log-table"/.test(panel) && /\.log-sweep\s*\{/.test(fxComp))
  check(
    '扫光带的位置由 JS 量出来（top/left/width 都绑上了）',
    /:style="\{[^}]*b\.top[^}]*b\.left[^}]*b\.width/.test(fxComp) &&
      /fxTargets\.value\.push\(\{ key: id,[^}]*top:[^}]*left:[^}]*width:/.test(panel),
    '效果层画位置、面板量位置，两边缺一处亮带就会画在原点',
  )
  // 回归闸：第一版把扫光画在 tr 的 ::after 上，结果在宽窗口下整张表的列宽会塌回
  // 声明宽度、右侧空出一条（tr 里出现非单元格子元素后，Chrome 不再按 fixed 布局
  // 分配多余宽度）。这个坑很容易「顺手」再踩一次 —— 比如为了少写几行 JS 又把
  // 伪元素挂回 tr —— 所以这里直接禁掉那种写法。三档都走同一层，检查覆盖新组件。
  for (const [name, src] of [
    ['RequestLogPanel', panel],
    ['NewLogEffect', fxComp],
  ]) {
    check(
      `${name} 没有把样式挂回 tr.is-new（否则列宽会塌、右侧空一条）`,
      !/:deep\(\.ant-table-tbody\s*>\s*tr\.is-new\)/.test(src) &&
        !/tr\.is-new\s*::after/.test(src) &&
        !/tr\.is-new\s*::before/.test(src),
      '往 tr 里加伪元素会让 table-layout: fixed 不再分配多余宽度，整表塌回声明宽度',
    )
  }
  // 动 background-position 而不是元素 transform：后者会撑大容器的可滚动溢出区
  const sweep = fxComp.slice(fxComp.indexOf('@keyframes row-sweep'))
  check('扫光动的是 background-position-x', /background-position-x:\s*-50%/.test(fxComp) && /background-position-x:\s*150%/.test(sweep.slice(0, 200)))
  check('扫光那一层裁掉溢出，亮带不会撑出滚动条', /\.log-table\s*\{[\s\S]{0,220}?overflow:\s*hidden/.test(panel))

  // ---- 档位一致性：真源是 utils/effects.ts 的 LOG_FX_OPTIONS ----
  //
  // 为什么值得钉：档位 id 是一串普通字符串，散在「注册表 / 面板的 data-fx /
  // 效果层的 CSS 选择器 / 设置页的预览」四处。任何一处写错都**不会报错** ——
  // 只是那一档选上去以后什么都不动，或者在设置页上是一张空白预览。
  // 三处集合必须与真源完全相等（不是"包含"：多出来的 id 同样说明有地方漂了）。
  const declared = [...fxUtil.matchAll(/^\s*id:\s*'([\w-]+)',/gm)].map((m) => m[1]).sort()
  check('从 effects.ts 读到了三档 id', declared.length === 3, `实际读到 ${declared.join(', ')}`)

  const dataFx = [...panel.matchAll(/:data-fx="([\w.]+)"/g)].map((m) => m[1])
  check('面板把当前档位写到 data-fx 上', dataFx.length === 1 && dataFx[0] === 'logFx.fx', `实际: ${dataFx.join(', ')}`)
  const selectors = [...fxComp.matchAll(/\.log-table\[data-fx='([\w-]+)'\]/g)].map((m) => m[1]).sort()
  check(
    '效果层里每一条 data-fx 选择器都是已声明的档位',
    selectors.every((s) => declared.includes(s)),
    `选择器 ${selectors.join(', ')} vs 已声明 ${declared.join(', ')}`,
  )
  check('设置页的档位来自 store（而不是自己抄一份）', /logFx\.options/.test(readFileSync(join(SRC, 'views/SettingsView.vue'), 'utf8')) && /LOG_FX_OPTIONS/.test(fxStore))
  // 预览里认档位有两种写法：v-if 直接比（sweep / glow 那两档各有一块自己的图形），
  // 以及舞台根上的 :data-fx（slide 那一档没有单独的图形，只靠它选中行）。
  // 两种都要收进来，否则「某档预览是空白的」这条闸就漏了。
  const previewIds = [
    ...preview.matchAll(/fx === '([\w-]+)'/g),
    ...preview.matchAll(/data-fx="fx"/g),
    ...preview.matchAll(/data-fx='([\w-]+)'/g),
    ...preview.matchAll(/\[data-fx='([\w-]+)'\]/g),
  ]
    .map((m) => m[1])
    .filter(Boolean)
    .sort()
  check(
    '设置页三张卡片的预览都画得出来',
    declared.every((d) => previewIds.includes(d)),
    `预览里出现的档位: ${[...new Set(previewIds)].join(', ')}`,
  )

  // ---- 硬约束：动效层不许吃掉表格的点击 ----
  //
  // 这一层铺在表格上面，只要有一处漏掉 pointer-events: none，表头或某一行就点不动了。
  // （曾经这一层里还有一块能吃事件的"猫道"，猫删掉之后整层彻底不参与命中测试，
  // 所以现在只钉这一条 —— 它是这一层唯一可能咬人的地方。）
  check('效果层根节点不吃事件', /\.fx-layer\s*\{[\s\S]{0,160}?pointer-events:\s*none/.test(fxComp))

  // ---- 行内动画的选择器写法 ----
  //
  // `:global(A) :deep(B)` 这种组合会被 scoped 编译器**丢掉后半截**：实测编译产物
  // 只剩 `.log-table[data-fx="slide"]`，规则落到 tr 上 —— 而 transform /
  // background-image 挂在 tr 上基本看不出效果，两档动画静默失效、不报任何错。
  // 正确写法是把整条选择器包进 :global(...)。这条闸钉的就是那个写法。
  //
  // 只看真正生效的那部分（去掉注释）：这段坑的说明本身就写在旁边的注释里，
  // 连注释一起正则会把示例也判成违规（第一版就是这样误报的）。
  const fxCode = stripComments(fxComp)
  check(
    '行内动画的选择器整条包在 :global(...) 里（:global + :deep 组合会丢后半截）',
    !/:global\([^)]*\)\s*:deep\(/.test(fxCode),
    ':global(A) :deep(B) 编译后只剩 A，规则会落到 tr 而不是 td 上',
  )
  // 2026-09-18 换血之后（slide/glow 退役，pulse/stardust 上岗）三档**全部**
  // 画在效果层里，任何一档都不再有「往单元格上挂动画」的实现方式 ——
  // 当年 :global/:deep 那个坑正是从行内动画长出来的，现在的契约因此升级成
  // 「不许再有行内动画档位」：效果层里不允许出现选 tr.is-new 的选择器。
  check(
    '入场动效全部画在效果层（不再有依赖行内动画的档位）',
    !/data-fx='[\w-]+'\]\s+tr\.is-new/.test(fxCode) && !/data-fx="[\w-]+"\]\s+tr\.is-new/.test(fxCode),
    '行内动画的选择器写法是当年 :global/:deep 静默失效的温床，新档一律走效果层',
  )
  // 新两档的元素都是 .fx-layer 里 v-for 出来的 span（与扫光同一套定位数据）：
  // 类名必须出现在效果层根节点**之后**，防止将来有人绕过效果层往表格里塞动画节点。
  const iLayer = fxComp.indexOf('class="fx-layer"')
  for (const cls of ['fx-pulse', 'fx-star']) {
    const iCls = fxComp.indexOf(`class="${cls}"`)
    check(
      `「${cls}」元素挂在效果层模板里（不是表格内部）`,
      iLayer >= 0 && iCls > iLayer,
    )
  }
}

console.log('')
console.log('=== 多排数值的列左缘对齐契约 ===')
{
  // 「词元」「任务耗时」两格是块级 grid + justify-content: center：居中的是轨道，
  // 轨道宽度一旦写成 auto，就会跟着数值长短伸缩，整块宽度逐行不同 —— 绿色竖条与
  // 图标于是每行落在不同的 x 上（站主原话「强迫症受不了」）。
  // 改回 auto 不会报错、不会崩，只会让那一列看着毛糙，所以静态盯住。
  const panel = readFileSync(join(SRC, 'components/RequestLogPanel.vue'), 'utf8')
  const block = (sel) => {
    const i = panel.indexOf(sel)
    return i < 0 ? '' : panel.slice(i, panel.indexOf('}', i))
  }
  const dur = block('.dur {')
  const tk = block('.token-cell {')
  check('任务耗时那格是块级 grid + 居中', /display:\s*grid/.test(dur) && /justify-content:\s*center/.test(dur))
  check(
    '任务耗时的轨道宽度是定值（不是 auto）',
    /grid-template-columns:\s*\d+px\s+minmax\(\s*\d+px\s*,\s*auto\s*\)/.test(dur),
    'auto 轨道会跟着数值长短伸缩，竖条每行落在不同 x 上',
  )
  check('词元那格是块级 grid + 居中', /display:\s*grid/.test(tk) && /justify-content:\s*center/.test(tk))
  check(
    '词元的轨道宽度是定值（不是 auto）',
    /grid-template-columns:\s*minmax\(\s*\d+px\s*,\s*auto\s*\)/.test(tk),
    'auto 轨道会跟着数值长短伸缩，内容块左缘每行落在不同 x 上',
  )
  // 定值必须装得进「列最窄时」的可用宽度：窗口出现横向滚动时列回到声明宽度，
  // 任务耗时 120-16=104px、词元 150-16=134px。词元第一版取 136px 就在这里溢出了。
  const durBar = Number((dur.match(/grid-template-columns:\s*(\d+)px/) || [])[1])
  const durPx = Number((dur.match(/minmax\(\s*(\d+)px/) || [])[1])
  const tkPx = Number((tk.match(/minmax\(\s*(\d+)px/) || [])[1])
  check(
    '任务耗时定宽装得进最窄列（竖条 + 间隔 + 定值 ≤ 104）',
    durPx > 0 && durBar > 0 && durBar + 6 + durPx <= 104,
    `竖条 ${durBar}px + 间隔 6px + 定值 ${durPx}px = ${durBar + 6 + durPx}px，上限 104px`,
  )
  check('词元定宽装得进最窄列（定值 ≤ 134）', tkPx > 0 && tkPx <= 134, `定值 ${tkPx}px，上限 134px`)

  // 竖条断成两段、段色跟数值同源：
  //   1) 段色必须用 currentColor 继承，不能写死某个颜色变量 ——
  //      写死就回到「一整条单色」，看不出慢在首字还是慢在生成；
  //   2) 圆角加在**每一段自己**上，整条不再有圆角、也不再裁剪溢出。
  //      2026-09-24 改版前是反过来的（整条圆角 + overflow: hidden、两段紧贴），
  //      当时断言「两段各自加圆角会让交界处收窄」——那个理由基于旧形状成立，
  //      但旧形状本身有个更严重的问题：两段等高紧贴共用一个圆角外壳，
  //      视觉上就是一根**被分成两截的比例条**，而它根本不是比例条
  //      （段高恒为 1fr，与数值大小无关）。站主与审评者都读错过。
  //      所以先改形状（两枚独立短标），再把这条断言反向：
  //      不再是「圆角只在整条上」，而是「圆角只在段上、条上不许有」。
  //      这条与 .shots/verify-dur.mjs 的几何断言互为一静一动。
  //   3) 两段必须分别取首字/耗时的档位，不能都取同一个数。
  const bar = block('.dur-bar {')
  const seg = block('.dur-bar i {')
  check('竖条是两行的 grid（两段各占一行）', /display:\s*grid/.test(bar) && /grid-template-rows:\s*1fr\s+1fr/.test(bar))
  check(
    '竖条两段的颜色继承 currentColor',
    /background:\s*currentColor/.test(seg),
    '写死颜色会让整条只有一个颜色，看不出是哪一段慢',
  )
  check(
    '竖条圆角只加在每一段上（两枚独立短标，不是一条比例条）',
    !/border-radius/.test(bar) && /border-radius/.test(seg),
    '整条带圆角 + overflow:hidden 会让两段连成一条、被读成「首字占比」',
  )
  // 缝是「两枚短标」与「一条比例条」的唯一形态差别：没有 row-gap，
  // 两段又会贴在一起，而颜色/对位全部不变，上面两条照样通过。
  check(
    '两段之间有缝（否则又连成一条）',
    /row-gap:\s*[1-9]/.test(bar),
    '缺 row-gap 时两段紧贴，形态退回旧的比例条',
  )
  const segs = panel.match(/<i\s+:class="latencyClass\(record\.\w+,\s*'(first|total)'\)"\s*\/>/g) || []
  check(
    '竖条两段分别取「首字」「耗时」的档位',
    segs.length === 2 && /'first'/.test(segs[0]) && /'total'/.test(segs[1]),
    `实际取到 ${segs.length} 段：${segs.join(' ')}`,
  )
  // 两根竖条必须各自跟着自己那一行：首字用 first 档、耗时用 total 档，
  // 两行都传 'total'（或都不传）会让首字用上宽松的那把尺子
  check(
    '首字那一行两处都按 first 档（文字与竖条同档）',
    (panel.match(/latencyClass\(record\.first_byte_ms,\s*'first'\)/g) || []).length === 2,
  )
  check(
    '耗时那一行两处都按 total 档',
    (panel.match(/latencyClass\(record\.total_ms,\s*'total'\)/g) || []).length === 2,
  )

  // 阈值是站主定的，写在函数里而不是散在模板各处的三元表达式里。
  // 2026-09-27：latencyClass / fmtMs 随耗时家族抽去 components/latency.ts
  //（单元测试 latency.spec.ts 也锚在那里），这条契约跟着搬 —— 阈值锚的是
  // 函数体，函数住哪儿锚哪儿。上面几条模板断言仍盯面板（调用点没动）。
  const lat = readFileSync(join(SRC, 'components/latency.ts'), 'utf8')
  check('首字档位是 10s / 30s', /first'\s*\?\s*\[10000,\s*30000\]/.test(lat))
  check('耗时档位是 20s / 60s', /\[20000,\s*60000\]/.test(lat))
  // 两位小数：秒一律 toFixed(2)，站主明确要求「保留两位并且补齐两位」
  check(
    '耗时秒值补齐两位小数',
    /\(v \/ 1000\)\.toFixed\(2\)/.test(lat),
    '位数不齐时小数点不在同一列上，扫一列数字要重新找基准',
  )
}

// 列表与工具栏筛选**共用**（2026-10-26 站主要求，反转 2026-09-16 的决定）。
//
// 历史：2026-09-16 起列表恒定全量最新（「盯着最新发生了什么」），
// 契约当时钉的是「不许共用」；2026-10-26 站主要求把请求日志纳入筛选 ——
// 同一页上「卡片筛了 A、列表显示 B」的两套口径才是真正要消灭的东西。
// 「盯最新」的用途由默认视角承担（今天 + 全部，即未筛选时的行为）。
//
// 两条都钉：面板必须声明这四个筛选 prop 并进查询串（漏了任何一个是静默失效 ——
// 卡片在筛、列表没筛，数字对不上还看不出原因）；「筛选只作用于上方卡片」
// 的旧提示仍然禁止出现（列表也在筛了，那句话已经变成错误描述）。
console.log('')
console.log('=== 请求日志列表与工具栏共用筛选 ===')
{
  const panelPath = join(SRC, 'components/RequestLogPanel.vue')
  const panel = readFileSync(panelPath, 'utf8')
  const dash = readFileSync(join(SRC, 'views/DashboardView.vue'), 'utf8')
  const panelProps = declaredProps(panelPath)

  const filterProps = ['range', 'channelId', 'model', 'keyId']
  const declared = filterProps.filter((p) => panelProps.has(p))
  check(
    '面板声明全部四个筛选 props（时间范围 / 渠道 / 模型 / 密钥）',
    declared.length === filterProps.length,
    `缺少: ${filterProps.filter((p) => !panelProps.has(p)).join(', ')}`,
  )
  const referenced = filterProps.filter((p) => !new RegExp(`props\\.${p}\\b`).test(panel))
  check('面板里四个筛选 props 都被引用（进了 buildParams 或 watch）', referenced.length === 0, referenced.join(', '))

  // 查询串必须带上四个筛选 + 分页 + 两个排障深链。用参数名而不是
  // 「有没有 if」来判：少传一个但换成别的方式塞进去（比如拼在 URL 上）同样要拦住。
  //
  // 2026-09-24：sort 曾进白名单（P1-9 的服务端排序）；2026-10-27 站主要求
  // 移除表头排序按钮，排序功能整体下掉，于是 sort 从白名单里移出 ——
  // 这条断言是**正向白名单**，`sort` 若再被加回来会立刻失败。
  // 2026-10-26：channel_id / key_id / model / range 进白名单（列表纳入筛选）。
  // 这条断言的用意是**别的一律不许出现**。
  const bpStart = panel.indexOf('function buildParams')
  const bp = panel.slice(bpStart, panel.indexOf('\n}', bpStart))
  const sent = [...bp.matchAll(/params\.set\(\s*'([\w-]+)'/g)].map((m) => m[1]).sort()
  check(
    '查询串 = 分页 + 四个筛选 + 两个排障深链（排序已移除）',
    sent.join(',') === 'channel_id,key_id,model,page,page_size,range,status_class,trace_id',
    `实际发出: ${sent.join(', ')}`,
  )

  // 两个深链必须留着 —— 「点日志行看整条链路」与「只看失败」
  // 的入口靠它，删掉的话排障深链会静默失效（页面上看不出来）
  check(
    '两个排障深链仍然进查询串（trace_id / status_class）',
    /params\.set\('trace_id'/.test(bp) && /params\.set\('status_class'/.test(bp),
  )

  // 看板那边必须把四个条件传下去：面板声明了却不传，只会被当成透传属性，
  // 既不报错也不生效，是最难发现的一种「改了没反应」
  const tagStart = dash.indexOf('<RequestLogPanel')
  const tag = dash.slice(tagStart, dash.indexOf('/>', tagStart))
  const passed = filterProps.filter((p) =>
    !new RegExp(`:${p.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase())}=`).test(tag),
  )
  check('看板把四个筛选条件传给列表', passed.length === 0, `缺少: ${passed.join(', ')}`)

  // 旧提示（「筛选只作用于上方卡片」）仍然禁止：列表现在也在吃筛选，
  // 那句话从「过期描述」变成了「错误描述」。
  check(
    '工具栏不得出现「筛选只作用于上方卡片」提示',
    !/筛选只作用于上方卡片/.test(dash),
    '列表已纳入筛选，该提示是错的；若要改回「列表不吃筛选」，请连同本契约一起反转',
  )
}

// 看板工具栏不许参与弹性压缩（2026-09-17 站主反馈）。
//
// 为什么值得钉：看板整页锁高（路由 meta.fill → MainLayout 的
// .content-inner.is-fill），工具栏是看板 .dashboard 这个纵向弹性容器的子项。
// 弹性子项默认 flex-shrink: 1，窗口一矮就按比例被压缩 —— 而 min-height
// 只有 42px，比内容实际需要的 50px（32px 控件 + 上下各 8px 内边距 +
// 上下各 1px 边框）还小，于是内容比盒子高、只能朝下溢出，控件贴到面板底边。
// 站主原话：「浏览器窗口高度不高时，就会出现纵向居中没有正常居中，
// 而是贴到了底部边缘的问题」。
//
// 这个坑的特点是**只在矮窗口下出现**，常规视口（977 高）截图完全正常，
// 所以肉眼看静态截图、跑常规尺寸的回归都发现不了它；
// 而 `flex: 0 0 auto` 这行又是最容易被"顺手"删掉的那一类
// （看起来像没用的冗余声明）。删掉不报错、不失败，只是窗口一矮就回到贴底边。
console.log('')
console.log('=== 看板工具栏不吃弹性压缩 ===')
{
  const toolbar = readFileSync(join(SRC, 'components/PageToolbar.vue'), 'utf8')
  const start = toolbar.indexOf('.toolbar-panel {')
  // 取整条规则：从选择器到配对的右花括号（规则里没有嵌套，第一个 \n} 即结尾）
  const rule = start >= 0 ? toolbar.slice(start, toolbar.indexOf('\n}', start)) : ''
  check('PageToolbar 里有 .toolbar-panel 规则', rule.length > 0)

  // flex: 0 0 auto（简写）或显式 flex-shrink: 0 都算，但必须有一条
  const noShrink = /flex:\s*0\s+0\s+auto/.test(rule) || /flex-shrink:\s*0/.test(rule)
  check(
    '工具栏声明了不可压缩（flex: 0 0 auto 或 flex-shrink: 0）',
    noShrink,
    '缺少它时窗口一矮工具栏就被压到 min-height，内容溢出到面板底边（站主反馈的那个现象）',
  )

  // min-height 只能是「下限」，绝不能被当成实际高度来用：
  //   * 写死 height 会锁死高度 —— 窄屏换行（实测 1100px 宽 3 行 = 88px）时
  //     内容会溢出面板，等于把站主那个 bug 换个方向又犯一遍；
  //   * 靠把 min-height 调到刚好够单行（例如 50px）也只是把失效点推到更窄的
  //     屏幕：换行后照样不够。
  // 所以这里钉住「没有 height」，而不是去比对某个具体像素 ——
  // 静态检查量不到真实内容高度，拿主题令牌比会得出「42 > 24 通过」这种
  // 看着绿、其实没验证到东西的结论（本项目不吃这种虚假信心）。
  check(
    '工具栏没有写死 height（换行后高度要能自己长）',
    !/(^|[;{\s])height:\s*\d/.test(rule),
    '写死高度后窄屏换行会溢出；正确做法是 flex: 0 0 auto 让它按内容占高',
  )
}

console.log('')
console.log('=== 加载态不会被静默重取卡死 ===')

// loading 只由非静默请求开关（第三轮审查 F-中2）。
//
// 这两个 load 都带「只有最后一次请求作数」的序号守卫，而静默重取（实时推送
// 那一路）会把序号顶掉。熄灯条件若也写成 `!silent && seq === loadSeq`，被顶掉的
// 那次非静默请求就既不落数据、也不熄灯 —— 屏幕永远停在加载态，而且 antd 的
// loading 按钮会把点击一起吃掉，用户连「刷新」都按不动，只能刷新整个页面。
//
// 静态检查不还原时序，只钉住这个结构性要求：非静默请求无条件收自己的尾，
// 静默那一路完全不碰 loading。
for (const rel of ['components/RequestLogPanel.vue', 'views/DashboardView.vue']) {
  const src = stripComments(readFileSync(join(SRC, rel), 'utf8'))
  check(
    rel + ' 的 loading 只由非静默请求收尾',
    /if \(!silent\) loading\.value = false/.test(src) &&
      !/!silent && seq === loadSeq\) loading\.value = false/.test(src),
    '被静默重取顶掉的那次非静默请求不熄灯，加载态就永远停着（loading 按钮还会吃掉点击）',
  )
}

console.log('')
console.log('=== 渠道表单不拿过期快照覆盖 extra_config ===')

// extra_config 在保存时是**整块覆盖**写的（后端只看到你提交的那份 JSON）。
// 于是「基底从哪来」就成了正确性问题：拿编辑弹窗打开时的快照当基底，
// 期间由别处写进去的键就会在保存时被静默删掉。
//
// 2026-09-20 实测踩到：用户配好的默认模型映射「过一会儿自己没了」，
// 根因是 openEdit 用的还是「那条渠道还没配兜底」的旧快照，保存时
// buildExtraConfig 的 else 分支把 default_model 两个键 delete 了。
// 而且它**不是一次性**的：每次编辑任何渠道都可能重演，因为快照一直在过期。
//
// 静态检查量不到运行时的新旧，但能钉住「有没有做校正」这个结构性要求：
// openEdit 里必须有一次拉取最新渠道数据的动作。
{
  const src = stripComments(readFileSync(join(SRC, 'views/ChannelsView.vue'), 'utf8'))

  // 抽出 openEdit 的函数体（从它声明到下一個顶层函数声明之间）
  const start = src.indexOf('async function openEdit')
  const body = start >= 0 ? src.slice(start, src.indexOf('\nasync function', start + 10)) : ''

  check(
    'openEdit 找得到',
    start >= 0,
    '改过函数名就要同步改这里，否则下面两条会静默失效（空字符串永远不匹配）',
  )
  check(
    'openEdit 会拉取最新渠道数据校正表单',
    /api\.get<\{[^}]*items:\s*ChannelRow\[\][^}]*\}>\('\/channels'\)/.test(body),
    'extra_config 是整块覆盖写的，用过期的 row 当基底会静默删掉期间新增的键',
  )
  check(
    'openEdit 用拉到的结果校正 extra_config 相关字段',
    /form\.default_model(_enabled)?\s*=/.test(body) && /form\.max_concurrency\s*=/.test(body),
    '拉到了却不回填，慢性的键照样会在保存时被删掉',
  )
  // 竞态防护（第三轮审查 F-中1）：静态检查量不到时序，但能钉住「异步回填之前
  // 有没有先判断这次结果是否已过期」这个结构。快点点两条渠道时，先发出的响应
  // 会把后一条的 editing/form.models 覆盖掉 —— 用户对着 B 编辑，保存下去的是
  // A 的白名单与兜底设置，而且保存成功、表面上毫无异样
  check(
    'openEdit 的异步回填带序号守卫',
    /const seq = \+\+openSeq/.test(body) && /if \(seq !== openSeq\) return/.test(body),
    '少了作废判断，先发出的响应会盖掉后打开的渠道，保存即用错的白名单覆盖',
  )
  check(
    'openCreate 也会作废路上未返回的回填',
    /openSeq\+\+/.test(src.slice(src.indexOf('function openCreate'), src.indexOf('async function openEdit'))),
    '新建表单被上一个渠道的回填填上，等于用旧渠道的白名单去建新渠道',
  )
  // buildExtraConfig 必须是「接收基底」的纯函数，而不是自己去读 editing.value ——
  // 后者就是这次缺陷的形状（它悄悄依赖一个可能过期的 ref）
  check(
    'buildExtraConfig 的基底由参数传入，不自己读 editing.value',
    /function buildExtraConfig\(\s*base\s*:/.test(src) &&
      !/function buildExtraConfig[\s\S]{0,400}editing\.value/.test(src),
    '从 editing.value 里取基底等于依赖一个可能过期的 ref，正是这次缺陷的根因',
  )
}

console.log('')
console.log('=== 定价边界前后端同口径 ===')

// 倍率上限在前端（ModelPricingEditor 的 MAX_MULTIPLIER）与后端
// （pricing.MaxMultiplier）各存一份 —— 数量级参数没法自然共享，但可以锁住
// 它们相等：前端放行而后端拒绝，用户看到的是「填得进去、保存报错」，
// 而且报错指向的是别的格子，很难自查。
//
// 这里是静态比对源码常量，不依赖构建产物；后端文件缺失（只 checkout 了
// frontend 的场合）时跳过而不是判失败。
const pricingGo = join(SRC, '..', '..', 'backend', 'internal', 'pricing', 'peak.go')
let priceBoundChecked = false
try {
  const goSrc = readFileSync(pricingGo, 'utf8')
  const goMax = goSrc.match(/const\s+MaxMultiplier\s*=\s*([\d.]+)/)
  const editorSrc = readFileSync(join(SRC, 'components/ModelPricingEditor.vue'), 'utf8')
  const tsMax = editorSrc.match(/export\s+const\s+MAX_MULTIPLIER\s*=\s*([\d.]+)/)
  if (goMax && tsMax) {
    priceBoundChecked = true
    check(
      `倍率上限前后端一致（${goMax[1]}）`,
      Number(goMax[1]) === Number(tsMax[1]),
      `后端 pricing.MaxMultiplier=${goMax[1]}，前端 MAX_MULTIPLIER=${tsMax[1]}`,
    )
    // 编辑器里不应再出现裸的 100 当倍率上限（集中在 MAX_MULTIPLIER 一处）
    check(
      '倍率上限没有散落在编辑器里硬编码',
      !/m\s*>\s*100|倍率[^"'`]*100/.test(stripComments(editorSrc)),
      '写死的 100 会在上限调整时被漏改，表现为前后端口径不一致',
    )

    // 倍率精度（2026-09-24）：前端 MULTIPLIER_PRECISION 必须与数据库列的
    // 小数位数一致。这一列是 numeric(10,4) —— 前端若放开到 5 位，
    // 输入框收下 0.18751、落库被截成 0.1875，用户看到的是「我明明填了五位」
    // 而账单按四位算；反过来前端若收窄回 2 位，就又会把 0.1875 舍成 0.19
    // （这正是本次要修的问题）。
    //
    // 钉在**实体定义**上而不是迁移语句上：numeric(10,4) 在两个地方出现
    //（GORM tag 与 ALTER TABLE），实体那份才是常态声明。
    const tsPrec = editorSrc.match(/export\s+const\s+MULTIPLIER_PRECISION\s*=\s*(\d+)/)
    const entityGo = join(SRC, '..', '..', 'backend', 'internal', 'model', 'entities.go')
    const entitySrc = readFileSync(entityGo, 'utf8')
    const colPrec = entitySrc.match(/column:multiplier;type:numeric\(\d+,(\d+)\)/)
    if (tsPrec && colPrec) {
      check(
        `倍率精度与数据库列一致（${colPrec[1]} 位小数）`,
        Number(tsPrec[1]) === Number(colPrec[1]),
        `channel_models.multiplier 是 numeric(…,${colPrec[1]})，前端 MULTIPLIER_PRECISION=${tsPrec[1]}`,
      )
    }
    // 输入框不能再写死 2 位：那正是「填不进 0.1875」的原因，
    // 而且 a-input-number 是失焦即舍入 —— 悄悄改掉用户的值
    check(
      '倍率输入框不再硬编码 2 位精度',
      !/:precision="2"/.test(editorSrc),
      'precision 写死 2 会让 0.1875 在失焦时被舍成 0.19',
    )
  }
} catch {
  // 后端源码不在本地：跳过这段（CI 里前后端一起 checkout，会真正跑到）
}
if (!priceBoundChecked) console.log('  SKIP  未找到后端 pricing/peak.go，跳过')

console.log('')
console.log('=== 思考档位词表前后端一致 ===')

// 档位词表（off/auto/minimal/low/medium/high/xhigh/max）在两处各存一份：
// 后端 convert.AllEfforts（/v1/models 的 supported_efforts 与转发映射的尺子）
// 与前端 components/efforts.ts 的 EFFORT_LEVELS（白名单编辑器的下拉）。
//
// 为什么不能共享：档位常量在 relay/convert 包里，tygo 只生成 internal/model
// 的实体类型，取不到它们。前端多一档或少一档的表现是「下拉里选不了某个
// 上游明明支持的档位」，或者反过来「选了它、后端归一后丢掉」—— 两种都
// 只在界面上看不出来，所以用静态比对钉住。
{
  const effortsGo = join(SRC, '..', '..', 'backend', 'internal', 'relay', 'convert', 'thinking_map.go')
  try {
    const goSrc = readFileSync(effortsGo, 'utf8')
    const declared = goSrc.match(/var\s+AllEfforts\s*=\s*\[\]string\{([^}]+)\}/)
    const tsSrc = readFileSync(join(SRC, 'components/efforts.ts'), 'utf8')
    const tsList = tsSrc.match(/EFFORT_LEVELS\s*=\s*\[([^\]]+)\]/)
    if (declared && tsList) {
      // Go 侧写的是常量名（effortOff …），TS 侧写的是字面值 —— 先把常量名
      // 按同一文件里的 `effortXxx = "值"` 定义翻译成字面值再比
      const consts = new Map(
        [...goSrc.matchAll(/^\s*(effort\w+)\s*=\s*"([^"]+)"/gm)].map((m) => [m[1], m[2]]),
      )
      const goList = declared[1]
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
        .map((name) => consts.get(name))
      const tsValues = [...tsList[1].matchAll(/'([^']+)'/g)].map((m) => m[1])
      check(
        `档位词表一致（${goList.join('/')}）`,
        goList.length > 0 && goList.join(',') === tsValues.join(','),
        `后端 convert.AllEfforts=${goList.join('/')}，前端 EFFORT_LEVELS=${tsValues.join('/')}`,
      )
    }
  } catch {
    // 后端源码不在本地：跳过（CI 里前后端一起 checkout，会真正跑到）
    console.log('  SKIP  未找到后端 convert/thinking_map.go，跳过')
  }
}

console.log('')
console.log('=== 请求日志列宽：测量锚点与模板必须对得上 ===')

// 列宽现在由脚本按内容量出来（RequestLogPanel 的 remeasureColumns），量的对象是
// 每列一个固定的锚点元素（CONTENT_MEASURE 里的选择器）。
//
// 锚点选择器与模板是**两份靠字符串耦合的代码**：模板里改了类名、或者元素被删掉，
// querySelectorAll 就返回空集，代码走 `if (max === 0) continue` 那条「本页该列
// 没有锚点」的正常分支 —— 那一列的宽度于是永远停在下限，长内容照旧被截断，
// 而没有任何报错。这正是契约检查该管的一类事。
{
  const panelSrc = readFileSync(join(SRC, 'components/RequestLogPanel.vue'), 'utf8')
  const measureBlock = panelSrc.match(/const CONTENT_MEASURE[\s\S]*?\n\}/)
  check('CONTENT_MEASURE 声明找得到', !!measureBlock)
  if (measureBlock) {
    const sels = [...measureBlock[0].matchAll(/sel:\s*'([^']+)'/g)].map((m) => m[1])
    check('锚点选择器不是空的', sels.length >= 6, `读到 ${sels.length} 个`)
    const tpl = panelSrc.slice(panelSrc.indexOf('<template>'))
    // .key-tag 是传给子组件 GroupTag 的 class（它自己渲染成 <span class="group-tag
    // key-tag">），不在本文件的模板文本里 —— 对它改为断言「那个组件确实带了这个类」。
    // 2026-09-23 之前这里写的是 .group-tag：模型列与密钥列都用 GroupTag 渲染，而
    // 测量是全局查询，于是模型列那枚更宽的胶囊被算进了密钥列（密钥列常年宽 58px）。
    // 静态检查看不出跨列命中，那一条在 scripts/check-log-columns.mjs 里运行时把关；
    // 这里守住的是另一半：锚点的类名得真的挂在元素上。
    //
    // 复合选择器 '.key-tag .gt-text'（2026-09-26）：测量锚点挪到了 GroupTag
    // 渲染的内层 inline 盒上 —— 外层带 max-width: 100% 会被列宽压扁，
    // 量它等于量「当前列宽」，长密钥名永远撑不开列。gt-text 的元素在
    // GroupTag.vue 的模板里，断言换成读那个组件的源码。
    const groupTagSrc = readFileSync(join(SRC, 'components/GroupTag.vue'), 'utf8')
    const external = {
      '.key-tag': { src: tpl, re: /<GroupTag\b[^>]*\bkey-tag\b/ },
      '.key-tag .gt-text': { src: groupTagSrc, re: /class="gt-text"/ }
    }
    for (const sel of sels) {
      const ext = external[sel]
      const ok = ext
        ? ext.re.test(ext.src)
        : new RegExp(`class="[^"]*\\b${sel.replace(/^\./, '')}\\b`).test(tpl)
      check(`锚点 ${sel} 能对应到真实元素`, ok, '选择器与模板对不上时测量会静默跳过，那一列永远停在下限')
    }
  }
  // 每一列都要绑运行时列宽：漏一列，那一列就还是定宽（长内容照旧截断），
  // 而 check-table-widths.mjs 只看「有没有裸数字」，看不出漏绑
  const boundsBlock = panelSrc.match(/const COL_BOUNDS[\s\S]*?\n\}/)
  check('COL_BOUNDS 声明找得到', !!boundsBlock)
  if (boundsBlock) {
    const keys = [...boundsBlock[0].matchAll(/^\s{2}(\w+):\s*\[/gm)].map((m) => m[1])
    const bound = [...panelSrc.matchAll(/:width="colW\.(\w+)"/g)].map((m) => m[1])
    check('COL_BOUNDS 覆盖全部 12 列', keys.length === 12, `读到 ${keys.length} 个：${keys.join(', ')}`)
    check(
      '模板里每一列都绑了 colW（没有写死的 :width）',
      bound.length === keys.length && [...bound].sort().join(',') === [...keys].sort().join(','),
      `模板绑了 ${bound.sort().join(', ')}；COL_BOUNDS 声明了 ${keys.join(', ')}`,
    )
    // scroll.x 必须跟着列宽联动，否则声明总宽与实际不符
    check(
      'scroll.x 由各列宽之和给出',
      /:scroll="\{\s*x:\s*columnsTotal/.test(panelSrc),
      'scroll.x 与列宽来自两份定义时，横向滚动的边界会与实际不符',
    )

    // 弹性列（2026-09-26）：容器比声明合计宽时，Chrome 的 fixed 表格布局会把
    // 余量按比例摊给所有绑了宽度的列（1920 实测 ×1.59，渠道列 128 声明被拉到
    // 204 而内容只有 37），量得再准也会被拉伸毁掉。解法是留一列**不绑宽度**：
    // 当日上午它独吞全部余量、其余列 1:1 渲染；同日下午余量改由 distribute
    // 等额摊进各列，这列退到只剩「浮点零头 + 窄窗口收 0」的职责 ——
    // 但「恰好一列不绑 width」的形态没有变。删掉它不报错、列表照常出数据，
    // 只是窄窗口横向滚动时会留一道 16px 的空缝，所以这里钉住它必须存在：
    // 列数 = 绑 width 的列数 + 1（且仅此一列）。
    // 刻意不再 grep「弹性列」等注释字样做第二锚：它抓的回归（删列/多列）
    // 计数已全覆盖，只会因改写注释而误报。
    const colCount = (panelSrc.match(/<a-table-column[\s>]/g) || []).length
    check(
      '弹性列存在（绑宽列之外恰好一列不绑 width，吸收容器余量）',
      colCount === bound.length + 1,
      `模板共 ${colCount} 列，绑 width ${bound.length} 列`,
    )

    // ---- 下限与内容的相称性：**只报告，不判定**（2026-09-24）----
    //
    // 背景：ui-spec 第 17 条要求「每列按显示内容动态调整列宽」。机制是
    // `取 max(下限, 内容所需宽度)`，所以**下限一旦偏高，那一列就永远宽** ——
    // 量出来的内容宽度根本没机会生效。
    //
    // 渠道列就是这么坏掉的：下限 130 是 2026-09-16 人工拍的最坏情况常量，
    // 2026-09-23 改成按内容量之后被原样留下。当前页渠道名只要 47px
    //（+图标 24 +内边距 16 +呼吸 6 = 93）时列仍占 137px，**44px 全是空白**，
    // 而同期其它列只空 28-34px。站主一眼看出「渠道列宽不对」。
    //
    // 为什么最终**没有**做成断言 —— 我试了四种判据，全都会误伤或漏判：
    //   · 运行时绝对阈值 24px：全部列误判（正常列也空 30+px，因为 antd
    //     按比例分配容器余量）
    //   · 运行时绝对阈值 40px：放过真问题（旧代码渠道列空 38px 仍 PASS）
    //   · 运行时「比其它列中位数高 X px」：实测只差 8px，同样漏掉
    //   · 静态「下限 vs 表头+内边距」：把 model(76) / tokens(96) / speed(74)
    //     一并判失败 —— 那些余量是内容真的需要（模型名很长、词元是两排数字）
    //
    // 根因是「合理余量」取决于该列的真实数据，静态看不出来，而运行时又因
    // 余量分配而浮动。所以这里只把数字打出来供人核对 —— 站主当初就是
    // 「一眼看出渠道列宽不对」，数字在输出里，下次改列宽时能直接对比。
    // 运行时那一侧（scripts/check-log-columns.mjs）同样只报告各列空白量。
    const headLabels = {
      time: '请求时间', model: '模型', channel: '渠道', tokens: '词元',
      elapsed: '任务耗时', cost: '费用', speed: '速度', status: '状态',
      key: '密钥', client: '客户端', action: '操作',
    }
    const measurePads = {}
    for (const m of panelSrc.matchAll(/^\s{2}(\w+):\s*\{\s*sel:\s*'[^']+',\s*pad:\s*(\d+)/gm)) {
      measurePads[m[1]] = Number(m[2])
    }
    const CELL_PAD_TOTAL = 16
    const report = []
    for (const m of boundsBlock[0].matchAll(/^\s{2}(\w+):\s*\[(\d+),\s*(\d+)\]/gm)) {
      const key = m[1]
      const label = headLabels[key]
      if (!label) continue
      const headW = Math.ceil(
        [...label].reduce((s, ch) => s + (/[\u4e00-\u9fa5]/.test(ch) ? 14 : 8.4), 0),
      )
      const hardFloor = headW + CELL_PAD_TOTAL + (measurePads[key] || 0)
      report.push(`${key} ${m[2]}(余 ${Number(m[2]) - hardFloor})`)
    }
    console.log(`  下限与硬下界之差（表头+内边距+图标）：${report.join(' / ')}`)
  }
}

// ============================================================================
// 文字色令牌契约（2026-10-10）
// ============================================================================
//
// 为什么需要：theme.css 顶部立了一条约定 —— `--color-*` 只用于**填充、边框、
// 图标**（非文本，WCAG 1.4.11 要求 3:1），凡是读的字一律用 `--text-*`
// （正文要求 4.5:1）。这条约定靠人记是靠不住的：2026-10-10 审计出 8 处把
// 填充色当正文色用，最低的只有 2.15:1 —— 12px 的「N 个未定价」用
// --color-orange（#f59e0b 压白卡），而它恰恰是「这条渠道在漏钱」的唯一提示。
//
// 为什么 check-theme-contrast.mjs 抓不到：那个脚本算的是**令牌之间**的比值
// （--color-orange 对 --color-bg，作为非文本是达标的），而错在「组件挑了哪个
// 令牌」—— 令牌本身完全合法，只是不该出现在 `color:` 上。
//
// 判据：组件里 `color:` / `-webkit-text-fill-color:` 的值里出现
// `var(--color-<语义色>)` 即失败；报错时把各主题的实测对比度一并算出来，
// 省掉「为什么不行」的追问。允许的例外只有一类：行尾写
// `/* text-token-exempt: <理由> */` 的显式豁免（豁免会出现在输出里，看得见）。
console.log('')
console.log('=== 文字色令牌契约（--color-* 不许当正文色）===')
const FORBIDDEN_TEXT_TOKENS = [
  '--color-primary',
  '--color-red',
  '--color-orange',
  '--color-green',
  '--color-blue',
  '--color-purple',
  '--color-gray'
]
const TEXT_MIN = 4.5
const textThemes = parseThemes()
// 前置字符类要包含引号与括号：内联样式（style="color: …"）与 h() 里的
// 对象字面量都靠它们命中。`-` 刻意不在类里 —— 那正是把
// `background-color` / `border-color` 排除在外的方式。
const RE_TEXT_COLOR = /(?:^|[;{\s"'(,])color\s*:\s*([^;]+)/g

/** 该令牌在所有主题、两种底色上最差的那个比值（报错信息用） */
function worstTextContrast(token) {
  let worst = null
  for (const [id, theme] of textThemes) {
    const fg = parseColor(theme.vars.get(token))
    if (!fg) continue
    for (const bgName of ['--color-fg', '--color-bg']) {
      const bg = parseColor(theme.vars.get(bgName))
      if (!bg) continue
      const ratio = contrast(fg, bg)
      if (!worst || ratio < worst.ratio) worst = { ratio, id, bgName }
    }
  }
  if (!worst) return '（算不出对比度：theme.css 里找不到这个令牌）'
  return (
    `实测最低 ${worst.ratio.toFixed(2)}:1（${worst.id} 主题，压 ${worst.bgName}）` +
    ` —— 正文要 ${TEXT_MIN}:1，请改用同色相的 --text-* 档`
  )
}

let textViolations = 0
let textColorDecls = 0
for (const file of walk(SRC)) {
  const raw = readFileSync(file, 'utf8')
  // 用「保留行数」的剥离版本：stripComments 会把多行注释整段删掉，
  // 于是报出来的行号指向的是**剥离后**的源码 —— 而人要按行号去原文件里
  // 找（实测差 5~8 行，找到的是另一条规则）。
  const src = stripComments(raw, true)
  const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
  const lines = src.split('\n')
  const rawLines = raw.split('\n')
  for (let i = 0; i < lines.length; i++) {
    for (const m of lines[i].matchAll(RE_TEXT_COLOR)) {
      textColorDecls++
      const value = m[1]
      const hit = FORBIDDEN_TEXT_TOKENS.find((t) =>
        new RegExp(`var\\(${t}(?![\\w-])`).test(value)
      )
      if (!hit) continue
      // 豁免写在**原始行**上（注释已被剥离，所以判据用 rawLines）
      if (/text-token-exempt/.test(rawLines[i] || '')) continue
      textViolations++
      check(`${rel}:${i + 1} 文字色不用 ${hit}`, false, worstTextContrast(hit))
    }
  }
}
// 非空断言：正则结构一变（比如 color: 的写法改成别处解析），下面那条
// 「没有违规」会永远绿 —— 全绿但什么都没扫，比红更危险。
check(
  `扫到足够多的 color: 声明（${textColorDecls} 条）`,
  textColorDecls >= 40,
  '只扫到极少声明 —— 是解析器坏了，不是代码干净'
)
if (textViolations === 0) {
  check('组件里没有把填充/图形色当正文色的地方', true)
}

// ============================================================================
// 弹窗宽度契约（2026-10-10）
// ============================================================================
//
// 十二个弹窗/抽屉此前各写一遍 `'min(560px, 94vw)'`，于是出现两种漂移：
// ChannelsView 的「测试连通性」是裸数字 440（没有任何窄屏保护，窄窗口两侧被裁），
// DailyReport 的「昨日战报」是 92vw（与其余十一处的 94vw 不一致）。
// 现在值只有一个出口：utils/modalWidth.ts 的 modalWidth(N)。
// 判据：每个 a-modal / a-drawer 的 :width 必须来自它，且模板里不许再出现字面 vw。
console.log('')
console.log('=== 弹窗宽度契约 ===')

/**
 * 取一个标签的属性串：从 start 往后扫到**不在引号内**的第一个 `>`。
 * 不能用 `/<a-modal\b([\s\S]*?)>/` —— 属性里的箭头函数（`@update:open="v => …"`）
 * 含 `>`，懒匹配会停在那个箭头上，属性串被腰斩（实测：ModelPricingEditor 的
 * :width 恰好排在箭头之前才侥幸命中）。
 */
function tagBody(src, start) {
  let quote = null
  for (let i = start; i < src.length; i++) {
    const c = src[i]
    if (quote) {
      if (c === quote) quote = null
      continue
    }
    if (c === '"' || c === "'") {
      quote = c
      continue
    }
    if (c === '>') return src.slice(start, i)
  }
  return src.slice(start)
}

let widthChecked = 0
for (const file of walk(SRC)) {
  if (!file.endsWith('.vue')) continue
  const src = stripComments(readFileSync(file, 'utf8'))
  const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
  for (const m of src.matchAll(/<(a-modal|a-drawer)\b/g)) {
    widthChecked++
    const body = tagBody(src, m.index + m[0].length)
    const ok = /:width="modalWidth\(\d+\)"/.test(body)
    check(
      `${rel} 第 ${widthChecked} 处 ${m[1]} 的宽度来自 modalWidth()`,
      ok,
      ok ? '' : '缺 :width="modalWidth(NNN)"，或写了字面量（窄窗口会被顶出视口）'
    )
  }
  const literal = src.match(/:width="'min\([^']*vw'\)"/)
  check(
    `${rel} 没有字面 vw 宽度`,
    !literal,
    literal ? `模板里仍有 ${literal[0]}，请改用 modalWidth()` : ''
  )
}

// ============================================================================
// 响应式断点契约（2026-10-10）
// ============================================================================
//
// 刻度写在 theme.css 的注释里（CSS 自定义属性不能用在 @media 条件里），
// 于是它是**文档**而不是代码 —— 而文档会漂：2026-10-10 之前 SettingsView 与
// ThemePicker 的 @media 用的是 900 / 600，其中 900 只属于「侧栏自动折叠」
// 那一个功能断点（MainLayout 的 JS 常量，不是 @media）。这里把两件事钉住：
//   1. theme.css 注释里列出的刻度与脚本里这份清单逐项相等；
//   2. 所有 .vue 里 @media (max-width|min-width: N) 的 N 都在刻度上。
console.log('')
console.log('=== 响应式断点契约 ===')
const BREAKPOINT_SCALE = [640, 768, 1024, 1200, 1536]

const themeSrc = readFileSync(join(SRC, 'styles', 'theme.css'), 'utf8')
const scaleBlock = themeSrc.match(/响应式断点基准[\s\S]*?\*\//)
const docScale = scaleBlock
  ? [...scaleBlock[0].matchAll(/^\s{7}(\d+)px\s/gm)].map((m) => Number(m[1]))
  : []
check(
  'theme.css 的断点刻度与脚本清单一致',
  docScale.length === BREAKPOINT_SCALE.length &&
    docScale.every((v, i) => v === BREAKPOINT_SCALE[i]),
  `文档里是 ${docScale.join('/') || '(没解析到)'}，脚本里是 ${BREAKPOINT_SCALE.join('/')}`
)

for (const file of walk(SRC)) {
  if (!file.endsWith('.vue')) continue
  const src = stripComments(readFileSync(file, 'utf8'))
  const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
  for (const m of src.matchAll(/@media\s*\((max|min)-width:\s*(\d+)px\)/g)) {
    check(
      `${rel} @media ${m[1]}-width: ${m[2]}px 在刻度上`,
      BREAKPOINT_SCALE.includes(Number(m[2])),
      `刻度：${BREAKPOINT_SCALE.join(' / ')}px；900 是侧栏折叠的功能断点，只以 JS 常量形式存在于 MainLayout，不写成 @media`
    )
  }
}

// ============================================================================
// 字号刻度契约（2026-10-10）
// ============================================================================
//
// 收口前组件里散着 117 处字面字号（10~26px，其中 12px 一档 63 处）：
// 问题不是值难看，而是**没有刻度** —— 下一个人只能再挑一个数字，
// 13.5px / 18px 迟早出现，而「哪些地方该一样大」从此说不清。
// 刻度定义在 theme.css 的 :root（那段注释解释了 lg/xl 为什么夹在正文之间）。
// 判据两条：令牌清单必须写全；组件里不许再出现字面 font-size。
console.log('')
console.log('=== 字号刻度契约 ===')
const FONT_SIZE_TOKENS = [
  '--font-size-2xs',
  '--font-size-xs',
  '--font-size-sm',
  '--font-size-md',
  '--font-size-panel',
  '--font-size-lg',
  '--font-size-base',
  '--font-size-xl',
  '--font-size-display-sm',
  '--font-size-display',
  '--font-size-menu'
]
{
  const rootVars = textThemes.get('light')?.vars ?? new Map()
  const missing = FONT_SIZE_TOKENS.filter((t) => !rootVars.has(t))
  check('theme.css 的 :root 写全了字号刻度', missing.length === 0, missing.join(', '))
}

let fontSizeLiterals = 0
let fontSizeDecls = 0
for (const file of walk(SRC)) {
  if (!file.endsWith('.vue')) continue
  const raw = readFileSync(file, 'utf8')
  const src = stripComments(raw, true)
  const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
  const lines = src.split('\n')
  const rawLines = raw.split('\n')
  for (let i = 0; i < lines.length; i++) {
    if (!/font-size\s*:/.test(lines[i])) continue
    fontSizeDecls++
    const m = lines[i].match(/font-size\s*:\s*([^;]+)/)
    if (!m) continue
    const value = m[1].trim()
    // 正确写法是干净的一个令牌引用。注意**不能**用「值里有没有数字」判断：
    // --font-size-2xs 这个名字本身就带数字（实测踩过，4 处正确写法被判红）
    if (/^var\(--font-size-[\w-]+\)$/.test(value)) continue
    if (!/\d/.test(value)) continue
    if (/font-size-token-exempt/.test(rawLines[i] || '')) continue
    fontSizeLiterals++
    check(`${rel}:${i + 1} 字号走令牌`, false, `写了「${value}」，请用 var(--font-size-*)`)
  }
}
check(
  `扫到足够多的 font-size 声明（${fontSizeDecls} 条）`,
  fontSizeDecls >= 60,
  '只扫到极少声明 —— 是解析器坏了，不是代码干净'
)
if (fontSizeLiterals === 0) {
  check('组件里没有字面字号（全部走 --font-size-* 刻度）', true)
}

// ============================================================================
// 标题层级契约（2026-10-10）
// ============================================================================
//
// 页面标题此前只有一个 <h1>（登录页），各管理页的面板标题是 <h2> 起步，
// 而系统设置那八个面板标题干脆是 <div> —— 读屏用户按标题跳转时，
// 既没有「当前在哪一页」，也拿不到「这一页有哪几块」。
// 收口后的结构：h1（MainLayout 的 .sr-only 页面名）→ h2（面板）→ h3（子块）。
console.log('')
console.log('=== 标题层级契约 ===')
{
  const layoutSrc = stripComments(readFileSync(join(SRC, 'components/MainLayout.vue'), 'utf8'))
  const h1Count = (layoutSrc.match(/<h1\b/g) || []).length
  check('MainLayout 有且仅有一个 <h1>', h1Count === 1, `实测 ${h1Count} 个`)
  check(
    'MainLayout 的 <h1> 是视觉隐藏的（.sr-only）',
    /<h1 class="sr-only"/.test(layoutSrc),
    '页面名在视觉上已由侧栏与各页标题表达，h1 只承担文档结构'
  )

  const loginSrc = stripComments(readFileSync(join(SRC, 'views/LoginView.vue'), 'utf8'))
  check('登录页有且仅有一个 <h1>（它不在 MainLayout 里）', (loginSrc.match(/<h1\b/g) || []).length === 1)


  // 动态码输入框不许带 maxlength（2026-10-27）。
  //
  // 浏览器在 input 事件**之前**按 maxlength 截断原始字符串：粘贴
  // "123 456" 先变成 "123 45"，再经 sanitizeCode 清洗得 "12345" ——
  // 第 6 位数字被丢掉，用户从认证器复制的码永远验不过，界面也不报错。
  // 截断该由 sanitizeCode 负责（它看到完整原文），这个属性只会抢在前面截。
  const codeInputsWithMaxlength = []
  for (const file of walk(SRC)) {
    if (!file.endsWith('.vue')) continue
    const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
    const src = stripComments(readFileSync(file, 'utf8'))
    // 只盯同时用了 sanitizeCode 的输入框：那是动态码，别的字段的 maxlength 合法
    if (!src.includes('sanitizeCode')) continue
    if (/:maxlength\s*=/.test(src)) codeInputsWithMaxlength.push(rel)
  }
  check(
    '动态码输入框不用 maxlength（会抢在 sanitizeCode 之前截断粘贴内容）',
    codeInputsWithMaxlength.length === 0,
    codeInputsWithMaxlength.join(', ')
  )
  // 两条规则各出一条断言（而不是每个文件一条）：三十来个 .vue 会刷出六十行
  // PASS，真正失败的那一行反而找不着 —— 违规文件列在 detail 里更清楚
  const ownH1 = []
  const fakeTitle = []
  for (const file of walk(SRC)) {
    if (!file.endsWith('.vue')) continue
    const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
    const src = stripComments(readFileSync(file, 'utf8'))
    // 视图里不该再有 h1：MainLayout 已经提供，重复会让「当前在哪一页」出现两份
    if (rel.startsWith('views/') && rel !== 'views/LoginView.vue' && /<h1\b/.test(src)) {
      ownH1.push(rel)
    }
    // 标题语义不能用 <div> 冒充：这是 2026-10-10 修掉的那一类
    if (/<div[^>]*class="[^"]*\bpanel-title\b/.test(src)) fakeTitle.push(rel)
  }
  check('各视图没有自己的 <h1>（由 MainLayout 统一提供）', ownH1.length === 0, ownH1.join(', '))
  check('没有 <div class="panel-title"> 这类假标题', fakeTitle.length === 0, fakeTitle.join(', '))
}

// ============================================================
// 模板里用到的组件必须真的被引入（2026-10-11 加）
//
// 为什么需要这条：组件在这个仓库里是**逐文件 import** 的（只有 main.ts 里
// 那 20 来个 antd 组件是全局注册的），而 `vue-tsc` **不检查模板里的未知标签** ——
// 漏一个 import 的表现是「控制台一条 Failed to resolve component 警告 +
// 那一块渲染不出来（或只剩文字）」，而 type-check、契约检查、列宽守卫
// 全都照过。这一批里就真的漏了两处（ChannelsView 的两枚批量图标、
// ProxiesView 的 EmptyState），是代码审查抓出来的 —— 那就该有一条脚本管着。
{
  console.log('\n=== 模板组件的引入完整性 ===')
  // main.ts 里 app.use(...) 注册过的组件（模板里写成 a-button 这类 kebab）。
  // 用「大写开头的标签」做检查，所以这里列的是它们的 PascalCase 形式。
  const globallyRegistered = new Set(
    (readFileSync(join(SRC, 'main.ts'), 'utf8').match(/\bcomponents = \[([\s\S]*?)\]/)?.[1] || '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
  )
  // Vue 内置 / 编译器关键字：它们不需要 import
  const BUILTIN = new Set([
    'Component', 'Transition', 'TransitionGroup', 'KeepAlive', 'Teleport',
    'Suspense', 'Fragment', 'Slot', 'Template'
  ])

  const missing = []
  for (const file of walk(SRC)) {
    if (!file.endsWith('.vue')) continue
    const rel = file.slice(SRC.length + 1).replace(/\\/g, '/')
    const raw = readFileSync(file, 'utf8')
    // 只在 <template> 段里找：<script> 里会有 h(Component) 这类调用
    const tpl = raw.match(/<template>([\s\S]*)<\/template>/)?.[1]
    if (!tpl) continue
    // 注释里的示例代码不算（stripComments 会去掉 <!-- -->）
    const clean = stripComments(tpl)
    // script setup 里 import 进来的所有绑定名。
    // 先 stripComments：import 列表里允许写注释（本仓库的惯例就是在那一行
    // 说明「这枚图标是干什么的」），而朴素解析会把注释文字也当成标识符 ——
    // 第一版就是这么错的：ChannelsView 明明 import 了 PlayCircleOutlined，
    // 报出来却是「没引入」，一屏全是假阳性。
    const script = stripComments(raw.match(/<script setup[^>]*>([\s\S]*?)<\/script>/)?.[1] || '')
    const imported = new Set()
    // 先抓 `import <clause> from '...'`：clause 可能是默认导入、命名导入、
    // 或两者混写（`import X, { a } from`），所以整段交给下面再拆
    for (const m of script.matchAll(/\bimport\s+([^'"]+?)\s+from\s*['"]/g)) {
      const clause = m[1]
      const dflt = clause.match(/^\s*([A-Za-z_$][\w$]*)/)
      if (dflt) imported.add(dflt[1])
      const named = clause.match(/\{([\s\S]*)\}/)
      if (named) {
        for (const part of named[1].split(',')) {
          const name = part.trim().split(/\s+as\s+/).pop()?.trim()
          if (name) imported.add(name)
        }
      }
    }
    // 模板里所有大写开头的标签
    for (const m of clean.matchAll(/<([A-Z][A-Za-z0-9]*)\b/g)) {
      const tag = m[1]
      if (imported.has(tag) || globallyRegistered.has(tag) || BUILTIN.has(tag)) continue
      missing.push(rel + ' → <' + tag + '>')
    }
  }
  check(
    '模板里的大写标签都在本文件 import 过（或在 main.ts 全局注册）',
    missing.length === 0,
    [...new Set(missing)].join('；')
  )
}

// ============================================================================
// 客户端图标契约（2026-10-14，随请求日志「客户端」列一起立）
// ============================================================================
//
// 为什么需要：客户端图标这条链路上有三份靠字符串耦合的清单 ——
//   · 后端识别规则表（relay/client.go 的 clientRules 的 id）；
//   · 前端映射表（components/clientMeta.ts 的 CLIENT_META 的 id → name/file）；
//   · 图标文件本身（public/client-icons/*.svg）。
//
// 三者任意一处漂移都不会报错，只会静默降级：映射表写了文件但没放 →
// 图标是空白（<mask> 引不存在的 url 不报错）；后端有 id 前端没映射 →
// 显示默认图标；两边名字不一致 → 同一客户端在列表与详情里两个叫法。
// 这三类都是「看起来正常、直到有人截图」的形态，必须变成会红的断言。
//
// 与既有契约同一手法：从源码文本解析而不是在脚本里抄一份清单
// （抄的那份会在源码改名后静默失效，本文件里 CONTENT_MEASURE 与
// COL_LABELS 两处都为此踩过坑）。
{
  const metaSrc = readFileSync(join(SRC, 'components/clientMeta.ts'), 'utf8')
  const iconsDir = join(SRC, '..', 'public', 'client-icons')

  // 前端映射表：抓 `'id': { name: '...', file: '...' }` 与不带引号的
  // 裸 id 两种形态（clientMeta.ts 里两种都在用）
  const metaEntries = [
    ...metaSrc.matchAll(/^\s{2}'?([a-z0-9-]+)'?:\s*\{\s*name:\s*'([^']+)',\s*file:\s*'([^']+)'\s*\},?\s*$/gm),
  ].map((m) => ({ id: m[1], name: m[2], file: m[3] }))
  check('能解析出 CLIENT_META 的条目', metaEntries.length >= 30, `读到 ${metaEntries.length} 条`)

  // ① 映射表里引用的每个图标文件都要真实存在
  let iconFiles = []
  try {
    iconFiles = readdirSync(iconsDir).filter((f) => f.endsWith('.svg'))
  } catch {
    /* 目录缺失下面单独报 */
  }
  const missingFiles = metaEntries.filter((e) => !iconFiles.includes(e.file))
  check(
    'CLIENT_META 引用的图标文件都存在于 public/client-icons/',
    missingFiles.length === 0 && iconFiles.length > 0,
    missingFiles.length
      ? missingFiles.map((e) => `${e.id} → ${e.file}`).join('；')
      : '目录不存在或没有任何 .svg'
  )

  // ② 默认图标必须存在：它是所有未知 id 的兜底，缺了整列图标全空
  check('默认图标 default.svg 存在', iconFiles.includes('default.svg'))

  // ③ 后端规则表的 id / 显示名与前端映射表一致（后端 ⊆ 前端）
  //
  // 反向不要求：前端可能保留后端已删规则的 id 用于历史行 ——
  // 那些老行的 client_id 还在库里，映射删了它们会掉到默认图标。
  // 解析的是 `{id: "x", name: "X", ...}` 这种结构体字面量；
  // client.go 里规则表的写法变了这条要跟着改（failure 信息里会写清）。
  const clientGo = join(SRC, '..', '..', 'backend', 'internal', 'relay', 'client.go')
  let goRules = []
  try {
    const goSrc = readFileSync(clientGo, 'utf8')
    goRules = [...goSrc.matchAll(/\{id:\s*"([a-z0-9-]+)",\s*name:\s*"([^"]+)"/g)].map((m) => ({
      id: m[1],
      name: m[2],
    }))
  } catch {
    /* 后端源码不在（纯前端 checkout）：跳过，CI 里前后端一起 checkout 会真正跑到 */
  }
  if (goRules.length) {
    const byId = new Map(metaEntries.map((e) => [e.id, e]))
    const missingIds = [...new Set(goRules.map((r) => r.id))].filter((id) => !byId.has(id))
    check(
      '后端 clientRules 的 id 都在前端 CLIENT_META 里（漏了会显示默认图标）',
      missingIds.length === 0,
      `缺：${missingIds.join(', ')} —— 在 components/clientMeta.ts 里补上`
    )
    const nameMismatch = goRules.filter((r) => byId.has(r.id) && byId.get(r.id).name !== r.name)
    check(
      '后端 display name 与前端映射表一致（两处叫法不同会让同一行看到两个名字）',
      nameMismatch.length === 0,
      nameMismatch.map((r) => `${r.id}: 后端「${r.name}」vs 前端「${byId.get(r.id).name}」`).join('；')
    )
  } else {
    console.log('  INFO  未读到后端 relay/client.go，跳过 id 集合比对（前端 CI 不 checkout 后端时属正常）')
  }

  // ④ ICON_LIBRARY 与目录实文件双向一致（2026-10-15，随客户端显示映射一起立）。
  //    这份清单是映射面板图标下拉的选项来源、也是 clientDisplay 判定
  //    「自定义图标按文件名引用内置图标」合法性的依据 ——
  //    清单里多了（文件没放）：下拉选了渲染一枚空白 mask，不报错；
  //    目录里多了（清单没记）：新图标放进去了却在下拉里选不到。
  //    两个方向都是「加了图标忘了另一半」的形态，必须双向钉住。
  const libMatch = metaSrc.match(/export const ICON_LIBRARY[\s\S]*?\n\]/)
  check('能解析出 ICON_LIBRARY 清单', !!libMatch, 'components/clientMeta.ts 里找不到 export const ICON_LIBRARY')
  if (libMatch) {
    const libFiles = [...libMatch[0].matchAll(/'([a-z0-9-]+\.svg)'/g)].map((m) => m[1])
    check('ICON_LIBRARY 非空且无重复', libFiles.length > 0 && new Set(libFiles).size === libFiles.length,
      `读到 ${libFiles.length} 条`)
    const dirSet = new Set(iconFiles)
    const inLibNotDir = [...new Set(libFiles)].filter((f) => !dirSet.has(f))
    check('ICON_LIBRARY 里的文件都存在于 client-icons/（缺了选了渲染空白）',
      inLibNotDir.length === 0, `缺：${inLibNotDir.join('；')}`)
    const inDirNotLib = iconFiles.filter((f) => !libFiles.includes(f))
    check('client-icons/ 里的文件都记进了 ICON_LIBRARY（漏了下拉里选不到）',
      inDirNotLib.length === 0, `未记录：${inDirNotLib.join('；')}`)
  }
}

console.log('')
console.log('=== 带宽缓冲前后端一致 ===')

// 带宽环形缓冲的长度在两边各存一份：后端 live.go 的 bandwidthHistory（决定
// 补帧时下发多少拍）与前端 bandwidth.ts 的 BANDWIDTH_POINTS（决定客户端自己
// 封到多少点）。数量级参数没法自然共享，但可以锁住它们相等 ——
// 不等时没有任何报错：服务端发 120 点、客户端裁到 60（或反过来只剩半分钟），
// 曲线上看起来都「正常」，只是窗口长度悄悄变了。
//
// 静态比对源码常量，不依赖构建产物；后端文件缺失时跳过而不是判失败。
{
  let checked = false
  try {
    const goSrc = readFileSync(join(SRC, '..', '..', 'backend', 'internal', 'api', 'live.go'), 'utf8')
    const goN = goSrc.match(/const\s+bandwidthHistory\s*=\s*(\d+)/)
    const tsSrc = readFileSync(join(SRC, 'components/bandwidth.ts'), 'utf8')
    const tsN = tsSrc.match(/export\s+const\s+BANDWIDTH_POINTS\s*=\s*(\d+)/)
    if (goN && tsN) {
      checked = true
      check(
        `带宽缓冲长度前后端一致（${goN[1]} 拍）`,
        Number(goN[1]) === Number(tsN[1]),
        `后端 bandwidthHistory=${goN[1]}，前端 BANDWIDTH_POINTS=${tsN[1]}`,
      )
    }
  } catch {
    // 后端源码不在本地：跳过（CI 里前后端一起 checkout，会真正跑到）
  }
  if (!checked) console.log('  SKIP  未找到后端 api/live.go，跳过')
}

// ---- 浮层类名属性必须与组件的 prop 名一致 ----
//
// antd 的浮层类名属性各家不同：Select 用 popupClassName（dropdownClassName
// 已废弃），Popover / Drawer / Tooltip 用 overlayClassName。写错了不会报错 ——
// Vue 把不认识的属性当普通 DOM 属性透传，类名永远挂不到浮层上，
// 表现是「样式写了但不生效」，而且看起来像 CSS 选择器的问题。
//
// 2026-10-27 实测踩到：给 a-select 写了 overlay-class-name 想限制下拉浮层
// 宽度，样式一条都没命中。
{
  const dtsPath = 'node_modules/ant-design-vue/es/select/index.d.ts'
  let dts = ''
  try {
    dts = readFileSync(dtsPath, 'utf8')
  } catch {
    dts = ''
  }
  // 从 d.ts 里取 Select 的 prop 名（4 空格缩进的 key）
  const selectProps = dts ? [...dts.matchAll(/^\s{4}(\w+):/gm)].map((m) => m[1]) : []
  if (!selectProps.length) {
    console.log('  SKIP  未找到 antd Select 的类型声明，跳过浮层类名检查')
  } else {
    check('Select 用 popupClassName（不是 overlayClassName）', selectProps.includes('popupClassName'), selectProps.filter((p) => /class/i.test(p)).join(', '))
    check('Select 不支持 overlayClassName', !selectProps.includes('overlayClassName'))

    // 模板里每个 a-select 都必须用 popup-class-name
    let wrong = []
    for (const file of walk(SRC)) {
      const src = readFileSync(file, 'utf8')
      for (const m of src.matchAll(/<a-select(?![\w-])[\s\S]*?(\/>|>)/g)) {
        if (/overlay-class-name/.test(m[0])) wrong.push(file)
      }
    }
    check('模板里 a-select 没有误用 overlay-class-name', wrong.length === 0, wrong.join(', '))
  }
}

console.log('')
if (failed > 0) {
  console.log(`${failed} 项未通过`)
  process.exit(1)
}
console.log('全部通过')
