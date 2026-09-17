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
  check(
    '滑入 / 光晕两档的选择器都落到 tr.is-new > td 上',
    /:global\(\.log-table\[data-fx='slide'\]\s+tr\.is-new\s*>\s*td\)/.test(fxCode) &&
      /:global\(\.log-table\[data-fx='glow'\]\s+tr\.is-new\s*>\s*td\)/.test(fxCode),
  )
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
  //   2) 圆角只能在整条上，两段各自加圆角会让交界处收窄（截图里是直角）；
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
    '竖条圆角只加在整条上（两段各自加会在交界处收窄）',
    /border-radius/.test(bar) && !/border-radius/.test(seg),
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

  // 阈值是站主定的，写在函数里而不是散在模板各处的三元表达式里
  const lat = panel.slice(panel.indexOf('function latencyClass'), panel.indexOf('function statusColor'))
  check('首字档位是 10s / 30s', /first'\s*\?\s*\[10000,\s*30000\]/.test(lat))
  check('耗时档位是 20s / 60s', /\[20000,\s*60000\]/.test(lat))
  // 两位小数：秒一律 toFixed(2)，站主明确要求「保留两位并且补齐两位」
  check(
    '耗时秒值补齐两位小数',
    /\(v \/ 1000\)\.toFixed\(2\)/.test(panel),
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

  // 查询串只该有分页与两个排障深链。用参数名而不是「有没有 if」来判：
  // 少传一个 range 但改成别的方式塞进去（比如拼在 URL 上）同样要拦住。
  const bpStart = panel.indexOf('function buildParams')
  const bp = panel.slice(bpStart, panel.indexOf('\n}', bpStart))
  const sent = [...bp.matchAll(/params\.set\(\s*'([\w-]+)'/g)].map((m) => m[1]).sort()
  check(
    '查询串只剩分页与两个排障深链',
    sent.join(',') === 'page,page_size,status_class,trace_id',
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
if (failed > 0) {
  console.log(`${failed} 项未通过`)
  process.exit(1)
}
console.log('全部通过')
