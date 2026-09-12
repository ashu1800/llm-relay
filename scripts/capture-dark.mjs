// 强制切到暗色主题后整页截图，用来检查暗色下的可读性。
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
const page = process.argv[2] || '/console/dashboard'
const outFile = process.argv[3] || '.shots/dark.png'
const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
await send('Page.enable', {}, sessionId)
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sessionId)

// 先把偏好写成 dark 再进页面，避免依赖「点一下切一次」的当前状态
await send('Page.navigate', { url: 'http://127.0.0.1:8888/' }, sessionId)
await new Promise((r) => setTimeout(r, 1500))
await send('Runtime.evaluate', { expression: 'localStorage.setItem("llm-relay-theme","dark")' }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888' + page }, sessionId)
await new Promise((r) => setTimeout(r, 7000))

const theme = await send('Runtime.evaluate', {
  expression: 'document.documentElement.getAttribute("data-theme")',
  returnByValue: true
}, sessionId)
console.log('主题: ' + theme.result.value)

const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true }, sessionId)
fs.writeFileSync(outFile, Buffer.from(shot.data, 'base64'))
console.log('已保存 ' + outFile)
await send('Target.closeTarget', { targetId })
ws.close()
