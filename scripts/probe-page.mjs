// 通用页面探针：CDP 打开指定路径，等页面渲染完，在页面里求值一段表达式。
//
// 为什么要有这个：这个项目的验收一律以「量出来的值」为准，
// 而每次临时拼一个 CDP 脚本既慢又容易在引号/转义上出错。
// 用法：node scripts/probe-page.mjs <路径> <页面侧表达式|@表达式文件> [等待毫秒]
//
// 表达式也可以写成 @文件：从命令行传带引号的 JS 要跨 PowerShell/bash 两层转义，
// 实测极容易出错（引号被吃掉、$ 被展开），写进文件再引用就没有这个问题。
import { readFileSync } from 'node:fs'
const [path, exprArg, waitMs] = process.argv.slice(2)
if (!path || !exprArg) {
  console.error('用法: node scripts/probe-page.mjs <路径> <页面侧表达式|@表达式文件> [等待毫秒]')
  process.exit(2)
}
const expr = exprArg.startsWith('@') ? readFileSync(exprArg.slice(1), 'utf8') : exprArg
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
await send('Runtime.enable', {}, sessionId)
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888' + path }, sessionId)
await new Promise((r) => setTimeout(r, Number(waitMs) || 4000))
const res = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true }, sessionId)
if (res.exceptionDetails) {
  console.log('页面内异常: ' + JSON.stringify(res.exceptionDetails.exception && res.exceptionDetails.exception.description || res.exceptionDetails))
} else {
  const v = res.result.value
  console.log(typeof v === 'string' ? v : JSON.stringify(v, null, 2))
}
await send('Target.closeTarget', { targetId })
ws.close()
