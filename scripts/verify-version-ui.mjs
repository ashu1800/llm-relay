// 界面验证：版本徽标的渲染与更新面板的交互。
//
// 为什么值得单独一个脚本：接口返回正确不等于界面正确。这一层最容易出
// 的问题恰好是接口测试看不见的 —— 元素被父容器裁掉、下拉点不开、
// 状态分支进错了一支。这些只有真的把页面跑起来、真的点一下才知道。
//
// 用法（需要先有开启 --remote-debugging-port=9222 的 Chrome）：
//   node scripts/verify-version-ui.mjs
//
// 注意：需禁用 Node 的环境代理（NODE_USE_ENV_PROXY=0），否则 127.0.0.1 会被代理劫持。
import http from 'node:http'
import { writeFileSync } from 'node:fs'

const CDP_HOST = '127.0.0.1'
const CDP_PORT = 9222
const APP = 'http://127.0.0.1:8888'

function httpGet(path) {
  return new Promise((ok, fail) => {
    const req = http.request({ host: CDP_HOST, port: CDP_PORT, path, method: 'GET' }, (res) => {
      let buf = ''
      res.setEncoding('utf8')
      res.on('data', (c) => { buf += c })
      res.on('end', () => ok(buf))
    })
    req.on('error', fail)
    req.setTimeout(15000, () => req.destroy(new Error('请求 ' + path + ' 超时')))
    req.end()
  })
}

async function findTarget() {
  const list = JSON.parse(await httpGet('/json/list'))
  const pages = list.filter((t) => t.type === 'page' && t.webSocketDebuggerUrl)
  const target = pages.find((p) => p.url.indexOf('8888') >= 0) || pages[0]
  if (!target) throw new Error('未找到可用的 Chrome 页面')
  return target
}

function connect(url) {
  return new Promise((ok, fail) => {
    const ws = new WebSocket(url)
    ws.onopen = () => ok(ws)
    ws.onerror = (e) => fail(new Error('WebSocket 连接失败: ' + String(e && e.message)))
  })
}

let msgId = 0
function send(ws, method, params) {
  const id = ++msgId
  return new Promise((ok, fail) => {
    const timer = setTimeout(() => fail(new Error(method + ' 超时')), 30000)
    const onMsg = (ev) => {
      const m = JSON.parse(ev.data)
      if (m.id !== id) return
      clearTimeout(timer)
      ws.removeEventListener('message', onMsg)
      if (m.error) fail(new Error(method + ': ' + JSON.stringify(m.error)))
      else ok(m.result)
    }
    ws.addEventListener('message', onMsg)
    ws.send(JSON.stringify({ id, method, params }))
  })
}

// evalJS 在页面里求值并取回 JSON 结果。
async function evalJS(ws, expr) {
  const r = await send(ws, 'Runtime.evaluate', {
    expression: expr,
    returnByValue: true,
    awaitPromise: true
  })
  if (r.exceptionDetails) {
    throw new Error('页面求值异常: ' + JSON.stringify(r.exceptionDetails.exception))
  }
  return r.result.value
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function main() {
  const target = await findTarget()
  const ws = await connect(target.webSocketDebuggerUrl)
  await send(ws, 'Runtime.enable')
  await send(ws, 'Page.enable')

  const report = {}

  // 导航到管理台并等应用挂载。
  // 用 Page.navigate + 轮询而不是等 load 事件：SPA 的 load 可能早于
  // Vue 挂载完成，那时查元素会全部落空。
  await send(ws, 'Page.navigate', { url: APP + '/console/dashboard' })
  for (let i = 0; i < 40; i++) {
    await sleep(500)
    const ready = await evalJS(ws, `!!document.querySelector('.console-sidebar')`)
    if (ready) break
  }
  await sleep(1500) // 给 /system/version 请求留出时间

  // ---- 1. 版本徽标是否存在并可见 ----
  report.badge = await evalJS(ws, `(() => {
    const el = document.querySelector('.version-badge-trigger')
    if (!el) return { found: false }
    const r = el.getBoundingClientRect()
    const cs = getComputedStyle(el)
    return {
      found: true,
      text: el.textContent.trim(),
      width: Math.round(r.width),
      height: Math.round(r.height),
      visible: r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none',
      // 被父容器裁掉是这类下拉最常见的故障，所以显式检查
      clippedByParent: (() => {
        let p = el.parentElement
        while (p) {
          const s = getComputedStyle(p)
          if (s.overflow === 'hidden' || s.overflowX === 'hidden' || s.overflowY === 'hidden') {
            return p.className || p.tagName
          }
          p = p.parentElement
        }
        return null
      })(),
      hasDot: !!el.querySelector('.version-dot'),
      title: el.getAttribute('title')
    }
  })()`)
  if (!report.badge.found) {
    console.log(JSON.stringify({ 失败: '找不到版本徽标', 地址: await evalJS(ws, `location.href`) }, null, 2))
    ws.close()
    process.exit(1)
  }

  // ---- 2. 侧栏里只应有一处版本号（避免两处显示同一信息）----
  //
  // 注意：这里的代码会被嵌进页面求值，而它写在 Node 的模板字符串里 ——
  // 任何反斜杠转义都会被 Node 先吃一层（写 \\s 到页面里变成 s）。
  // 所以下面一律用字符串方法而不是正则，避免依赖转义层数。
  report.versionOccurrences = await evalJS(ws, `(() => {
    const sidebar = document.querySelector('.console-sidebar')
    const scope = sidebar || document.body
    const text = scope.innerText || ''
    const m = text.match(/v0[.]1[.]0[^ ]*/g) || []
    return { matches: m, count: m.length }
  })()`)

  // ---- 3. 点开面板 ----
  await evalJS(ws, `(() => {
    const t = document.querySelector('.version-badge-trigger')
    if (t && !document.querySelector('.version-panel')) t.click()
  })()`)
  await sleep(2500) // 面板要发一次 check-updates（走服务端缓存，很快）

  report.panel = await evalJS(ws, `(() => {
    const p = document.querySelector('.version-panel')
    if (!p) return { open: false }
    const r = p.getBoundingClientRect()
    // 面板由 popover 渲染进 body，不受侧栏裁剪，但仍要确认它真的
    // 溢出了侧栏宽度且没跑出视口 —— 那两件事才是「用户看得到」
    const sb = document.querySelector('.console-sidebar')
    const sr = sb ? sb.getBoundingClientRect() : null
    return {
      open: true,
      width: Math.round(r.width),
      height: Math.round(r.height),
      inViewport: r.top >= -1 && r.left >= -1 && r.bottom <= innerHeight + 1 && r.right <= innerWidth + 1,
      overflowsSidebar: sr ? r.right > sr.right : null,
      currentVersion: (p.querySelector('.vb-version') || {}).textContent,
      subtitle: (p.querySelector('.vb-sub') || {}).textContent,
      meta: (p.querySelector('.vb-meta') || {}).textContent,
      hasOkCheck: !!p.querySelector('.vb-ok'),
      alerts: Array.from(p.querySelectorAll('.vb-alert')).map(a => ({
        kind: a.className,
        text: a.innerText.split(String.fromCharCode(10)).join(' ').trim()
      })),
      buttons: Array.from(p.querySelectorAll('.vb-btn')).map(b => b.innerText.trim()),
      links: Array.from(p.querySelectorAll('a')).map(a => a.innerText.trim()),
      hasCollapse: !!p.querySelector('.vb-collapse')
    }
  })()`)

  // ---- 4. 展开回滚区 ----
  await evalJS(ws, `(() => {
    const b = document.querySelector('.vb-collapse')
    if (b) b.click()
  })()`)
  await sleep(2500)

  report.rollback = await evalJS(ws, `(() => {
    const rb = document.querySelector('.vb-rollback')
    if (!rb) return { open: false }
    const p = document.querySelector('.version-panel')
    const pr = p.getBoundingClientRect()
    return {
      open: true,
      panelHeightAfterExpand: Math.round(pr.height),
      panelStillInViewport: pr.bottom <= innerHeight + 1,
      // 正文可滚动是「内容变多也不会溢出面板」的保证
      bodyScrollable: (() => {
        const b = p.querySelector('.vb-body')
        if (!b) return null
        return { scrollHeight: b.scrollHeight, clientHeight: b.clientHeight, canScroll: b.scrollHeight > b.clientHeight }
      })(),
      text: rb.innerText.split(String.fromCharCode(10)).join(' ').trim().slice(0, 300),
      hasLocalButton: !!rb.querySelector('.vb-btn.is-warn'),
      versionItems: rb.querySelectorAll('.vb-rb-item').length
    }
  })()`)

  // ---- 5. 截图 ----
  //
  // 截图失败**不让整个验证失败**：Chrome 在窗口最小化或后台标签页时
  // 可能让 captureScreenshot 挂住（它需要一次真实的合成）。而截图的
  // 作用只是给人看，上面那些 DOM 断言才是真正的验证 —— 为一张图
  // 丢掉全部结论是不划算的。
  try {
    const shot = await send(ws, 'Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
    writeFileSync('.shots/version-badge.png', Buffer.from(shot.data, 'base64'))
    report.screenshot = '.shots/version-badge.png'
  } catch (e) {
    report.screenshot = '跳过（' + e.message + '）'
  }

  // ---- 6. 深色模式下的可读性 ----
  await evalJS(ws, `document.documentElement.setAttribute('data-theme', 'dark')`)
  await sleep(600)
  report.darkBadge = await evalJS(ws, `(() => {
    const el = document.querySelector('.version-badge-trigger')
    const cs = getComputedStyle(el)
    return { color: cs.color, borderColor: cs.borderColor, background: cs.backgroundColor }
  })()`)
  try {
    const shotDark = await send(ws, 'Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
    writeFileSync('.shots/version-badge-dark.png', Buffer.from(shotDark.data, 'base64'))
    report.screenshotDark = '.shots/version-badge-dark.png'
  } catch (e) {
    report.screenshotDark = '跳过（' + e.message + '）'
  }

  // 恢复浅色
  await evalJS(ws, `document.documentElement.setAttribute('data-theme', 'light')`)

  // ---- 7. 断言 ----
  //
  // 上面那些采集如果不加判定，脚本就只是个「打印器」：任何一条不变量
  // 被改坏，它都照样以退出码 0 结束，而调用方（含 verify-all.sh）会
  // 认为一切正常。下面把这些不变量真的判一遍。
  //
  // 每条都对应一个**实际踩过的缺陷**，注释里写明是哪一个。
  const failures = []
  const assert = (cond, msg) => { if (!cond) failures.push(msg) }

  // 徽标必须可见且有版本号可读
  assert(report.badge.visible, '徽标不可见')
  assert(/^v?\d+\./.test((report.badge.text || '').trim()),
    `徽标文字不像版本号：${JSON.stringify(report.badge.text)}`)

  // 缺陷：侧栏里同时出现两处版本号（徽标 + 别处），信息重复且容易不一致
  assert(report.versionOccurrences.count === 1,
    `侧栏里出现了 ${report.versionOccurrences.count} 处版本号，应恰好 1 处`)

  // 缺陷：面板被侧栏的 overflow 裁成一条（曾实测被裁到 192px）。
  // 现在面板由 a-popover 渲染进 body（脱离侧栏裁剪上下文），断言只看
  // **结果**（完整宽度 + 溢出侧栏 + 在视口内），不绑定实现方式。
  assert(report.panel.open, '点击后更新面板没有打开')
  assert(report.panel.width >= 280,
    `面板宽度被挤扁到 ${report.panel.width}px（侧栏只有 192px，说明又被裁了）`)
  assert(report.panel.overflowsSidebar === true, '面板没有溢出侧栏，说明它可能仍被侧栏裁剪')

  // 缺陷：展开回滚区后面板超出视口，底部按钮点不到
  assert(report.panel.inViewport, '面板不在视口内')
  assert(report.rollback.panelStillInViewport,
    `展开回滚区后面板底部超出视口（高度 ${report.rollback.panelHeightAfterExpand}px）`)

  // 缺陷：检测失败时同时显示绿勾与「已是最新版本」（自相矛盾的界面）
  const sub = report.panel.subtitle || ''
  const hasWarn = (report.panel.alerts || []).some(a => a.kind.includes('is-warn'))
  if (hasWarn) {
    assert(!report.panel.hasOkCheck, '检测有告警时仍显示了绿色对勾')
    assert(!sub.includes('已是最新'), `检测有告警时副标题仍说「已是最新」：${JSON.stringify(sub)}`)
  }
  // 反之，真的确认最新时不该显示「未能确认」
  if (report.panel.hasOkCheck) {
    assert(sub.includes('已是最新'), `显示绿勾时副标题应说「已是最新」，实际 ${JSON.stringify(sub)}`)
  }

  // 深色模式下徽标不能是透明或与背景同色（否则等于看不见）
  const db = report.darkBadge || {}
  assert(db.color && db.color !== 'rgba(0, 0, 0, 0)', '深色模式下徽标文字颜色为空/透明')

  console.log(JSON.stringify(report, null, 2))
  ws.close()

  if (failures.length) {
    console.error('')
    console.error('断言失败 ' + failures.length + ' 项：')
    for (const f of failures) console.error('  ✗ ' + f)
    console.error('')
    console.error('HAS_FAILURE')
    process.exit(1)
  }
  console.log('')
  console.log('ALL_PASS')
}

main().catch((e) => {
  console.error('验证失败:', e.message)
  process.exit(1)
})
