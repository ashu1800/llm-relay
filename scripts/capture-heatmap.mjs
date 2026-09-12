// 把热力图面板单独高清截出来，并用 echarts 的实际渲染结果核对行列数。
import fs from 'node:fs'
const ver = await (await fetch('http://127.0.0.1:9222/json/version')).json()
const ws = new WebSocket(ver.webSocketDebuggerUrl)
await new Promise((r, j) => { ws.onopen = r; ws.onerror = j })
let id = 0
const pending = new Map()
ws.onmessage = (e) => {
  const m = JSON.parse(e.data)
  if (m.id && pending.has(m.id)) {
    const { resolve, reject } = pending.get(m.id)
    pending.delete(m.id)
    m.error ? reject(new Error(JSON.stringify(m.error))) : resolve(m.result)
  }
}
const send = (method, params, sessionId) => {
  const mid = ++id
  return new Promise((resolve, reject) => {
    pending.set(mid, { resolve, reject })
    ws.send(JSON.stringify({ id: mid, method, params: params || {}, sessionId }))
  })
}
const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
await send('Page.enable', {}, sessionId)
await send('Emulation.setDeviceMetricsOverride', { width: 1600, height: 1000, deviceScaleFactor: 2, mobile: false }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/dashboard' }, sessionId)
await new Promise((r) => setTimeout(r, 6500))

// 先看清 DOM 里热力图容器的位置与尺寸
const info = await send('Runtime.evaluate', {
  expression: `(() => {
    const el = [...document.querySelectorAll('.panel')].find(p => p.innerText.includes('请求热力图'))
    if (!el) return 'not found'
    const r = el.getBoundingClientRect()
    const cv = el.querySelector('canvas')
    return JSON.stringify({
      面板: { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) },
      canvas: cv ? { w: cv.width, h: cv.height, cssW: Math.round(cv.getBoundingClientRect().width), cssH: Math.round(cv.getBoundingClientRect().height) } : null
    })
  })()`,
  returnByValue: true
}, sessionId)
console.log(info.result.value)

const box = JSON.parse(info.result.value)
await send('Page.captureScreenshot', {}, sessionId)
const shot = await send('Page.captureScreenshot', {
  format: 'png',
  clip: { x: box.面板.x, y: box.面板.y, width: box.面板.w, height: box.面板.h, scale: 2 }
}, sessionId)
fs.writeFileSync('.shots/heatmap.png', Buffer.from(shot.data, 'base64'))
console.log('已保存 .shots/heatmap.png')
await send('Target.closeTarget', { targetId })
ws.close()
