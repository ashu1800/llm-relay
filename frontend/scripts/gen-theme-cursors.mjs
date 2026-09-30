// 主题光标生成器（2026-09-30 随 12 套主题一起立）。
//
// 背景：光标是当图片加载的，没有 CSS 级联，SVG 里写不了 var()，颜色只能是字面量；
// 而本项目要求「每套主题的光标配色等于该主题实心块那一对」（fill = 按钮底色、
// stroke = 压在上面的文字色）。2 套主题时这四份文件是手写的，12 套就是 22 份 ——
// 手抄 18 份两两对应的色值必然会漂，而且漂了不会有任何报错，
// 只是光标停在上一套主题的颜色上（在相近的两色之间肉眼很难发现）。
//
// 所以：cursor-arrow-light.svg / cursor-hand-light.svg（锐角轮廓）与
//      cursor-arrow-dark.svg  / cursor-hand-dark.svg （圆角轮廓）
// 升格为**形状模板**，其余主题的文件由本脚本从对应族的模板重着色生成。
//
// 用法：
//   node scripts/gen-theme-cursors.mjs --write    # 生成/覆盖（改完主题色后跑这个）
//   node scripts/gen-theme-cursors.mjs --check    # 逐字节比对磁盘内容（CI/契约脚本用）
// 默认（不带参数）等同于 --check。
//
// 不生成的两类：
//   · light / dark —— 它们就是那两份手写模板，正文里有形状与尺寸的实测记录，
//     生成覆盖会把那些记录冲掉。
//   · 注册表里写了 cursorSource 的主题（目前只有 oled）—— 它的实心块那一对
//     与 dark 完全相同，再写一份字节一样的文件没有意义。
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { PUBLIC_DIR, parseRegistry, parseThemes, readColor } from './lib/theme-css.mjs'

/** 形状模板：按明暗族选（浅色族用锐角轮廓，深色族用圆角轮廓）。 */
export const TEMPLATE = {
  light: { arrow: 'cursor-arrow-light.svg', hand: 'cursor-hand-light.svg' },
  dark: { arrow: 'cursor-arrow-dark.svg', hand: 'cursor-hand-dark.svg' }
}

const TEMPLATE_IDS = new Set(['light', 'dark'])

/** 注释里不能出现连续两个减号（会让整个 SVG 解析失败），所以这段文案里不写 CSS 变量名。 */
function headerComment(id, kind, family, templateName) {
  const shape = family === 'light' ? '锐角轮廓' : '圆角轮廓'
  const what = kind === 'arrow' ? '默认箭头' : '交互手型'
  return (
    `  <!-- 由 frontend/scripts/gen-theme-cursors.mjs 生成，不要手改。\n` +
    `       形状取自形状模板 ${templateName}（${family === 'light' ? '浅色' : '深色'}族的${shape}），\n` +
    `       ${what}的配色取自 ${id} 主题「主色实心块」那一对：fill 是块底色、stroke 是压在上面的文字色。\n` +
    `       改完主题色跑 npm run gen:cursors 重新生成，check-contracts.mjs 会逐字节比对。 -->\n`
  )
}

/**
 * 从模板渲染出一份主题光标 SVG。
 * @param {string} templateName 模板文件名（在 public/ 下）
 * @param {string} id           主题 id
 * @param {'arrow'|'hand'} kind
 * @param {string} fill         实心块底色
 * @param {string} stroke       实心块文字色
 */
export function renderCursor(templateName, id, kind, fill, stroke, family) {
  const raw = readFileSync(join(PUBLIC_DIR, templateName), 'utf8')
  // 去掉模板里的注释（那是模板自己的实测记录，与被生成的这一份无关），
  // 再把压缩后的空白还原成缩进过的形状，保持与模板一致的排版。
  const body = raw
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/\n\s*\n/g, '\n')
    .trim()
  const out = body
    .replace(/\bfill="[^"]*"/, `fill="${fill}"`)
    .replace(/\bstroke="[^"]*"/, `stroke="${stroke}"`)
  const firstLineEnd = out.indexOf('\n')
  return out.slice(0, firstLineEnd + 1) + headerComment(id, kind, family, templateName) + out.slice(firstLineEnd + 1) + '\n'
}

/**
 * 生成所有**该由脚本维护**的光标文件。
 * @returns {Map<string, {content: string, id: string, kind: string}>} 文件名 → 内容
 */
export function renderAll(cssSource) {
  const themes = parseThemes(cssSource)
  const out = new Map()
  for (const entry of parseRegistry()) {
    if (TEMPLATE_IDS.has(entry.id)) continue
    if (entry.cursorSource) continue
    const theme = themes.get(entry.id)
    if (!theme) continue
    const fill = readColor(theme.vars, '--solid-primary-bg')
    const stroke = readColor(theme.vars, '--solid-primary-fg')
    if (!fill || !stroke) continue
    const family = entry.tone === 'dark' ? 'dark' : 'light'
    for (const kind of ['arrow', 'hand']) {
      const name = kind === 'arrow' ? `cursor-arrow-${entry.id}.svg` : `cursor-hand-${entry.id}.svg`
      out.set(name, {
        content: renderCursor(TEMPLATE[family][kind], entry.id, kind, fill, stroke, family),
        id: entry.id,
        kind
      })
    }
  }
  return out
}

/** CLI：--write 落盘，--check（默认）比对。 */
function main() {
  const mode = process.argv.includes('--write') ? 'write' : 'check'
  const files = renderAll()

  if (mode === 'write') {
    let changed = 0
    for (const [name, { content }] of files) {
      const path = join(PUBLIC_DIR, name)
      const before = existsSync(path) ? readFileSync(path, 'utf8') : null
      if (before === content) continue
      writeFileSync(path, content)
      changed++
      console.log(`  写入 public/${name}`)
    }
    console.log(`主题光标生成完成：${files.size} 份，其中 ${changed} 份有变化`)
    return
  }

  let failed = 0
  for (const [name, { content }] of files) {
    const path = join(PUBLIC_DIR, name)
    if (!existsSync(path)) {
      console.log(`  FAIL  缺文件 public/${name}（跑 npm run gen:cursors 生成）`)
      failed++
      continue
    }
    if (readFileSync(path, 'utf8') !== content) {
      console.log(`  FAIL  public/${name} 与主题色不同步（跑 npm run gen:cursors 重新生成）`)
      failed++
    }
  }
  if (failed) {
    console.error(`主题光标检查不通过：${failed}/${files.size} 份不同步`)
    process.exit(1)
  }
  console.log(`主题光标检查通过：${files.size} 份与各主题实心块配色一致`)
}

// 被 check-contracts.mjs import 时不要执行 CLI（那个脚本自己做逐项断言）
if (process.argv[1] && process.argv[1].endsWith('gen-theme-cursors.mjs')) main()
