// 确认西文现在到底落在哪个字体上。
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
  'var td=Array.from(document.querySelectorAll(".ant-table-tbody td")).filter(function(e){return (e.innerText||"").indexOf("2026")>=0})[0];' +
  'var ctx=document.createElement("canvas").getContext("2d");' +
  'var s="2026-09-12 13:05:50";' +
  'function w(f){ctx.font="14px "+f;return Math.round(ctx.measureText(s).width*100)/100}' +
  'function has(f){return document.fonts.check("14px "+f)}' +
  'var el=getComputedStyle(td).fontFamily;' +
  'return JSON.stringify({' +
  '  元素字族: el.slice(0,80),' +
  '  量_实际: w(el),' +
  '  量_Palatino: w("Palatino Linotype"),' +
  '  量_BookAntiqua: w("Book Antiqua"),' +
  '  量_Times: w("Times New Roman"),' +
  '  量_SimSun: w("SimSun"),' +
  '  可用_Palatino: has("Palatino Linotype"),' +
  '  可用_BookAntiqua: has("Book Antiqua")' +
  '},null,1)})()'
const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true }, sessionId)
console.log(r.exceptionDetails ? 'ERR ' + JSON.stringify(r.exceptionDetails).slice(0, 300) : r.result.value)
await send('Target.closeTarget', { targetId })
ws.close()
