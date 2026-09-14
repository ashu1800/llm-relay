// 截我们日志页的表格区域，用来和参考站对比字体。
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
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/logs' }, sessionId)
await new Promise((r) => setTimeout(r, 8000))
const box = JSON.parse((await send('Runtime.evaluate', {
  expression: 'JSON.stringify((function(){var e=document.querySelector(".ant-table");var b=e.getBoundingClientRect();return {x:b.x,y:b.y,width:Math.min(b.width,1100),height:Math.min(b.height,560)}})())',
  returnByValue: true
}, sessionId)).result.value)
const shot = await send('Page.captureScreenshot', { format: 'png', clip: { x: box.x, y: box.y, width: box.width, height: box.height, scale: 2 } }, sessionId)
fs.writeFileSync(new URL('../.shots/our-logs2.png', import.meta.url), Buffer.from(shot.data, 'base64'))
console.log('已保存 ' + Math.round(box.width) + 'x' + Math.round(box.height))
await send('Target.closeTarget', { targetId })
ws.close()
