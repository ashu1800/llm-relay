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

// 「列宽 vs 内容」判据要用的 (表头文字, 锚点, 固定前缀宽) 三元组。
//
// 这一份**从源码解析**而不是在脚本里抄：抄的那份会在源码改了锚点或 pad 之后
// 静默失效（本文件上面那段注释已经为 ANCHORS 踩过一次同样的坑）。
// CONTENT_MEASURE 的形态是 `key: { sel: '...', pad: N }`，键名与表头文字
// 的对应关系来自 COL_LABELS。
const COL_LABELS = {
  model: '模型',
  channel: '渠道',
  tokens: '词元',
  elapsed: '任务耗时',
  cost: '费用',
  speed: '速度',
  key: '密钥',
}
const CONTENT_MEASURES = [
  ...((panelSrcForKeys.match(/const CONTENT_MEASURE[\s\S]*?\n\}/) || [''])[0]).matchAll(
    /(\w+):\s*\{\s*sel:\s*'([^']+)',\s*pad:\s*(\d+)/g,
  ),
]
  .map((m) => ({ name: COL_LABELS[m[1]], sel: m[2], pad: Number(m[3]) }))
  .filter((m) => m.name)
if (CONTENT_MEASURES.length < 6) {
  console.error(`读不到列宽测量项（只解析出 ${CONTENT_MEASURES.length} 项，需要 6 项以上）`)
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
  // 列宽 vs 内容所需宽度（2026-09-24 新增）。
  //
  // 上面那些判据全是「有没有被截断」，方向是**列太窄**。而反方向的问题
  //（列比内容宽一大截）一样违反 ui-spec 第 17 条「按内容动态调整列宽」，
  // 却一条断言都拦不住 —— 它看起来不像故障，只是「排版松」。
  //
  // 实测来源：渠道列的 [下限] 是 2026-09-16 人工拍的最坏情况常量 130，
  // 2026-09-23 改成按内容量之后它被原样留下，于是当前页渠道名只要 47px
  //（+图标 24 +内边距 16 +呼吸 6 = 93）时，列仍占 137px，**44px 全是空白**，
  // 而同期其它列只空 5-10px。站主一眼看出来「渠道列宽不对」。
  //
  // 量出来供**报告**用（不做阈值判定 —— 原因见下方断言处的注释：
  // 空白量随视口与内容浮动，没有稳定的门槛；真正的判据放在静态契约里）。
  const contentNeed = {}
  // 用**离屏 span** 量每个锚点里文字的真实宽度（2026-09-24 新增）。
  //
  // 为什么需要它：上面那条「没有一格内容被截断」用的是
  // scrollWidth ≤ clientWidth，而 text-overflow: ellipsis 的列上
  // 这条判据不敏感 —— 被省略号截断时 scrollWidth 会被压到接近 clientWidth。
  // 实测（1280 视口、渠道名 CodingPlan）：clientW=74 / scrollW=75 判为「没溢出」，
  // 而离屏量出的真实文字宽是 82px，名字明明被截了。
  //
  // 这个坑让渠道列的问题从一开始就查不出来：列宽不合适 → 名字被省略 →
  // scrollWidth 也跟着变小 → 断言反而更「绿」。
  //
  // 做法：拿同一个元素的 computed font 建一个 nowrap 的离屏 span，量文字宽度，
  // 再和该元素实际可用宽度（clientWidth）比。只对**文字类**锚点做
  //（.chan-name / .key-tag / .spd-cell / .txt-cell 这类；图标/胶囊类的
  //  .model-cell 内部还有子元素，文字宽不等于内容宽，不在此列）。
  const textEllipsis = []
  for (const m of ${JSON.stringify(CONTENT_MEASURES)}) {
    if (!['渠道', '密钥', '速度', '费用'].includes(m.name)) continue
    const els = [...wrap.querySelectorAll('.ant-table-tbody ' + m.sel)]
    if (!els.length) continue
    const probe = document.createElement('span')
    const cs = getComputedStyle(els[0])
    probe.style.cssText = 'position:absolute;visibility:hidden;white-space:nowrap;left:-9999px;font:' + cs.font
    document.body.appendChild(probe)
    let worst = null
    for (const el of els) {
      const text = (el.innerText || '').trim()
      if (!text) continue
      probe.textContent = text
      const need = Math.ceil(probe.getBoundingClientRect().width)
      // 可用宽度取**单元格**而不是锚点自己：.txt-cell（费用）是 display:inline，
      // 没有 clientWidth（恒为 0），拿它比会得出「需 73px 只有 0px」这种假失败。
      // 单元格减去左右内边距才是内容真正能用的宽度。
      const cell = el.closest('td')
      let have
      if (cell) {
        const ccs = getComputedStyle(cell)
        have = Math.floor(cell.clientWidth - parseFloat(ccs.paddingLeft || 0) - parseFloat(ccs.paddingRight || 0))
      } else {
        have = el.clientWidth
      }
      if (need > have + 1 && (!worst || need - have > worst.over)) {
        worst = { col: m.name, text: text.slice(0, 20), need, have, over: need - have }
      }
    }
    probe.remove()
    if (worst) textEllipsis.push(worst)
  }

  for (const m of ${JSON.stringify(CONTENT_MEASURES)}) {
    let max = 0
    wrap.querySelectorAll('.ant-table-tbody ' + m.sel).forEach((el) => {
      const w = Math.ceil(el.getBoundingClientRect().width)
      if (w > max) max = w
    })
    if (max > 0) contentNeed[m.name] = max + m.pad
  }
  const headByTitle = {}
  ;[...wrap.querySelectorAll('.ant-table-thead th')].forEach((th) => {
    headByTitle[th.innerText.replace(/\\s+/g, ' ').trim()] = Math.round(th.getBoundingClientRect().width)
  })
  const slack = Object.keys(contentNeed).map((name) => ({
    name,
    need: contentNeed[name],
    got: headByTitle[name] === undefined ? -1 : headByTitle[name],
    slack: headByTitle[name] === undefined ? -1 : headByTitle[name] - contentNeed[name],
  }))
  return JSON.stringify({ rows, cells: cells.length, cw, tw, overflow: overflow.slice(0, 8), overflowN: overflow.length, cellOver, cellOverList: cellOverList.slice(0, 6), cover, colW, stored, pagerText: pagerText.trim(), pillLefts, spdLefts, pillCount, tagOffsets, anchorColumns, slack, textEllipsis })
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
    // 省略号列的截断：上面那条对它们**不敏感**（scrollWidth 被省略号压小），
    // 见 PROBE 里 textEllipsis 的注释。这里拿离屏量出的文字宽 vs 元素可用宽比。
    check(
      '省略号列的文字没有被截（离屏量宽判定）',
      (d.textEllipsis || []).length === 0,
      (d.textEllipsis || [])
        .map((x) => `${x.col}「${x.text}」需 ${x.need}px 只有 ${x.have}px（差 ${x.over}px）`)
        .join(' | '),
    )
    check(
      '单元格自身没有横向溢出',
      d.cellOver === 0,
      d.cellOverList.map((x) => `第 ${x.col} 列宽 ${x.width} 溢出 ${x.over}px「${x.txt}」`).join(' | '),
    )
    check('横向滚到最右时右侧固定列不遮挡内容', d.cover <= 0 || d.cover === -1, d.cover > 0 ? `遮挡 ${d.cover}px` : '')
    // 各列空白量的**报告**（不是判据）。
    //
    // 为什么不做成判据：我试过两种写法，都不可靠 ——
    //   · 绝对阈值 24px：所有列都空 30+px（antd 按比例分配容器余量），全部误判
    //   · 绝对阈值 40px：旧代码的渠道列空 38px，放过了真问题
    //   · 相对中位数 10px：旧代码渠道列 38px vs 中位数 30px，只差 8px，同样漏掉
    // 空白量随视口与当前页内容浮动，没有稳定的绝对或相对门槛。
    //
    // 真正能精确钉住的是**下限常量本身**（「某列的下限远高于它内容所需」
    // 这种缺陷就写在那行常量里），那条由 frontend/scripts/check-contracts.mjs
    // 静态检查守（源码即判据，不随渲染浮动）。
    // 这里把实测空白打出来供人核对 —— 站主当初就是「一眼看出渠道列宽不对」，
    // 数字留在输出里，下次改列宽时能立刻对比。
    const slacks = (d.slack || []).filter((s) => s.slack >= 0)
    if (slacks.length) {
      console.log(`  各列空白 ${slacks.map((s) => `${s.name} ${s.slack}px`).join(' / ')}`)
    }
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
