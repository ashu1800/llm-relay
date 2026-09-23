// 检查每个 a-table 的 scroll.x 与各列声明宽度是否对得上。
//
// 两种形态，分别校验：
//
//  1. **静态表**（列宽是数字字面量）：校验 scroll.x ≥ 各列宽之和。
//     本仓库的约定是 scroll.x 必须不小于列宽之和 —— 声明值和实际渲染宽度
//     对不上时，量出来的数就没法用来核对。
//
//     实测备注（antd-vue 4.2.6，2026-09-14 补）：只开 x 滚动（没有 scroll.y）时，
//     把密钥页从 1170 改回 1230 前后量了一遍，几何完全一样 —— 内容宽都是 1230
//     （列宽合计说了算），滚到最右时最后数据列右边界与固定操作列左边界都贴齐、
//     被盖住 0px。也就是说这里报出来的「偏小」在当前版本下并没有复现出可见的
//     压盖，"固定列会压住最后一列" 那句是照搬旧版的印象，未经复现。
//     这条检查保留的意义是「声明与实际一致」，而不是「避免可见的压盖」。
//
//  2. **运行时表**（列宽由脚本按内容测量后给出，如请求日志的 colW）：
//     scroll.x 绑的是各列宽之和的 computed，静态算不出数值。这时可查的不变量
//     是「**每一列都绑了那个表达式**」—— 混进一个裸数字，那一列就不会随测量
//     联动，表现为它永远停在声明值上（长内容照旧被截断）。
//     逐格的完整性由运行时脚本 scripts/check-log-columns.mjs 验证。
//
// 2026-09-23 修：此前只认数字字面量，请求日志表改成 colW.xxx 之后整张表被
// `continue` 静默跳过 ——「没报错」于是被读成「没问题」，而这张表恰恰是最需要
// 盯着的（它的列宽现在全靠运行时测量）。
//
// 扫描范围：views/ 与 components/（2026-09-16 扩）。
// 请求日志的表格随页面合并搬进了 components/RequestLogPanel.vue，
// 只扫 views/ 的话它会**静默**从检查里消失。
import fs from 'node:fs'
import path from 'node:path'

const DIRS = ['frontend/src/views', 'frontend/src/components']
const files = DIRS.flatMap((d) =>
  fs
    .readdirSync(d)
    .filter((x) => x.endsWith('.vue'))
    .map((x) => path.join(d, x))
)
console.log('扫描 ' + files.length + ' 个文件')

let problems = 0

for (const file of files) {
  const text = fs.readFileSync(file, 'utf8')
  const f = path.basename(file)
  let idx = 0
  let n = 0
  while ((idx = text.indexOf('<a-table', idx)) >= 0) {
    const end = text.indexOf('</a-table>', idx)
    const body = text.slice(idx, end < 0 ? text.length : end)
    idx += 8
    n++
    const sx = body.match(/:scroll="\{\s*x:\s*([^,}\s]+)/)
    if (!sx) continue
    const declared = sx[1]
    const widths = [...body.matchAll(/:width="([^"]+)"/g)].map((m) => m[1].trim())
    if (!widths.length) continue

    const label = (f + ' 表' + n).padEnd(30)

    if (/^\d+$/.test(declared)) {
      // ---- 静态表：scroll.x 与列宽之和都是已知数 ----
      const nonNumeric = widths.filter((w) => !/^\d+$/.test(w))
      if (nonNumeric.length) {
        problems++
        console.log(
          label + ' scroll.x=' + declared.padEnd(6) +
          '  <== 混用：scroll.x 是数字，但 ' + nonNumeric.length + ' 列写的是表达式（' +
          nonNumeric.join(', ') + '），静态算不出合计'
        )
        continue
      }
      const sum = widths.map(Number).reduce((a, c) => a + c, 0)
      const bad = Number(declared) < sum
      if (bad) problems++
      console.log(
        label + ' scroll.x=' + String(declared).padEnd(6) +
        ' 列宽合计=' + String(sum).padEnd(6) +
        (bad ? '  <== 偏小（声明与实际不一致；实测未见可见压盖，见文件头）' : '')
      )
    } else {
      // ---- 运行时表：列宽与 scroll.x 由同一份响应式状态驱动 ----
      const numeric = widths.filter((w) => /^\d+$/.test(w))
      const ok = numeric.length === 0
      if (!ok) problems++
      console.log(
        label + ' scroll.x=' + declared.padEnd(14) +
        ' 共 ' + widths.length + ' 列，动态 ' + (widths.length - numeric.length) +
        (ok
          ? '  <-- 运行时联动，逐格完整性见 check-log-columns.mjs'
          : '  <== 有 ' + numeric.length + ' 列写死数字（' + numeric.join(', ') + '），不会随测量联动')
      )
    }
  }
}

if (problems) console.log('\n' + problems + ' 处需要处理')
process.exit(problems ? 1 : 0)
