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
 */
function stripComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/[^\n]*/g, '$1')
    .replace(/<!--[\s\S]*?-->/g, '')
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
//   2. JS 里通过 :style 绑定的（例如 groupStyle.ts 返回 { '--gt-h': ... }）
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

// 列表与工具栏筛选解耦（2026-09-16 站主要求）。
//
// 为什么值得钉住：合并进看板时这两者本来是共用一套条件的（同一个工具栏同时喂
// 卡片与列表，理由是「卡片按 A 算、列表按 B 查」看不出来）。站主后来说
// 「请求日志列表不再使用筛选条件显示了，默认显示所有最新的请求」——
// 因为列表的用处是盯着最新发生了什么，筛过之后反而看不到刚进来的调用。
// 这是个**反向**的约定（原来是「必须共用」，现在是「不许共用」），
// 最容易在下次改工具栏时被顺手加回来，所以两边都钉：组件不许再声明这四个 prop、
// 看板不许再传，查询串里也不许再出现它们。
console.log('')
console.log('=== 请求日志列表不吃筛选条件 ===')
{
  const panelPath = join(SRC, 'components/RequestLogPanel.vue')
  const panel = readFileSync(panelPath, 'utf8')
  const dash = readFileSync(join(SRC, 'views/DashboardView.vue'), 'utf8')
  const panelProps = declaredProps(panelPath)

  const filterProps = ['range', 'groupId', 'channelId', 'model']
  const declared = filterProps.filter((p) => panelProps.has(p))
  check(
    '面板不再声明筛选类 props（时间范围 / 分组 / 渠道 / 模型）',
    declared.length === 0,
    `又加回来了: ${declared.join(', ')} —— 列表会重新跟着工具栏走`,
  )
  const referenced = filterProps.filter((p) => new RegExp(`props\\.${p}\\b`).test(panel))
  check('面板里没有残留的 props.<筛选> 引用', referenced.length === 0, referenced.join(', '))

  // 查询串只该有分页、两个排障深链与排序。用参数名而不是「有没有 if」来判：
  // 少传一个 range 但改成别的方式塞进去（比如拼在 URL 上）同样要拦住。
  //
  // 2026-09-24：白名单加入 sort（P1-9 的服务端排序）。它进查询串是有意的 ——
  // 排序必须由服务端做，前端只能排当前页那 50 条，而「最贵的一单」
  // 显然不在当前页。这条断言的用意仍然成立：**别的一律不许出现**
  //（尤其是时间范围 / 分组 / 渠道 / 模型那四个，见下面两条）。
  const bpStart = panel.indexOf('function buildParams')
  const bp = panel.slice(bpStart, panel.indexOf('\n}', bpStart))
  const sent = [...bp.matchAll(/params\.set\(\s*'([\w-]+)'/g)].map((m) => m[1]).sort()
  check(
    '查询串只剩分页、两个排障深链与排序',
    sent.join(',') === 'page,page_size,sort,status_class,trace_id',
    `实际发出: ${sent.join(', ')}`,
  )

  // 反过来也要钉：两个深链必须留着 —— 「点日志行看整条链路」与「只看失败」
  // 的入口靠它，删掉的话排障深链会静默失效（页面上看不出来）
  check(
    '两个排障深链仍然进查询串（trace_id / status_class）',
    /params\.set\('trace_id'/.test(bp) && /params\.set\('status_class'/.test(bp),
  )

  // 看板那边也不许再传：组件不声明时传下去只会被当成透传属性，
  // 既不报错也不生效，是最难发现的一种「改了没反应」
  const tagStart = dash.indexOf('<RequestLogPanel')
  const tag = dash.slice(tagStart, dash.indexOf('/>', tagStart))
  const passed = filterProps.filter((p) =>
    new RegExp(`:${p.replace(/[A-Z]/g, (c) => '-' + c.toLowerCase())}=`).test(tag),
  )
  check('看板不再把这四个条件传给列表', passed.length === 0, `仍在传: ${passed.join(', ')}`)

  // 作用范围提示已于 2026-09-17 应站主要求移除。当初「必须写这句话」的
  // 契约一并反转成禁止式：钉住它不再出现 —— 从旧分支或旧文档里把标签
  // 带回来不会报任何错，而那已经不是当前的产品决定
  check(
    '工具栏不再带「筛选只作用于上方卡片」提示',
    !/筛选只作用于上方卡片/.test(dash),
    '2026-09-17 站主移除了该标签；若要恢复请连同本契约一起改',
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
    check('COL_BOUNDS 覆盖全部 10 列', keys.length === 10, `读到 ${keys.length} 个：${keys.join(', ')}`)
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
      key: '密钥', action: '操作',
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

console.log('')
if (failed > 0) {
  console.log(`${failed} 项未通过`)
  process.exit(1)
}
console.log('全部通过')
