// 光标审计：把每个页面上所有元素的计算 cursor 统计一遍，确认「该是自定义
// 光标的地方没有退回系统光标」。
//
// 为什么需要这个脚本：CSS 的 cursor 不进截图 —— 光标画在页面之外，截图里
// 永远看不见。也就是说「样式写了但没生效」这件事在截图与人工核对里都是隐形的：
// 少写一个选择器、antd 升级换了 class 名、URL 写错导致图片 404，
// 表现都只是那一处悄悄变回系统箭头，没有任何报错。
//
// 用法:
//   node scripts/audit-cursors.mjs [baseUrl]
//   baseUrl 默认 http://127.0.0.1:8888（容器里跑的那个实例）
//   开发态用 http://127.0.0.1:5173（先 npm run dev）
//
// 前置：一个开着调试端口的 Chrome
//   chrome --remote-debugging-port=9222 --user-data-dir=.chrome-profile
//
// 判定：允许出现的光标值是自定义箭头/自定义手型，以及刻意保留的
// grab / grabbing / not-allowed / help / text / auto（沿用系统的功能性光标，
// 理由见 frontend/src/styles/theme.css 的「自定义光标」段）。
// 出现系统默认箭头（default）或系统手型（pointer）即判失败 ——
// 但 pointer-events: none 的元素不算：鼠标根本碰不到它们，antd 的那些隐藏
// input 就属于这一类。
import http from 'node:http'
import { WebSocket } from 'ws'

const CDP_PORT = 9222
const base = (process.argv[2] || 'http://127.0.0.1:8888').replace(/\/$/, '')

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

const AUDIT = `(() => {
  const classify = (v) => v.includes('cursor-arrow.svg') ? '自定义箭头'
    : v.includes('cursor-hand.svg') ? '自定义手型' : v
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

const PROBE_ASSETS = `(async () => {
  const out = {}
  for (const f of ['cursor-arrow.svg', 'cursor-hand.svg']) {
    try {
      const r = await fetch('/' + f)
      out[f] = r.status + ' ' + r.headers.get('content-type')
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
  console.log(`光标审计　目标 ${base}\n`)

  for (const c of CASES) {
    const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
    const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
    try {
      await send('Page.enable', {}, sessionId)
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
            console.log(`  SKIP  ${c.name}　（没等到「${c.click}」入口按钮：${c.needsData}时页面上没有这一行）`)
            continue
          }
          console.log(`  FAIL  ${c.name}：等满 15 秒仍找不到「${c.click}」按钮`)
          failed++
          continue
        }
        await sleep(1800)
      }
      const buckets = await evaluate(sessionId, AUDIT)
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
        console.log(`  PASS  ${c.name}　${sum}`)
      } else {
        console.log(`  FAIL  ${c.name}`)
        for (const { k, b } of bad) {
          const top = Object.entries(b.sigs)
            .sort((x, y) => y[1] - x[1])
            .slice(0, 6)
            .map(([s, n]) => `${n} × ${s}`)
            .join('、')
          console.log(`        残留系统光标 ${k}：${b.total - b.inert} 个可达元素 —— ${top}`)
        }
        failed++
      }
    } catch (e) {
      console.log(`  FAIL  ${c.name}：${e.message}`)
      failed++
    } finally {
      await send('Target.closeTarget', { targetId }).catch(() => {})
    }
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
