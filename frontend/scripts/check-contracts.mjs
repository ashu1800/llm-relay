// 渲染期契约检查：把每个页面的 DataState 用法和组件的 props 声明对一遍。
//
// 为什么需要这个：DataState 的根节点是 Fragment（多个 v-if 分支），
// Vue 无法把未声明的属性透传到子节点，只会打印一条开发期警告 ——
// 而那条警告在构建产物里被剥掉，测试也不看控制台，于是
// ProxiesView 传的 :empty / empty-text 错了很久都没人发现（空态文案从来没显示过）。
//
// 这里直接读 .vue 源码做静态比对，不依赖浏览器环境。
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

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

// 深色块必须覆盖语义色：漏掉的话深色模式下会用到浅色底上挑的值
const darkStart = theme.indexOf(":root[data-theme='dark']")
const darkBlock = darkStart >= 0 ? theme.slice(darkStart, theme.indexOf('\n}', darkStart)) : ''
for (const tok of ['--color-red', '--color-orange', '--color-green', '--color-blue', '--color-purple', '--color-gray', '--color-icon', '--text-primary-ink']) {
  check(`深色主题覆盖了 ${tok}`, darkBlock.includes(tok + ':'))
}

// 光标 SVG 里的品牌色是 --color-primary 的第二份定义。
//
// 光标是当作图片加载的，没有 CSS 级联，SVG 里写不了 var(--color-primary)，
// 只能把色值抄一遍。这与 theme.ts 里记的「双份定义会漂移」是同一类坑：
// 改主色时漏掉光标，界面不会报错、也不会失败，只是光标停在旧主色上，
// 肉眼在两种颜色之间很难发现。所以在这里盯住。
console.log('')
console.log('=== 光标资源 ===')
const primaryMatch = theme.match(/--color-primary:\s*(#[0-9a-fA-F]{3,8})/)
check('theme.css 里能读到 --color-primary', !!primaryMatch)
if (primaryMatch) {
  const primary = primaryMatch[1].toLowerCase()
  const publicDir = join(SRC, '..', 'public')
  for (const f of ['cursor-arrow.svg', 'cursor-hand.svg']) {
    const svg = readFileSync(join(publicDir, f), 'utf8')
    check(
      `${f} 的填充色与 --color-primary 一致`,
      svg.toLowerCase().includes(primary),
      `SVG 里没找到 ${primary}，把 fill 同步成主色`
    )
    // 光标图必须自带固有尺寸：Chrome 拿不到 width/height 时不会渲染光标，
    // 也不报错，表现只是「样式改了但没效果」
    check(`${f} 带固有尺寸`, /<svg[^>]*\bwidth="\d+"[^>]*\bheight="\d+"/.test(svg.replace(/\s+/g, ' ')))
    // Firefox 67 起自定义光标上限 32x32，超了会被整个丢弃
    const wh = svg.match(/<svg[^>]*\bwidth="(\d+)"[^>]*\bheight="(\d+)"/)
    check(`${f} 不超过 32x32（Firefox 上限）`, !!wh && +wh[1] <= 32 && +wh[2] <= 32, wh ? `实测 ${wh[1]}x${wh[2]}` : '读不到尺寸')
    // XML 注释里出现连续两个减号会让整个 SVG 解析失败（favicon 踩过这个坑）
    const comments = svg.match(/<!--[\s\S]*?-->/g) || []
    check(`${f} 注释里没有连续减号`, !comments.some((c) => c.slice(4, -3).includes('--')))
  }
  // CSS 里的热区必须与图形对得上：写错的表现是「点下去的位置和看到的尖差开」
  for (const tok of ['--cursor-arrow', '--cursor-hand']) {
    check(`theme.css 定义了 ${tok} 且带热区坐标`, new RegExp(tok + ":\\s*url\\('[^']+'\\)\\s+\\d+\\s+\\d+,").test(theme))
  }
}

console.log('')
if (failed > 0) {
  console.log(`${failed} 项未通过`)
  process.exit(1)
}
console.log('全部通过')
