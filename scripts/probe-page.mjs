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
// 页面侧的异常与 console.error 一并收集：只断言「元素在不在」的话，
// 一个抛了异常的页面在探针里看起来完全正常（元素早就渲染出来了）
const pageErrors = []
ws.onmessage = (e) => {
  const m = JSON.parse(e.data)
  if (m.method === 'Runtime.exceptionThrown') {
    const d = m.params?.exceptionDetails
    pageErrors.push('异常: ' + (d?.exception?.description || d?.text || '未知'))
  }
  if (m.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(m.params?.type)) {
    pageErrors.push(m.params.type + ': ' + (m.params.args || []).map((a) => a.value ?? a.description ?? '').join(' '))
  }
  if (m.method === 'Log.entryAdded' && ['error'].includes(m.params?.entry?.level)) {
    pageErrors.push('日志: ' + m.params.entry.text)
  }
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
await send('Log.enable', {}, sessionId)
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId)
// 让页面认为自己有焦点：CDP 打开的标签页在后台，浏览器会**暂停
// requestAnimationFrame**，于是任何滚动/补间动画都不会跑 ——
// 实测「数字滚动」时采样到的值一直不动，就是这个原因，不是功能坏了。
await send('Emulation.setFocusEmulationEnabled', { enabled: true }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888' + path }, sessionId)
await new Promise((r) => setTimeout(r, Number(waitMs) || 4000))
const res = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true }, sessionId)
if (res.exceptionDetails) {
  console.log('页面内异常: ' + JSON.stringify(res.exceptionDetails.exception && res.exceptionDetails.exception.description || res.exceptionDetails))
} else {
  const v = res.result.value
  console.log(typeof v === 'string' ? v : JSON.stringify(v, null, 2))
}
if (pageErrors.length) {
  // 去重：同一个错误常被异常与日志两条通道各报一次
  console.log('页面错误(' + pageErrors.length + '):')
  for (const line of [...new Set(pageErrors)].slice(0, 10)) console.log('  ' + line)
} else {
  console.log('页面错误: 无')
}
await send('Target.closeTarget', { targetId })
ws.close()
