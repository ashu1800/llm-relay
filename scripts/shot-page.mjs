// 截图任意页面到 PNG（CDP），用于「必须看一眼」的视觉验收。
//
// 为什么单独一个脚本：verify 脚本只能量数值，而图标、配色、间距这类东西
// 只能看图。用法：node scripts/shot-page.mjs <url> <输出文件> [宽] [高] [等待毫秒]
import { writeFileSync } from 'node:fs'

const [url, out, w, h, waitMs] = process.argv.slice(2)
if (!url || !out) {
  console.error('用法: node scripts/shot-page.mjs <url> <输出文件> [宽] [高] [等待毫秒]')
  process.exit(2)
}
const width = Number(w) || 1280
const height = Number(h) || 800
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
await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 2, mobile: false }, sessionId)
await send('Page.navigate', { url }, sessionId)
await new Promise((r) => setTimeout(r, Number(waitMs) || 4000))
// 图标这类资源没有「渲染完成」事件，轮询一次 document.readyState 更稳
const shot = await send('Page.captureScreenshot', { format: 'png' }, sessionId)
writeFileSync(out, Buffer.from(shot.data, 'base64'))
console.log('已保存 ' + out)
await send('Target.closeTarget', { targetId })
ws.close()
