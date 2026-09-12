// 截取包含画布的那个面板（路由分析的分流图），含上方标题。
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
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 2, mobile: false }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/route-analysis' }, sessionId)
await new Promise((r) => setTimeout(r, 7000))

const box = JSON.parse((await send('Runtime.evaluate', {
  expression: 'JSON.stringify((function(){' +
    'var cv=document.querySelector("canvas");var p=cv.closest("section");var b=p.getBoundingClientRect();' +
    'return {x:b.x,y:b.y,width:b.width,height:b.height}})())',
  returnByValue: true
}, sessionId)).result.value)
const shot = await send('Page.captureScreenshot', {
  format: 'png',
  clip: { x: box.x, y: box.y, width: box.width, height: box.height, scale: 2 }
}, sessionId)
fs.writeFileSync('.shots/routing-chart.png', Buffer.from(shot.data, 'base64'))
console.log('已保存 ' + Math.round(box.width) + 'x' + Math.round(box.height))
await send('Target.closeTarget', { targetId })
ws.close()
