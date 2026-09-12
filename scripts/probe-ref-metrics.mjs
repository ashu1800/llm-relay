// 在参考站（Harding 真实可用）上量同一字符串，找出最接近 Harding 的系统衬线。
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
const t = await send('Target.getTargets')
const page = t.targetInfos.filter(function (x) { return x.type === 'page' && x.url.indexOf('ohub.vip') >= 0 })[0]
const { sessionId } = await send('Target.attachToTarget', { targetId: page.targetId, flatten: true })
await new Promise((r) => setTimeout(r, 1500))

const expr = '(function(){' +
  'var ctx=document.createElement("canvas").getContext("2d");' +
  'var s="2026-09-12 13:05:50";' +
  'function w(f){ctx.font="14px "+f;return Math.round(ctx.measureText(s).width*100)/100}' +
  'var td=document.querySelector(".ant-table-tbody td");' +
  'return JSON.stringify({' +
  '  目标串: s,' +
  '  Harding: w("Harding"),' +
  '  BookAntiqua: w("Book Antiqua"),' +
  '  Palatino: w("Palatino Linotype"),' +
  '  Times: w("Times New Roman"),' +
  '  Georgia: w("Georgia"),' +
  '  SimSun: w("SimSun"),' +
  '  表格实际字族: td?getComputedStyle(td).fontFamily.slice(0,50):"?"' +
  '},null,1)})()'
const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true }, sessionId)
console.log(r.exceptionDetails ? 'ERR ' + JSON.stringify(r.exceptionDetails).slice(0, 300) : r.result.value)
ws.close()
