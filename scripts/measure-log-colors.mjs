// 量日志表格各列的最终计算颜色，确认样式真的生效且互不相同。
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
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/logs' }, sessionId)
await new Promise((r) => setTimeout(r, 8000))

const expr = '(function(){' +
  'function col(sel){var e=document.querySelector(sel);if(!e)return "缺失";return getComputedStyle(e).color}' +
  'var tk=Array.from(document.querySelectorAll(".token-cell")).filter(function(e){return e.innerText.indexOf("31")>=0})[0];' +
  'var lats=Array.from(document.querySelectorAll(".lat-fast,.lat-mid,.lat-slow")).slice(0,6).map(function(e){' +
  '  return e.className+"="+e.innerText+" -> "+getComputedStyle(e).color});' +
  'return JSON.stringify({' +
  '  模型: col(".model-tag"),' +
  '  密钥: col(".cell-key"),' +
  '  渠道: col(".cell-channel"),' +
  '  词元三段的类: tk?Array.from(tk.children).map(function(c){return c.className+"="+c.innerText}):"无",' +
  '  词元三段的色: tk?Array.from(tk.children).filter(function(c){return c.innerText.trim()!=="/"}).map(function(c){' +
  '     return getComputedStyle(c).color}):"无",' +
  '  耗时样例: lats' +
  '},null,1)})()'
const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true }, sessionId)
console.log(r.exceptionDetails ? 'ERR ' + JSON.stringify(r.exceptionDetails).slice(0, 300) : r.result.value)
await send('Target.closeTarget', { targetId })
ws.close()
