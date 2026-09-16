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
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/dashboard' }, sessionId)
await new Promise((r) => setTimeout(r, 8000))

const expr = '(function(){' +
  'function col(sel){var e=document.querySelector(sel);if(!e)return "缺失";return getComputedStyle(e).color}' +
  // 词元格 2026-09-16 起是上下两排（上排输入/输出、下排缓存/命中率）：
  // 颜色挂在「图标 + 数值」那一组（.tk-in/.tk-out/.tk-cache）上，
  // 图标靠 currentColor 继承 —— 这里连同图标一起量，顺便确认两者同色。
  'var tk=Array.from(document.querySelectorAll(".token-cell")).filter(function(e){return e.innerText.indexOf("31")>=0})[0];' +
  'function grp(sel){var e=tk?tk.querySelector(sel):null;if(!e)return "无";' +
  '  var ico=e.querySelector("svg");return {组:e.className,值:e.innerText,色:getComputedStyle(e).color,' +
  '    图标色:ico?getComputedStyle(ico).color:"无图标",图标宽:ico?Math.round(ico.getBoundingClientRect().width):0}}' +
  'var lats=Array.from(document.querySelectorAll(".lat-fast,.lat-mid,.lat-slow")).slice(0,6).map(function(e){' +
  '  return e.className+"="+e.innerText+" -> "+getComputedStyle(e).color});' +
  'return JSON.stringify({' +
  '  模型胶囊: col(".ant-table-tbody .ant-tag"),' +
  '  词元格: tk?{"上排":tk.querySelectorAll(".tk-line")[0].innerText.replace(/\\s+/g," "),' +
  '    "下排":tk.querySelectorAll(".tk-line")[1].innerText.replace(/\\s+/g," "),' +
  '    "行高":getComputedStyle(tk).lineHeight,"字号":getComputedStyle(tk).fontSize,' +
  '    "输入":grp(".tk-in"),"输出":grp(".tk-out"),"缓存":grp(".tk-cache")}:"无",' +
  '  耗时样例: lats' +
  '},null,1)})()'
const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true }, sessionId)
console.log(r.exceptionDetails ? 'ERR ' + JSON.stringify(r.exceptionDetails).slice(0, 300) : r.result.value)
await send('Target.closeTarget', { targetId })
ws.close()
