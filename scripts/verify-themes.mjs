// 主题系统端到端验证（2026-09-30 随 12 套主题一起立）。
//
// 它验的是**只有真浏览器才能确认**的五件事：
//   1. 每套主题都能落地：data-theme 属性、页面底色（计算样式）、
//      浏览器外壳色（meta theme-color）三处与 theme.css / 注册表的声明值逐字一致 ——
//      静态脚本能比对声明，但「声明有没有真的作用到页面上」只有渲染后才知道
//      （CSS 选择器写错、属性拼错、打包没带上，表现都只是页面退回默认配色）。
//   2. 切换交互：真实鼠标点开看板右上角那枚按钮 → 弹层出现 12 张主题卡 →
//      点其中一张 → 主题真的换了、且弹层不关闭（换主题的用法就是连点着看效果）。
//   3. 持久化：刷新之后仍是刚选的那套（预涂脚本 + store 两处都要认它，
//      少一处就会出现「刷新闪一下别的配色」）。
//   4. 控制台无异常：12 次导航都不许报错。
//   5. 首帧主题：把打包产物挡掉，只让 index.html 的预涂脚本跑，逐条验它
//      对老档位迁移、脏数据、系统偏好这几档的判定 —— 那一帧在任何正常加载里
//      都看不到，分支写错了也没有别的检查能发现。
//
// 用法：node scripts/verify-themes.mjs [base] [主题 id（只验一套，调试用）]
//   默认 base = http://127.0.0.1:8888，逐套跑到 12 套。
// 前置：一个开着调试端口的 Chrome
//   chrome --remote-debugging-port=9222 --user-data-dir=.chrome-profile
// 截图落到 .shots/theme-<id>.png（该目录不入库）。
//
// THEME_VERIFY_STUB_API=1 时把管理接口（`*/api/admin/*`）的请求就地打桩成成功响应
// （见下方说明；模式不能写成 `*/api/*`，那会连 `src/api/client.ts` 这个模块一起拦下来）。
// 用途：本机连着一个**开着鉴权但拿不到管理密钥**的实例时，仍然能把主题系统走一遍。
// 它只影响数据，不影响被验的东西 —— 主题（CSS 变量、data-theme、localStorage、
// 弹层交互）全在客户端。
import { mkdirSync, writeFileSync } from 'node:fs'
import { parseRegistry, parseThemes } from '../frontend/scripts/lib/theme-css.mjs'

const BASE = (process.argv[2] || 'http://127.0.0.1:8888').replace(/\/$/, '')
const CDP_PORT = process.env.CDP_PORT || 9222
const OUT_DIR = '.shots'
const ONLY = process.argv[3] || ''
const STUB_API = process.env.THEME_VERIFY_STUB_API === '1'

let failed = 0
const chk = (name, ok, detail) => {
  console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  ' + detail : ''}`)
  if (!ok) failed++
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// ---- 期望值来自主题声明（theme.css + 注册表），不在这里手抄 ----
const THEMES = parseThemes()
const REGISTRY = new Map(parseRegistry().map((t) => [t.id, t]))
const IDS = [...THEMES.keys()].filter((id) => !ONLY || id === ONLY)
if (IDS.length === 0) {
  console.error(`没有匹配的主题：${ONLY}（可用：${[...THEMES.keys()].join(' / ')}）`)
  process.exit(2)
}

// ---- CDP 样板（与 .shots 下其它走查脚本同一套） ----
const ver = await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/version`)).json()
const ws = new WebSocket(ver.webSocketDebuggerUrl)
await new Promise((r, j) => { ws.onopen = r; ws.onerror = j })
let msgId = 0
const pending = new Map()
const pageErrors = []
// 打桩模式下要就地应答 API 请求，所以 onmessage 里要能发命令
let sendFn = null
ws.onmessage = (e) => {
  const m = JSON.parse(e.data)
  if (m.method === 'Fetch.requestPaused') {
    const url = m.params?.request?.url || ''
    // 「首帧主题」那一节要把打包产物挡掉，让页面上只剩预涂脚本在跑。
    // 注意这里必须放在 API 打桩之前：那一节的模式表里同时含 API 与产物两类请求。
    if (/\/assets\/[^/]*\.js(\?|$)/.test(url) || /\/src\/main\.ts(\?|$)/.test(url)) {
      void sendFn('Fetch.failRequest', { requestId: m.params.requestId, errorReason: 'Aborted' }, m.sessionId)
      return
    }
    // 只影响数据：鉴权状态按「未开启鉴权」应答（路由守卫放行），
    // 列表类接口给空数组、其余给空对象 —— 组件都有空态处理。
    const payload = url.includes('/auth/status')
      ? { authenticated: true, enabled: false }
      : /\/(logs|channels|keys|groups|proxies|models|proxies\/list)\b/.test(url)
        ? []
        : {}
    void sendFn('Fetch.fulfillRequest', {
      requestId: m.params.requestId,
      responseCode: 200,
      responseHeaders: [
        { name: 'Content-Type', value: 'application/json' },
        { name: 'Cache-Control', value: 'no-store' }
      ],
      body: Buffer.from(JSON.stringify(payload)).toString('base64')
    }, m.sessionId)
    return
  }
  if (m.method === 'Runtime.exceptionThrown') {
    const d = m.params?.exceptionDetails
    pageErrors.push(d?.exception?.description || d?.text || '未知异常')
  }
  if (m.method === 'Runtime.consoleAPICalled' && m.params?.type === 'error') {
    pageErrors.push((m.params.args || []).map((a) => a.value ?? a.description ?? '').join(' '))
  }
  if (m.id && pending.has(m.id)) {
    const p = pending.get(m.id)
    pending.delete(m.id)
    m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result)
  }
}
const send = (method, params, sessionId) => {
  const mid = ++msgId
  return new Promise((resolve, reject) => {
    pending.set(mid, { resolve, reject })
    ws.send(JSON.stringify({ id: mid, method, params: params || {}, sessionId }))
  })
}
sendFn = send

const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
for (const d of ['Page', 'Runtime']) await send(d + '.enable', {}, sessionId)
const evaluate = async (expr) => {
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true }, sessionId)
  if (r.exceptionDetails) throw new Error('页面内报错: ' + JSON.stringify(r.exceptionDetails).slice(0, 300))
  return r.result.value
}
const goto = async (path, wait = 3000) => {
  await send('Page.navigate', { url: BASE + path }, sessionId)
  await sleep(wait)
}

// 登录态：鉴权开启的部署下未登录会被改道到登录页，那样验不到看板。
// 密钥取不到就直接跑（鉴权关闭的部署不需要），与其它走查脚本同一口径。
async function adminCookie() {
  for (const key of [process.env.RELAY_ADMIN_KEY, 'probe-admin-key-for-theme-test']) {
    if (!key) continue
    try {
      const r = await fetch(BASE + '/api/admin/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Origin: BASE },
        body: JSON.stringify({ key })
      })
      if (!r.ok) continue
      const sc = r.headers.get('set-cookie')
      if (sc) return sc.split(';')[0]
    } catch { /* 试下一个 */ }
  }
  return ''
}
const cookie = await adminCookie()
if (cookie) {
  await send('Network.enable', {}, sessionId)
  await send('Network.setCookie', {
    name: cookie.split('=')[0],
    value: cookie.split('=').slice(1).join('='),
    domain: new URL(BASE).hostname,
    path: '/'
  }, sessionId)
  console.log('  已注入管理会话，按登录态走查')
}
if (STUB_API) {
  // 请求阶段拦截：本机实例开着鉴权又拿不到密钥时，用它把数据面打桩。
  // 模式必须写成 */api/admin/* 而不是 */api/* —— 后者会把
  // http://…/src/api/client.ts 这个**模块**也拦下来（URL 里含 "/api/"），
  // 于是整个应用加载不出来，而报错只是控制台里一句 MIME type 不符。
  await send('Fetch.enable', { patterns: [{ urlPattern: '*/api/admin/*', requestStage: 'Request' }] }, sessionId)
  console.log('  已开启 API 打桩（THEME_VERIFY_STUB_API=1）：只验主题，不验数据')
}

mkdirSync(OUT_DIR, { recursive: true })
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false }, sessionId)
await goto('/console/dashboard', 4000)

// ---- 1. 逐套主题：属性、页面底色、外壳色、截图 ----
console.log(`\n=== 逐套主题落地（${IDS.length} 套）===`)
for (const id of IDS) {
  const block = THEMES.get(id)
  const expectBg = (block.vars.get('--color-bg') || '').toLowerCase()
  const expectShell = (REGISTRY.get(id)?.shell || '').toLowerCase()

  // 先在本站原点下写偏好再导航：预涂脚本在读它，必须在文档创建之前就写进去
  await evaluate(`localStorage.setItem('llm-relay-theme', ${JSON.stringify(id)})`)
  pageErrors.length = 0
  await goto('/console/dashboard', 2600)

  const actual = JSON.parse(
    await evaluate(`(() => JSON.stringify({
      attr: document.documentElement.getAttribute('data-theme'),
      bg: getComputedStyle(document.documentElement).getPropertyValue('--color-bg').trim().toLowerCase(),
      shell: (document.querySelector('meta[name="theme-color"]') || {}).content || '',
      stored: localStorage.getItem('llm-relay-theme')
    }))()`)
  )

  chk(`${id} 预涂与 store 都认这套主题`, actual.attr === id && actual.stored === id, `data-theme=${actual.attr} / localStorage=${actual.stored}`)
  chk(`${id} 页面底色等于声明的 ${expectBg}`, actual.bg === expectBg, `实测 ${actual.bg}`)
  chk(`${id} 浏览器外壳色等于注册表的 ${expectShell}`, actual.shell.toLowerCase() === expectShell, `实测 ${actual.shell}`)
  chk(`${id} 导航期间控制台无异常`, pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))

  const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }, sessionId)
  writeFileSync(`${OUT_DIR}/theme-${id}.png`, Buffer.from(shot.data, 'base64'))
}

// ---- 2. 交互：真实鼠标点开弹层、选一套、刷新后保持 ----
console.log('\n=== 切换交互（真实鼠标事件）===')
await evaluate(`localStorage.setItem('llm-relay-theme', 'light')`)
await goto('/console/dashboard', 3000)

const rectOf = async (selector) =>
  evaluate(`(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return null
    const r = el.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2, w: r.width, h: r.height } })()`)
const clickAt = async (p) => {
  for (const type of ['mousePressed', 'mouseReleased']) {
    await send('Input.dispatchMouseEvent', { type, x: p.x, y: p.y, button: 'left', clickCount: 1 }, sessionId)
  }
}
/** 轮询等待选择器出现（页面数据/动画都是异步的，固定 sleep 会偶发失败） */
const waitFor = async (selector, timeout = 4000) => {
  const deadline = Date.now() + timeout
  for (;;) {
    const r = await rectOf(selector)
    if (r) return r
    if (Date.now() > deadline) return null
    await sleep(200)
  }
}
/**
 * 点一个元素并等某个选择器出现；第一次没出现就再点一次。
 *
 * 为什么必须轮询而不是「点一次 + sleep」：这套脚本靠真实鼠标事件驱动 antd 的
 * 弹层，而按钮的位置、弹层的挂载、上一帧的布局都可能比固定等待慢一点点 ——
 * 实测同一份代码连跑几次会出现「点了但弹层没开」的偶发失败，而那是脚本的
 * 时序问题，不是产品问题（假红比假绿更浪费人：每次都要重新判断是不是真错）。
 */
const clickAndWait = async (targetSel, appearSel, timeout = 4000) => {
  for (let attempt = 0; attempt < 2; attempt++) {
    const p = await waitFor(targetSel, timeout)
    if (!p) return null
    await clickAt(p)
    const appeared = await waitFor(appearSel, timeout)
    if (appeared) return appeared
  }
  return null
}
/** 轮询等待 data-theme 变成某个值（切换本身是同步的，写回属性在 watcher 里） */
const waitForAttr = async (value, timeout = 3000) => {
  const deadline = Date.now() + timeout
  for (;;) {
    const v = await evaluate(`document.documentElement.getAttribute('data-theme')`)
    if (v === value || Date.now() > deadline) return v
    await sleep(150)
  }
}

const btn = await waitFor('.theme-btn')
chk('看板工具栏右上角能找到主题按钮（32px 圆钮）', !!btn && Math.round(btn.w) === 32, btn ? `${Math.round(btn.w)}×${Math.round(btn.h)}` : '找不到 .theme-btn')
if (btn) {
  const opened = await clickAndWait('.theme-btn', '.theme-popover')
    .then(() => evaluate(`(() => { const p = document.querySelector('.theme-popover'); if (!p) return null
      return { cards: p.querySelectorAll('.theme-item').length, groups: p.querySelectorAll('.theme-group-label').length } })()`))
  chk('点一下真的打开了主题弹层', !!opened, opened ? '' : '找不到 .theme-popover')
  // 弹层内容被 antd teleport 到 body 外层且不自动移焦 —— 不主动把焦点送进去的话，
  // 纯键盘用户点开之后按 Tab 会走到弹层**外面**的下一个按钮，12 张卡一张都够不着。
  let focusInside = false
  for (let i = 0; i < 10 && !focusInside; i++) {
    focusInside = await evaluate(
      `(() => { const p = document.querySelector('.theme-popover'); return !!p && p.contains(document.activeElement) })()`
    )
    if (!focusInside) await sleep(150)
  }
  chk('打开弹层后焦点落在弹层里（纯键盘用户能继续操作）', focusInside)
  if (opened) {
    const total = [...THEMES.keys()].length
    chk('弹层里有全部主题卡', opened.cards === total, `实测 ${opened.cards} 张 / 应有 ${total} 张`)
    chk('浅色与深色分成两组', opened.groups === 2, `实测 ${opened.groups} 组`)

    // 点「暗夜紫」那张卡（按可见文字找，不依赖 DOM 顺序）。
    // 点之前先给 startViewTransition 套一层：记下**过渡回调执行那一刻**的
    // data-theme。扩散动效要求「旧快照是新主题之前的样子」，也就是回调里读到的
    // 仍是旧主题 —— 如果哪个入口抢先把属性改了（历史上 @click 与 @change 双挂时
    // 就是这样），这里会读到新主题，而动画其实在空转（肉眼只看到一次普通切换，
    // 不会报错）。单测抓不到这条：它跑在 store 层，看不到组件的双入口。
    const vtProbe = await evaluate(`(() => {
      window.__atCallback = null
      const orig = document.startViewTransition
      if (typeof orig !== 'function') return 'no-vt'
      document.startViewTransition = function (cb) {
        return orig.call(document, function () {
          window.__atCallback = document.documentElement.getAttribute('data-theme')
          return cb()
        })
      }
      return document.documentElement.getAttribute('data-theme')
    })()`)
    const card = await evaluate(`(() => {
      const el = [...document.querySelectorAll('.theme-item')].find((x) => x.textContent.includes('暗夜紫'))
      if (!el) return null
      const r = el.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 }
    })()`)
    chk('弹层里能找到「暗夜紫」那张卡', !!card)
    if (card) {
      await clickAt(card)
      await waitForAttr('dracula')
      if (vtProbe === 'no-vt') {
        console.log('  SKIP  圆形扩散：这个 Chrome 没有 View Transitions')
      } else {
        const atCallback = await evaluate(`window.__atCallback`)
        chk(
          '圆形扩散没被抢跑（过渡回调那一刻页面还是旧主题）',
          atCallback === vtProbe,
          `回调里读到 ${atCallback} / 切换前是 ${vtProbe}`
        )
      }
      const after = JSON.parse(
        await evaluate(`(() => JSON.stringify({
          attr: document.documentElement.getAttribute('data-theme'),
          stored: localStorage.getItem('llm-relay-theme'),
          open: !!document.querySelector('.theme-popover'),
          shell: document.querySelector('meta[name="theme-color"]').content.toLowerCase()
        }))()`)
      )
      chk('点卡片真的切换了主题', after.attr === 'dracula' && after.stored === 'dracula', `data-theme=${after.attr} / localStorage=${after.stored}`)
      chk('切换后浏览器外壳色跟着换', after.shell === (REGISTRY.get('dracula')?.shell || '').toLowerCase(), `实测 ${after.shell}`)
      chk('弹层不自动关闭（方便连着试几套）', after.open === true)

      // 刷新：预涂脚本与 store 都要认这套主题
      pageErrors.length = 0
      await goto('/console/dashboard', 3000)
      const reloaded = await evaluate(`document.documentElement.getAttribute('data-theme')`)
      chk('刷新后仍是刚选的主题', reloaded === 'dracula', `实测 ${reloaded}`)
      chk('刷新后控制台无异常', pageErrors.length === 0, pageErrors.slice(0, 2).join(' | '))

      // 快捷「浅色」：验的是「按记忆切回该族上次用过的那套」。
      // 所以先显式写一个**非默认**的浅色记忆，再刷新让 store 读到它 ——
      // 否则（浏览器的 localStorage 会跨运行保留）这条断言要么落回默认浅色、
      // 要么被上一轮跑出来的记忆带着走，两种都不再说明「记忆被用上了」。
      await evaluate(`localStorage.setItem('theme-light-last', 'latte')`)
      await goto('/console/dashboard', 3000)
      const opened2 = await clickAndWait('.theme-btn', '.theme-popover')
      chk('重新打开弹层（快捷按钮在里面）', !!opened2)
      const quick = await evaluate(`(() => {
        const el = [...document.querySelectorAll('.theme-quick-btn')].find((x) => x.textContent.trim() === '浅色')
        if (!el) return null
        const r = el.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 }
      })()`)
      chk('弹层里有「浅色」快捷按钮', !!quick)
      if (quick) {
        await clickAt(quick)
        // 切换本身是同步的，这里只是等 watcher 落盘与属性写回
        await sleep(700)
        const back = await evaluate(`document.documentElement.getAttribute('data-theme')`)
        chk('快捷按钮切回该族上次用过的主题（记忆里的 latte）', back === 'latte', `实测 ${back}`)
      }
    }
  }
}

// ---- 3. 首帧主题：预涂脚本单独跑时的判定 ----
//
// 预涂脚本（index.html 里的内联 IIFE）与 store 是两处独立实现，而它只在
// 「打包产物还没跑起来」的那一帧起作用 —— 这一帧在任何正常加载里都看不到，
// 所以它的分支错了也没人会发现（表现只是某类用户刷新时闪一下别的配色）。
// 做法：把打包产物与入口模块的请求就地失败掉，页面就只剩预涂脚本在跑，
// 此时读到的 data-theme 必然出自它。
console.log('\n=== 首帧主题（只让预涂脚本跑，挡掉打包产物）===')
{
  const patterns = [{ urlPattern: '*/assets/*.js', requestStage: 'Request' }, { urlPattern: '*/src/main.ts', requestStage: 'Request' }]
  if (STUB_API) patterns.push({ urlPattern: '*/api/admin/*', requestStage: 'Request' })
  await send('Fetch.enable', { patterns }, sessionId)
  // 系统偏好也要可控：预涂脚本的最后一档回落就是它
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'light' }] }, sessionId)

  const CASES = [
    { name: '老用户：主题=dark + 老档位=oled → 直接进 oled（不闪经典深灰）', keys: { 'llm-relay-theme': 'dark', 'llm-relay-darkstyle': 'oled' }, expect: 'oled' },
    { name: '老用户：主题=light + 老档位=oled → 保持浅色（档位只对深色底生效）', keys: { 'llm-relay-theme': 'light', 'llm-relay-darkstyle': 'oled' }, expect: 'light' },
    { name: '老用户：没有主题键 + 老档位=oled → 迁移成 oled', keys: { 'llm-relay-darkstyle': 'oled' }, expect: 'oled' },
    { name: '新用户：主题键是合法新 id → 原样使用', keys: { 'llm-relay-theme': 'nord' }, expect: 'nord' },
    { name: '脏数据：主题键不认识 → 回落系统偏好（这里模拟浅色）', keys: { 'llm-relay-theme': 'auto' }, expect: 'light' },
    { name: '没有任何键 → 跟随系统偏好（模拟浅色）', keys: {}, expect: 'light' }
  ]
  for (const c of CASES) {
    await evaluate(`(() => {
      localStorage.removeItem('llm-relay-theme')
      localStorage.removeItem('llm-relay-darkstyle')
      for (const [k, v] of Object.entries(${JSON.stringify(c.keys)})) localStorage.setItem(k, v)
      return true
    })()`)
    await send('Page.navigate', { url: BASE + '/console/dashboard' }, sessionId)
    await sleep(1200)
    const attr = await evaluate(`document.documentElement.getAttribute('data-theme')`)
    chk(c.name, attr === c.expect, `首帧 data-theme=${attr} / 期望 ${c.expect}`)
  }
  // 系统偏好为深色时同样要认
  await evaluate(`(() => { localStorage.removeItem('llm-relay-theme'); localStorage.removeItem('llm-relay-darkstyle'); return true })()`)
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'dark' }] }, sessionId)
  await send('Page.navigate', { url: BASE + '/console/dashboard' }, sessionId)
  await sleep(1200)
  const darkAttr = await evaluate(`document.documentElement.getAttribute('data-theme')`)
  chk('没有任何键 + 系统偏好深色 → 首帧就是深色', darkAttr === 'dark', `首帧 data-theme=${darkAttr}`)
}

await send('Target.closeTarget', { targetId }).catch(() => {})
ws.close()

console.log('')
if (failed) {
  console.error(`${failed} 项未通过`)
  process.exit(1)
}
console.log(`全部通过；截图已存到 ${OUT_DIR}/theme-<id>.png`)
process.exit(0)
