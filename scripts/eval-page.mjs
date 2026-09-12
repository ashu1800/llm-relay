// 在页面里执行一段 JS 并打印结果，用于核对计算样式这类无法从截图看出的东西。
import { WebSocket } from 'ws'

const url = process.argv[2]
const expr = process.argv[3]
if (!url || !expr) { console.error('用法: node eval-page.mjs <url> <js表达式>'); process.exit(1) }

const ver = await (await fetch('http://127.0.0.1:9222/json/version')).json()
const ws = new WebSocket(ver.webSocketDebuggerUrl, { maxPayload: 256 * 1024 * 1024 })
await new Promise((r, j) => { ws.once('open', r); ws.once('error', j) })

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
await send('Page.navigate', { url }, sessionId)
await new Promise((r) => setTimeout(r, 5000))

const res = await send('Runtime.evaluate', {
  expression: expr, returnByValue: true, awaitPromise: true
}, sessionId)

if (res.exceptionDetails) {
  console.log('页面内报错:', JSON.stringify(res.exceptionDetails.exception && res.exceptionDetails.exception.description || res.exceptionDetails))
} else {
  console.log(JSON.stringify(res.result.value, null, 2))
}
await send('Target.closeTarget', { targetId })
ws.close()
process.exit(0)
