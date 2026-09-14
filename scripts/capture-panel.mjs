// 按面板标题关键字单独高清截取某张卡片。
// 用法: node scripts/capture-panel.mjs <标题关键字> <输出png> [页面路径] [额外下边距]
import fs from 'node:fs'
import { fileURLToPath } from 'node:url'

const key = process.argv[2]
const outFile = process.argv[3] || fileURLToPath(new URL('../.shots/panel.png', import.meta.url))
const page = process.argv[4] || '/console/dashboard'
const padBottom = Number(process.argv[5] || 0)

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
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1200, deviceScaleFactor: 2, mobile: false }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888' + page }, sessionId)
await new Promise((r) => setTimeout(r, 8000))

const raw = await send('Runtime.evaluate', {
  expression: 'JSON.stringify((function(){' +
    'var ps=Array.from(document.querySelectorAll(".panel"));' +
    'var p=ps.filter(function(e){return e.innerText.indexOf(' + JSON.stringify(key) + ')>=0})[0];' +
    'if(!p) return null;' +
    'var b=p.getBoundingClientRect();' +
    'return {x:b.x,y:b.y,width:b.width,height:b.height}})())',
  returnByValue: true
}, sessionId)
if (!raw.result.value || raw.result.value === 'null') {
  console.error('没找到包含「' + key + '」的面板')
  process.exit(1)
}
const box = JSON.parse(raw.result.value)
const shot = await send('Page.captureScreenshot', {
  format: 'png',
  clip: { x: box.x, y: box.y, width: box.width, height: box.height + padBottom, scale: 2 }
}, sessionId)
fs.writeFileSync(outFile, Buffer.from(shot.data, 'base64'))
console.log('已保存 ' + outFile + '  (' + Math.round(box.width) + 'x' + Math.round(box.height) + ')')
await send('Target.closeTarget', { targetId })
ws.close()
