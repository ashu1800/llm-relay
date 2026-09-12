// 检查每个 a-table 的 scroll.x 是否小于各列宽度之和。
//
// scroll.x 是表格的最小宽度；声明值偏小时，固定在右侧的列会盖住最后一列，
// 表现为表头被截断、单元格内容被压住 —— 不报错，只能靠眼睛发现。
import fs from 'node:fs'
import path from 'node:path'

const SRC = 'frontend/src/views'
const files = fs.readdirSync(SRC).filter((x) => x.endsWith('.vue'))
console.log('扫描 ' + files.length + ' 个文件')

for (const f of files) {
  const text = fs.readFileSync(path.join(SRC, f), 'utf8')
  let idx = 0
  let n = 0
  while ((idx = text.indexOf('<a-table', idx)) >= 0) {
    const end = text.indexOf('</a-table>', idx)
    const body = text.slice(idx, end < 0 ? text.length : end)
    idx += 8
    n++
    const sx = body.match(/:scroll="\{\s*x:\s*(\d+)/)
    if (!sx) continue
    const widths = [...body.matchAll(/:width="(\d+)"/g)].map((m) => Number(m[1]))
    if (!widths.length) continue
    const declared = Number(sx[1])
    const sum = widths.reduce((a, c) => a + c, 0)
    console.log(
      (f + ' 表' + n).padEnd(26) +
        ' scroll.x=' + String(declared).padEnd(6) +
        ' 列宽合计=' + String(sum).padEnd(6) +
        (declared < sum ? '  <== 偏小，固定列会压住最后一列' : '')
    )
  }
}
