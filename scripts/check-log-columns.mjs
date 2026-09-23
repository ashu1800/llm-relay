// 请求日志表格的列宽契约：每一格的内容都要完整显示。
//
// 为什么必须运行时验证：列宽现在是脚本按内容量出来的（RequestLogPanel 的
// remeasureColumns）。静态检查只能保证「测量代码接对了」（见
// scripts/check-table-widths.mjs 与 frontend/scripts/check-contracts.mjs），
// 而「量出来的宽度是否真的装得下内容」只有渲染之后才知道 —— 而且这类问题
// 不会报错：内容被省略号截掉，页面一切正常。
//
// 判据（对当前页每一行、每一列）：
//   锚点元素的 scrollWidth ≤ clientWidth  →  内容没有被截断
// 锚点就是测量用的那几个，**从 CONTENT_MEASURE 读**（不在这里再抄一份）。
// inline 元素（.txt-cell）没有 clientWidth，它的溢出由外层单元格兜住，
// 所以单元格自身也单独判一次。
//
// 另外两条是**报告**而不是判据：表格总宽 vs 容器宽（内容比容器宽时横向滚动是
// 设计允许的），以及横向滚到最右端时右侧固定列遮挡了多少像素（遮挡大于 0
// 会让最后一列的内容被压在固定列下面，那才是要判失败的）。
//
// 用法：先带调试端口起一个 Chrome（见 README「在 Windows 上跑这些脚本」），然后
//   node scripts/check-log-columns.mjs [base] [视口宽,视口宽...] [每页条数,...]
// 例：node scripts/check-log-columns.mjs http://127.0.0.1:8888 1440 20
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const base = process.argv[2] || 'http://127.0.0.1:8888'
const widths = (process.argv[3] || '1280,1440,1600').split(',').map(Number)
const pageSizes = (process.argv[4] || '20,50,100').split(',').map(String)

// 每页条数存在 localStorage 里，键名 = persistedChoice 的前缀 + 面板里的常量。
// 从源码读而不是抄一份：抄的那份会在改名后静默失效 —— 脚本照样「全通过」，
// 而其实每一轮测的都是同一个 pageSize（第一版就踩了两次：先是漏了前缀，
// 后是 navigate 到同一 URL 不重新加载）。
const panelSrcForKeys = readFileSync(join('frontend', 'src', 'components', 'RequestLogPanel.vue'), 'utf8')
const choiceSrcForKeys = readFileSync(join('frontend', 'src', 'utils', 'persistedChoice.ts'), 'utf8')
const sizeKey =
  (choiceSrcForKeys.match(/PREFIX\s*=\s*'([^']+)'/) || [, ''])[1] +
  (panelSrcForKeys.match(/PAGE_SIZE_KEY\s*=\s*'([^']+)'/) || [, ''])[1]
if (!sizeKey) {
  console.error('读不到每页条数的 localStorage 键名（RequestLogPanel 的 PAGE_SIZE_KEY）')
  process.exit(1)
}

// 测量锚点也从源码读 CONTENT_MEASURE：脚本里再抄一份的话，源码换了锚点
// （就像密钥列从 .group-tag 换成 .key-tag 这次）脚本还在盯旧的那个，
// 于是「锚点只命中一列」这条断言看着在跑，其实盯的是一列都不命中的旧名字。
const ANCHORS = [
  ...((panelSrcForKeys.match(/const CONTENT_MEASURE[\s\S]*?\n\}/) || [''])[0].matchAll(/sel:\s*'([^']+)'/g)),
].map((m) => m[1])
if (ANCHORS.length < 6) {
  console.error(`读不到列宽测量锚点（CONTENT_MEASURE 里只解析出 ${ANCHORS.length} 个）`)
  process.exit(1)
}

const ver = await (await fetch('http://127.0.0.1:9222/json/version')).json()
const ws = new WebSocket(ver.webSocketDebuggerUrl)
await new Promise((r, j) => {
  ws.onopen = r
  ws.onerror = j
})
let id = 0
const pending = new Map()
ws.onmessage = (e) => {
  const m = JSON.parse(e.data)
  if (m.id && pending.has(m.id)) {
    const p = pending.get(m.id)
    pending.delete(m.id)
    m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result)
  }
}
const send = (method, params, sessionId) => {
  const mid = ++id
  return new Promise((resolve, reject) => {
    pending.set(mid, { resolve, reject })
    ws.send(JSON.stringify({ id: mid, method, params: params || {}, sessionId }))
  })
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
for (const d of ['Page', 'Runtime']) await send(d + '.enable', {}, sessionId)
const evalJs = async (expr) => {
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true }, sessionId)
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300))
  return r.result.value
}

// 页面内探针：把「内容有没有被截断」量清楚。
// 锚点名单与 RequestLogPanel 的 CONTENT_MEASURE 同源 —— 那边是测量的输入，
// 这边是测量的验收，两份都盯着同一批元素。
const PROBE = `(() => {
  const wrap = document.querySelector('.log-table')
  if (!wrap) return JSON.stringify({ error: '页面上没有 .log-table（列表还没渲染？）' })
  const rows = wrap.querySelectorAll('.ant-table-tbody tr[data-row-key]').length
  const anchors = ['.model-cell', '.chan-name', '.tk-line', '.dur-line', '.txt-cell', '.spd-cell', '.group-tag']
  const overflow = []
  for (const sel of anchors) {
    for (const el of wrap.querySelectorAll('.ant-table-tbody ' + sel)) {
      if (el.clientWidth === 0) continue   // inline 元素没有 clientWidth，交给外层单元格判
      const over = el.scrollWidth - el.clientWidth
      if (over > 1) overflow.push(sel + ' 溢出 ' + over + 'px: ' + (el.textContent || '').trim().slice(0, 24))
    }
  }
  const cells = [...wrap.querySelectorAll('.ant-table-tbody tr[data-row-key] td')]
  // 溢出的是哪一格要报出来：只说「有 20 格溢出」等于让人自己去猜是哪一列。
  //
  // 这里**不能**用 td.scrollWidth 判：固定列是 position: sticky，在可横向滚动的
  // 容器里它的 scrollWidth 会虚报 —— 实测（1440 视口、可滚 30px）时间列每行都报
  // 「溢出 30px」，而那一列是定长格式、列宽 155 从未截断过。虚报量恰好多出
  // 「可滚动量」，所以判据换成 Range：它量的是内容自己的布局矩形，与 sticky 偏移
  // 无关。
  //
  // 反过来说，Range 判据**测不出** ellipsis 截断（被 overflow: hidden 裁掉的文本
  // 不参与布局），那类问题由上面锚点级的 scrollWidth 判据兜住 —— 两者互补。
  const perRow = rows ? cells.length / rows : 0
  const cellOverList = cells
    .map((td, i) => {
      const cs = getComputedStyle(td)
      const avail = td.clientWidth - parseFloat(cs.paddingLeft || 0) - parseFloat(cs.paddingRight || 0)
      const range = document.createRange()
      range.selectNodeContents(td)
      const contentW = range.getBoundingClientRect().width
      return {
        col: perRow ? i % perRow : i,
        over: Math.round(contentW - avail),
        txt: (td.textContent || '').trim().slice(0, 18),
        width: Math.round(td.getBoundingClientRect().width),
      }
    })
    .filter((x) => x.over > 1)
  const cellOver = cellOverList.length
  const container = wrap.querySelector('.ant-table-body') || wrap.querySelector('.ant-table-content')
  const table = wrap.querySelector('.ant-table-body table') || wrap.querySelector('.ant-table-content table')
  const cw = container ? Math.round(container.getBoundingClientRect().width) : -1
  const tw = table ? Math.round(table.getBoundingClientRect().width) : -1
  // 横向滚到最右，再量右侧固定列压住了最后一列多少像素
  let cover = -1
  if (container && container.scrollWidth > container.clientWidth + 1) {
    container.scrollLeft = container.scrollWidth
    const fixedRight = cells.find((td) => td.classList.contains('ant-table-cell-fix-right'))
    const dataCells = cells.filter((td) => !td.classList.contains('ant-table-cell-fix-right'))
    const last = dataCells[dataCells.length - 1]
    if (fixedRight && last) {
      cover = Math.max(0, Math.round(last.getBoundingClientRect().right - fixedRight.getBoundingClientRect().left))
    }
  }
  const colW = [...wrap.querySelectorAll('.ant-table-thead th')].map((th) => Math.round(th.getBoundingClientRect().width))
  // 列**内部**的对齐：速度列的「流」胶囊要落在同一条纵线上。
  // 速度是「输出词元 ÷ 耗时」，位数随请求变，而这一块在单元格里居中 ——
  // 数值轨道不定宽时整块宽度逐行不同，胶囊就跟着左右跳（站主 2026-09-23 反馈）。
  // 判据：所有胶囊的左缘只允许有一个取值。用左缘而不是宽度：宽度本来就该一样，
  // 而「在不在一条线上」问的是位置。
  const uniqRound = (xs) => [...new Set(xs.map((v) => Math.round(v * 10) / 10))]
  const pillLefts = uniqRound([...wrap.querySelectorAll('.ant-table-tbody .stream-pill')].map((el) => el.getBoundingClientRect().left))
  // 数值只统计**流式行**里的：非流式行没有胶囊，它的数值本来就该自己居中
  // （整块居中的结果），与流式行不在一条线上是设计如此，不是缺陷
  const spdLefts = uniqRound(
    [...wrap.querySelectorAll('.ant-table-tbody .spd-cell:has(.stream-pill) .spd')].map((el) => el.getBoundingClientRect().left),
  )
  const pillCount = wrap.querySelectorAll('.ant-table-tbody .stream-pill').length
  // 状态码那一格：a-tag 在单元格里要**真的居中**。
  // 量的是「标签到单元格内容区左缘」与「到右缘」两段留白 —— 只看标签宽没用，
  // 偏移恰恰来自标签自己带的边距：antd 给 .ant-tag 默认加了 margin-inline-end: 8px
  // （那是给多个标签并排用的），而这一格只有一个标签、又要居中，那 8px 会被算进
  // 内容宽度，胶囊因此偏左 4px（站主 2026-09-23 反馈「200 的状态码轻微向左偏移」）。
  const tagCells = [...wrap.querySelectorAll('.ant-table-tbody tr[data-row-key] td')].filter((td) => td.querySelector('.ant-tag'))
  const tagOffsets = tagCells.map((td) => {
    const t = td.querySelector('.ant-tag').getBoundingClientRect()
    const c = td.getBoundingClientRect()
    const cs = getComputedStyle(td)
    const padL = parseFloat(cs.paddingLeft || 0)
    const padR = parseFloat(cs.paddingRight || 0)
    return {
      左: Math.round((t.left - (c.left + padL)) * 10) / 10,
      右: Math.round((c.right - padR - t.right) * 10) / 10,
    }
  })
  // 每个测量锚点在表体里只能落在一列上。
  // remeasureColumns 是**全局查询**（.ant-table-tbody 拼上锚点选择器），锚点跨列命中就会把
  // 别列的内容算进本列：密钥列原来用 '.group-tag'，而模型名与密钥名都用 GroupTag
  // 渲染，于是模型列那枚更宽的胶囊（deepseek-v4.1-flash ≈ 123px）被算进了密钥列，
  // 密钥列常年 145px，而内容只要 71px（站主 2026-09-23 反馈「密钥列为什么那么宽」）。
  // 静态检查看不出这种事 —— 只有真渲染出来，才知道哪些列被同一个选择器命中了。
  const anchorColumns = {}
  for (const sel of ${JSON.stringify(ANCHORS)}) {
    const cols = new Set()
    for (const tr of wrap.querySelectorAll('.ant-table-tbody tr[data-row-key]')) {
      [...tr.querySelectorAll('td')].forEach((td, i) => {
        if (td.querySelector(sel)) cols.add(i)
      })
    }
    anchorColumns[sel] = [...cols].sort((a, b) => a - b)
  }
  // 诊断用：每页条数是存在 localStorage 里的，读回来确认这轮真的切过去了 ——
  // 否则整套矩阵测的都是同一个 pageSize（第一版就踩了：9 组全是 20 行）
  let stored = 'N/A'
  try { stored = String(localStorage.getItem('${sizeKey}')) } catch (e) { stored = 'ERR' }
  const pagerText = (document.querySelector('.ant-pagination-options') || {}).textContent || ''
  return JSON.stringify({ rows, cells: cells.length, cw, tw, overflow: overflow.slice(0, 8), overflowN: overflow.length, cellOver, cellOverList: cellOverList.slice(0, 6), cover, colW, stored, pagerText: pagerText.trim(), pillLefts, spdLefts, pillCount, tagOffsets, anchorColumns })
})()`

let failed = 0
const check = (name, ok, detail) => {
  if (!ok) failed++
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  —— ' + detail : ''}`)
}

let primed = false
for (const w of widths) {
  for (const ps of pageSizes) {
    console.log(`\n=== 视口 ${w}px × 每页 ${ps} 条 ===`)
    await send('Emulation.setDeviceMetricsOverride', { width: w, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId)
    if (!primed) {
      // 先落到同源页面，之后才能写 localStorage（每页条数是存在那儿的）
      await send('Page.navigate', { url: base + '/console/dashboard' }, sessionId)
      await sleep(6000)
      primed = true
    }
    await evalJs(`localStorage.setItem('${sizeKey}', '${ps}')`)
    // 必须 reload 而不是 navigate：navigate 到**同一个 URL** 时 Chrome 不会重新
    // 执行页面脚本（第一版就是这么写的，9 组矩阵全是 20 行，看着「全通过」
    // 其实只测了一个 pageSize）
    await send('Page.reload', { ignoreCache: true }, sessionId)
    await sleep(7000)

    const raw = await evalJs(PROBE)
    const d = JSON.parse(raw)
    if (d.error) {
      check('页面探针', false, d.error)
      continue
    }
    console.log(`  行 ${d.rows}  单元格 ${d.cells}  容器 ${d.cw}  表格 ${d.tw}  ${d.tw > d.cw ? '横向可滚 ' + (d.tw - d.cw) + 'px' : '一屏装下'}`)
    console.log(`  列宽 ${d.colW.join(' / ')}   分页「${d.pagerText}」 localStorage=${d.stored}`)
    check('当前页有数据行', d.rows > 0, `${d.rows} 行`)
    // 行数必须真的等于这一轮的 pageSize：否则整组断言跑在别的行数上，
    // 「全通过」就是假的（库里有两万多条日志，不可能是数据不够）
    check(`每页条数生效（${ps} 行）`, d.rows === Number(ps), `实际 ${d.rows} 行，分页显示「${d.pagerText}」`)
    check('没有一格内容被截断', d.overflowN === 0, d.overflow.slice(0, 4).join(' | '))
    check(
      '单元格自身没有横向溢出',
      d.cellOver === 0,
      d.cellOverList.map((x) => `第 ${x.col} 列宽 ${x.width} 溢出 ${x.over}px「${x.txt}」`).join(' | '),
    )
    check('横向滚到最右时右侧固定列不遮挡内容', d.cover <= 0 || d.cover === -1, d.cover > 0 ? `遮挡 ${d.cover}px` : '')
    check(
      '速度列的「流」胶囊都落在同一条纵线上',
      d.pillCount < 2 || d.pillLefts.length === 1,
      `${d.pillCount} 枚胶囊，左缘 ${d.pillLefts.length} 个取值：${d.pillLefts.slice(0, 6).join(' / ')}`,
    )
    check(
      '速度列流式行的数值起点也一致',
      d.spdLefts.length <= 1,
      `数值左缘 ${d.spdLefts.length} 个取值：${d.spdLefts.slice(0, 6).join(' / ')}`,
    )
    // 状态码标签左右留白必须相等（差 1px 以内算相等：亚像素舍入）
    const tagBad = d.tagOffsets.filter((o) => Math.abs(o.左 - o.右) > 1)
    check(
      '状态码标签在单元格里居中',
      tagBad.length === 0,
      tagBad.length
        ? `${tagBad.length}/${d.tagOffsets.length} 格偏移，例如 左 ${tagBad[0].左}px / 右 ${tagBad[0].右}px`
        : `${d.tagOffsets.length} 格，例如 左 ${d.tagOffsets[0] ? d.tagOffsets[0].左 : '-'}px / 右 ${d.tagOffsets[0] ? d.tagOffsets[0].右 : '-'}px`,
    )
    // 锚点跨列命中：静态检查看不出来，只有真渲染出来才知道哪几列被同一个选择器命中
    const crossed = Object.entries(d.anchorColumns).filter(([, cols]) => cols.length > 1)
    check(
      '每个列宽测量锚点只命中一列',
      crossed.length === 0,
      crossed.length
        ? crossed.map(([sel, cols]) => `${sel} 命中第 ${cols.join('、')} 列`).join('；')
        : Object.entries(d.anchorColumns).map(([sel, cols]) => `${sel}→第${cols.length ? cols[0] : '?'}列`).join(' '),
    )
  }
}

await send('Target.closeTarget', { targetId })
ws.close()
console.log('')
console.log(failed === 0 ? '全部通过' : `${failed} 项未通过`)
process.exit(failed === 0 ? 0 : 1)
