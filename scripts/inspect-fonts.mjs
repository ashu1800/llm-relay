// 通过 CDP 查清一个页面真正用哪个平台字体渲染文本。
// CSS.getPlatformFontsForNode 给出的才是实际命中的字体，
// 光看 font-family 声明只能看到回退链，看不出最终落到哪一款。
import { WebSocket } from 'ws'

const target = process.argv[2]
const selector = process.argv[3] || 'body'
if (!target) { console.error('用法: node inspect-fonts.mjs <url> [selector]'); process.exit(1) }

const ver = await (await fetch('http://127.0.0.1:9222/json/version')).json()
const ws = new WebSocket(ver.webSocketDebuggerUrl, { maxPayload: 256 * 1024 * 1024 })
await new Promise((r, j) => { ws.once('open', r); ws.once('error', j) })

let id = 0
const pending = new Map()
const events = []
ws.on('message', (raw) => {
  const msg = JSON.parse(raw.toString())
  if (msg.id && pending.has(msg.id)) {
    const { resolve, reject } = pending.get(msg.id)
    pending.delete(msg.id)
    msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result)
  } else if (msg.method) {
    events.push(msg)
  }
})
function send(method, params, sessionId) {
  const mid = ++id
  return new Promise((resolve, reject) => {
    pending.set(mid, { resolve, reject })
    ws.send(JSON.stringify({ id: mid, method, params: params || {}, sessionId }))
  })
}

const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })

await send('Page.enable', {}, sessionId)
await send('DOM.enable', {}, sessionId)
await send('CSS.enable', {}, sessionId)

await send('Page.navigate', { url: target }, sessionId)
await new Promise((r) => setTimeout(r, 6000))

const evalJs = async (expr) => {
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true }, sessionId)
  return r.result && r.result.value
}

const info = await evalJs(`(() => {
  const out = {}
  const b = getComputedStyle(document.body)
  out.title = document.title
  out.bodyFamily = b.fontFamily
  out.bodySize = b.fontSize
  const h1 = document.querySelector('h1,h2,.panel-title,.title')
  if (h1) { out.headingFamily = getComputedStyle(h1).fontFamily }
  out.loadedFonts = []
  document.fonts.forEach(f => out.loadedFonts.push(f.family + ' ' + f.weight + ' ' + f.status))
  const nav = document.querySelector('nav a, .menu a, aside a, li a')
  if (nav) { out.navFamily = getComputedStyle(nav).fontFamily; out.navText = (nav.textContent||'').trim().slice(0,12) }
  out.sampleTexts = []
  document.querySelectorAll('h1,h2,h3,button,a,td,th,span,div').forEach(el => {
    const t = (el.textContent || '').trim()
    if (t && t.length >= 2 && t.length <= 14 && out.sampleTexts.length < 6 && el.children.length === 0) {
      out.sampleTexts.push(t)
    }
  })
  return out
})()`)

console.log('标题:', info.title)
console.log('body font-family:', info.bodyFamily)
console.log('body font-size:', info.bodySize)
if (info.headingFamily) console.log('标题 font-family:', info.headingFamily)
if (info.navFamily) console.log('菜单 font-family:', info.navFamily)
console.log('已加载字体:', info.loadedFonts.length ? info.loadedFonts.join(' | ') : '(无 Web 字体)')
console.log('样例文本:', info.sampleTexts.join(' / '))

// 实际命中的平台字体
const doc = await send('DOM.getDocument', { depth: -1 }, sessionId)
try {
  const node = await send('DOM.querySelector', { nodeId: doc.root.nodeId, selector }, sessionId)
  if (node.nodeId) {
    const fonts = await send('CSS.getPlatformFontsForNode', { nodeId: node.nodeId }, sessionId)
    console.log('实际渲染字体 (' + selector + '):')
    for (const f of fonts.fonts) {
      console.log('   ' + f.familyName + '  字形数 ' + f.glyphCount + (f.isCustomFont ? '  [Web 字体]' : '  [系统字体]'))
    }
  }
} catch (e) { console.log('取平台字体失败:', e.message) }

await send('Target.closeTarget', { targetId })
ws.close()
process.exit(0)
