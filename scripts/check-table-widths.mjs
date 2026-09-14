// 检查每个 a-table 的 scroll.x 是否小于各列宽度之和。
//
// scroll.x 是表格的最小宽度，本仓库的约定是它必须不小于各列宽度之和：
// 声明值和实际渲染宽度对不上时，量出来的数就没法用来核对。
//
// 实测备注（antd-vue 4.2.6，2026-09-14 补）：只开 x 滚动（没有 scroll.y）时，
// 把密钥页从 1170 改回 1230 前后量了一遍，几何完全一样 —— 内容宽都是 1230
// （列宽合计说了算），滚到最右时最后数据列右边界与固定操作列左边界都贴齐、
// 被盖住 0px。也就是说这里报出来的「偏小」在当前版本下并没有复现出可见的
// 压盖，"固定列会压住最后一列" 那句是照搬旧版的印象，未经复现。
// 这条检查保留的意义是「声明与实际一致」，而不是「避免可见的压盖」。
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
        (declared < sum ? '  <== 偏小（声明与实际不一致；实测未见可见压盖，见文件头）' : '')
    )
  }
}
