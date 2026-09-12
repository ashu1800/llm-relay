// 找出「模板里用了、但全项目任何样式块里都没定义」的 class。
//
// 这类问题不会报错，只会让元素退回浏览器默认样式 ——
// 就像 Dashboard 那排时间范围按钮，看起来"能用"，但一眼就不像这套界面。
//
// 会有误报：antd 内部类、父组件给子组件根元素加的类、动态拼接的类名。
// 所以输出要人工过一遍，不能当成硬性结论。
import fs from 'node:fs'
import path from 'node:path'

const SRC = 'frontend/src'
const files = []
function walk(d) {
  for (const e of fs.readdirSync(d, { withFileTypes: true })) {
    const p = path.join(d, e.name)
    if (e.isDirectory()) walk(p)
    else if (/\.(vue|css|ts)$/.test(e.name)) files.push(p)
  }
}
walk(SRC)

const defined = new Set()
const used = new Map() // class -> [file:line]

for (const f of files) {
  const text = fs.readFileSync(f, 'utf8')
  if (/\.(vue|css)$/.test(f)) {
    // <style> 块或 .css 文件里的选择器
    const styleBlocks = f.endsWith('.vue')
      ? [...text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1])
      : [text]
    for (const css of styleBlocks) {
      // 去掉声明块，只留选择器部分，避免把属性值里的词当成类名
      const selectors = css.replace(/\{[^}]*\}/g, '{}')
      for (const m of selectors.matchAll(/\.(-?[_a-zA-Z][\w-]*)/g)) defined.add(m[1])
    }
  }
  if (f.endsWith('.vue')) {
    const tpl = text.match(/<template>([\s\S]*)<\/template>/)
    if (!tpl) continue
    const t = tpl[1]
    // 行号必须按整个文件算：之前直接拿模板内的偏移量去 text 里数行，
    // 报出来的位置全是错的，照着它去找代码会被带偏
    const tplOffset = text.indexOf(t)
    const push = (name, idx) => {
      if (!name || !/^[a-zA-Z][\w-]*$/.test(name)) return
      if (/^(a|ant)-/.test(name)) return // antd 自己的类
      const line = text.slice(0, tplOffset + idx).split('\n').length
      if (!used.has(name)) used.set(name, [])
      used.get(name).push(f + ':' + line)
    }
    for (const m of t.matchAll(/class="([^"]*)"/g)) {
      m[1].split(/\s+/).forEach((c) => push(c, m.index))
    }
    for (const m of t.matchAll(/:class="\{([^}]*)\}/g)) {
      for (const k of m[1].matchAll(/(?:^|,)\s*'?([\w-]+)'?\s*:/g)) push(k[1], m.index)
    }
  }
}

const missing = [...used.entries()].filter(([c]) => !defined.has(c))
console.log('模板中使用但未定义的类：' + missing.length + ' 个')
console.log()
for (const [c, locs] of missing.sort()) {
  console.log('  ' + c.padEnd(24) + locs.join(', '))
}
