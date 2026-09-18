// 光标审计：把每个页面上所有元素的计算 cursor 统计一遍，确认「该是自定义
// 光标的地方没有退回系统光标」。
//
// 为什么需要这个脚本：CSS 的 cursor 不进截图 —— 光标画在页面之外，截图里
// 永远看不见。也就是说「样式写了但没生效」这件事在截图与人工核对里都是隐形的：
// 少写一个选择器、antd 升级换了 class 名、URL 写错导致图片 404，
// 表现都只是那一处悄悄变回系统箭头，没有任何报错。
//
// 用法:
//   node scripts/audit-cursors.mjs [baseUrl] [light|dark|both]
//   baseUrl 默认 http://127.0.0.1:8888（容器里跑的那个实例）
//   开发态用 http://127.0.0.1:5173（先 npm run dev）
//   第三个参数默认 both —— 每个用例在浅色与深色下各跑一遍。
//
// 前置：一个开着调试端口的 Chrome
//   chrome --remote-debugging-port=9222 --user-data-dir=.chrome-profile
//
// 判定：允许出现的光标值是**当前主题那一套**自定义箭头/手型，以及刻意保留的
// grab / grabbing / not-allowed / help / text / auto（沿用系统的功能性光标，
// 理由见 frontend/src/styles/theme.css 的「自定义光标」段）。
// 出现系统默认箭头（default）或系统手型（pointer）即判失败 ——
// 但 pointer-events: none 的元素不算：鼠标根本碰不到它们，antd 的那些隐藏
// input 就属于这一类。
//
// 主题切换靠预置 localStorage 里的 llm-relay-theme（与前端 store 同一个键），
// 必须在导航**之前**用 Page.addScriptToEvaluateOnNewDocument 注入：
// store 在页面初始化时就把 data-theme 写上了，导航之后再改会漏掉首屏那批元素，
// 而「首屏用了另一个主题的光标」恰恰是最该被发现的情况。
import http from 'node:http'
import { WebSocket } from 'ws'

const CDP_PORT = 9222
const base = (process.argv[2] || 'http://127.0.0.1:8888').replace(/\/$/, '')
const themeArg = (process.argv[3] || 'both').toLowerCase()
if (!['light', 'dark', 'both'].includes(themeArg)) {
  console.error(`第三个参数只能是 light / dark / both，收到 ${themeArg}`)
  process.exit(2)
}
const THEMES = themeArg === 'both' ? ['light', 'dark'] : [themeArg]

// 需要点的用例：弹窗、抽屉、下拉都是按需渲染的，不点开就完全不在 DOM 里，
// 只测静态页面会漏掉它们（实测：渠道弹窗里 246 个可点元素一个都不在静态页面里）
//
// needsData：入口按钮在表格行里，而空库（刚装好、还没有请求日志或渠道）时
// 表格是空的、按钮根本不存在。这种用例报 SKIP 而不是 FAIL —— 但要显式打出来，
// 不能静默跳过：真实环境里按钮改名/消失也应该看得见。
const CASES = [
  { name: '数据看板', path: '/console/dashboard' },
  { name: '请求日志列表', path: '/console/dashboard' },
  { name: '密钥信息', path: '/console/keys' },
  { name: '渠道管理', path: '/console/channels' },
  { name: '分组管理', path: '/console/groups' },
  { name: '代理管理', path: '/console/proxies' },
  { name: '系统设置', path: '/console/system' },
  { name: '日志详情抽屉 + 下拉', path: '/console/dashboard', click: '详情', needsData: '至少一条请求日志' },
  { name: '渠道白名单抽屉', path: '/console/channels', click: '模型', needsData: '至少一条渠道' },
  { name: '渠道新建弹窗', path: '/console/channels', click: '新建渠道' },
  { name: '分组新建弹窗', path: '/console/groups', click: '新建分组' },
  { name: '密钥新建弹窗', path: '/console/keys', click: '新建密钥' }
]

// 审计要按主题分别确认，所以浏览器里要先确认主题真的切过去了。
// 只信 localStorage 不够：store 可能因为历史遗留值走了别的分支，
// 而 data-theme 属性才是样式实际生效的依据。
const CHECK_THEME = `(() => JSON.stringify({
  attr: document.documentElement.getAttribute('data-theme') || '(无)',
  stored: localStorage.getItem('llm-relay-theme'),
}))()`

// 每个主题各有自己的文件名，所以按当前主题生成 classify。
// 关键一条：**另一个主题的文件名必须判失败** ——
// 只检查「是不是自定义光标」的话，深色下用到浅色那套（例如深色块漏配了
// 覆盖，从这里继承 :root 的值）会被当成通过，而那正是这次重构要防的问题。
const auditExpr = (theme) => `(() => {
  const mineArrow = 'cursor-arrow-${theme}.svg'
  const mineHand = 'cursor-hand-${theme}.svg'
  const otherArrow = 'cursor-arrow-${theme === 'light' ? 'dark' : 'light'}.svg'
  const otherHand = 'cursor-hand-${theme === 'light' ? 'dark' : 'light'}.svg'
  const classify = (v) => {
    if (v.includes(mineArrow)) return '自定义箭头'
    if (v.includes(mineHand)) return '自定义手型'
    // 串主题：本主题下拿到了另一套图形，单独归一类，不与系统光标混在一起报
    if (v.includes(otherArrow)) return '串主题箭头(' + otherArrow + ')'
    if (v.includes(otherHand)) return '串主题手型(' + otherHand + ')'
    return v
  }
  const out = {}
  for (const el of document.querySelectorAll('*')) {
    const cs = getComputedStyle(el)
    const k = classify(cs.cursor)
    if (!out[k]) out[k] = { total: 0, inert: 0, sigs: {} }
    const b = out[k]
    b.total++
    if (cs.pointerEvents === 'none') b.inert++
    let cls = ''
    if (typeof el.className === 'string' && el.className.trim()) {
      cls = '.' + el.className.trim().split(/\\s+/).filter((c) => !/^css-/.test(c)).slice(0, 4).join('.')
    }
    const sig = el.tagName.toLowerCase() + cls
    b.sigs[sig] = (b.sigs[sig] || 0) + 1
  }
  return out
})()`

// 四份资源都要探测：只测当前主题那两份的话，另一主题的图形 404 了也看不见 ——
// 而那种情况直到用户切主题才会暴露。只查 200 会被 SPA 回退骗过：
// 后端对任何未知路径都回 index.html + 200，图片解码失败后光标静默退回系统光标。
const CURSOR_FILES = [
  'cursor-arrow-light.svg',
  'cursor-hand-light.svg',
  'cursor-arrow-dark.svg',
  'cursor-hand-dark.svg',
]

const PROBE_ASSETS = `(async () => {
  const out = {}
  for (const f of ${JSON.stringify(CURSOR_FILES)}) {
    try {
      const r = await fetch('/' + f, { cache: 'no-store' })
      const body = r.ok ? await r.text() : ''
      // 顺带把填充/描边读回来：文件在不在、颜色对不对是两件事，
      // 而颜色错了（例如两套都指向同一个色）在界面上极易被忽略
      const fill = (body.match(/\\bfill="(#[0-9a-fA-F]{3,8})"/) || [])[1] || '?'
      const stroke = (body.match(/\\bstroke="(#[0-9a-fA-F]{3,8})"/) || [])[1] || '?'
      out[f] = r.status + ' ' + r.headers.get('content-type') + ' fill=' + fill + ' stroke=' + stroke
    } catch (e) { out[f] = 'fetch 失败 ' + e.message }
  }
  return out
})()`

function httpGet(path) {
  return new Promise((ok, fail) => {
    const req = http.request({ host: '127.0.0.1', port: CDP_PORT, path, method: 'GET' }, (res) => {
      let buf = ''
      res.setEncoding('utf8')
      res.on('data', (c) => (buf += c))
      res.on('end', () => ok(buf))
    })
    req.on('error', fail)
    req.setTimeout(15000, () => req.destroy(new Error('连接调试端口超时')))
    req.end()
  })
}

async function main() {
  let version
  try {
    version = JSON.parse(await httpGet('/json/version'))
  } catch (e) {
    console.error(`连不上 127.0.0.1:${CDP_PORT} 的调试端口：${e.message}`)
    console.error('先起一个带调试端口的 Chrome，见本文件顶部用法。')
    process.exit(2)
  }
  const ws = new WebSocket(version.webSocketDebuggerUrl, { maxPayload: 64 * 1024 * 1024 })
  await new Promise((r, j) => {
    ws.once('open', r)
    ws.once('error', j)
  })

  let id = 0
  const pending = new Map()
  ws.on('message', (raw) => {
    const m = JSON.parse(raw.toString())
    if (m.id && pending.has(m.id)) {
      const { resolve, reject } = pending.get(m.id)
      pending.delete(m.id)
      m.error ? reject(new Error(JSON.stringify(m.error))) : resolve(m.result)
    }
  })
  const send = (method, params, sessionId) =>
    new Promise((resolve, reject) => {
      const mid = ++id
      pending.set(mid, { resolve, reject })
      ws.send(JSON.stringify({ id: mid, method, params: params || {}, sessionId }))
    })

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
  const evaluate = async (sessionId, expression) => {
    const res = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }, sessionId)
    if (res.exceptionDetails) throw new Error('页面内报错: ' + JSON.stringify(res.exceptionDetails).slice(0, 200))
    return res.result.value
  }

  let failed = 0
  const assetReports = new Set()
  console.log(`光标审计　目标 ${base}　主题 ${THEMES.join(' + ')}\n`)

  for (const theme of THEMES) {
    console.log(`---- 主题 ${theme} ----`)
    for (const c of CASES) {
      const label = `[${theme}] ${c.name}`
      const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
      const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
      try {
        await send('Page.enable', {}, sessionId)
        // 主题必须在导航前注入：store 在页面初始化时就会写 data-theme，
        // 导航之后再设置的话，首屏那一批元素已经按另一个主题渲染过了
        await send(
          'Page.addScriptToEvaluateOnNewDocument',
          { source: `try { localStorage.setItem('llm-relay-theme', ${JSON.stringify(theme)}) } catch (e) {}` },
          sessionId
        )
        await send('Page.navigate', { url: base + c.path }, sessionId)
        await sleep(c.click ? 5000 : 4000)
        if (c.click) {
          // 按按钮文案找，找不到用例就失败 —— 静默跳过等于这条用例不存在。
          // 轮询而不是点一次：按钮所在的表格要等接口回来才渲染，
          // 线上实例（WSL 里、打包产物大）实测 5 秒时还没出现。
          const deadline = Date.now() + 15000
          let clicked = false
          while (!clicked && Date.now() < deadline) {
            clicked = await evaluate(
              sessionId,
              `(() => { const b = [...document.querySelectorAll('button')].find((x) => x.textContent.includes(${JSON.stringify(c.click)})); if (b) { b.click(); return true } return false })()`
            )
            if (!clicked) await sleep(500)
          }
          if (!clicked) {
            if (c.needsData) {
              console.log(`  SKIP  ${label}　（没等到「${c.click}」入口按钮：${c.needsData}时页面上没有这一行）`)
              continue
            }
            console.log(`  FAIL  ${label}：等满 15 秒仍找不到「${c.click}」按钮`)
            failed++
            continue
          }
          await sleep(1800)
        }
        // 先确认主题真的切过去了：没切成功的话下面的断言其实在测另一个主题，
        // 「全绿」就成了假象
        const st = JSON.parse(await evaluate(sessionId, CHECK_THEME))
        if (st.attr !== theme) {
          console.log(`  FAIL  ${label}：主题没切过去（data-theme=${st.attr}，localStorage=${st.stored}）`)
          failed++
          continue
        }
        const buckets = await evaluate(sessionId, auditExpr(theme))
        const assets = await evaluate(sessionId, PROBE_ASSETS)
        for (const [k, v] of Object.entries(assets)) assetReports.add(`${k}: ${v}`)

        const bad = []
        for (const [k, b] of Object.entries(buckets)) {
          const allowed = k === '自定义箭头' || k === '自定义手型' || ['grab', 'grabbing', 'not-allowed', 'help', 'text', 'auto'].includes(k)
          // 系统光标只有在「鼠标碰不到」时才算无害
          if (!allowed && b.inert < b.total) bad.push({ k, b })
        }
        if (bad.length === 0) {
          const sum = Object.entries(buckets)
            .map(([k, b]) => `${k} ${b.total}`)
            .join('　')
          console.log(`  PASS  ${label}　${sum}`)
        } else {
          console.log(`  FAIL  ${label}`)
          for (const { k, b } of bad) {
            const top = Object.entries(b.sigs)
              .sort((x, y) => y[1] - x[1])
              .slice(0, 6)
              .map(([s, n]) => `${n} × ${s}`)
              .join('、')
            const why = k.startsWith('串主题') ? '（本主题拿到了另一套图形）' : '（退回系统光标）'
            console.log(`        ${k}${why}：${b.total - b.inert} 个可达元素 —— ${top}`)
          }
          failed++
        }
      } catch (e) {
        console.log(`  FAIL  ${label}：${e.message}`)
        failed++
      } finally {
        await send('Target.closeTarget', { targetId }).catch(() => {})
      }
    }
    console.log('')
  }

  console.log('')
  console.log('=== 光标资源 ===')
  for (const line of assetReports) {
    // 只查 200 会被 SPA 回退骗过：后端对任何未知路径都回 index.html + 200，
    // 图片解码失败后光标静默退回系统光标
    const ok = /200 image\/svg\+xml/.test(line)
    console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${line}`)
    if (!ok) failed++
  }

  console.log('')
  ws.close()
  if (failed > 0) {
    console.log(`${failed} 项未通过`)
    process.exit(1)
  }
  console.log('全部通过')
  process.exit(0)
}

main().catch((e) => {
  console.error('审计失败: ' + e.message)
  process.exit(2)
})
