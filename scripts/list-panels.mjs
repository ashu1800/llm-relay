// 列出路由分析页所有面板标题与其中的画布尺寸。
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
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1400, deviceScaleFactor: 1, mobile: false }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/route-analysis' }, sessionId)
await new Promise((r) => setTimeout(r, 7000))

const r = await send('Runtime.evaluate', {
  expression: 'JSON.stringify(Array.from(document.querySelectorAll(".panel")).map(function(p){' +
    'var h=p.querySelector(".section-title");' +
    'var cv=p.querySelector("canvas");' +
    'return {title: h?h.innerText:"(无标题)", canvas: cv?Math.round(cv.getBoundingClientRect().width)+"x"+Math.round(cv.getBoundingClientRect().height):"无"}}))',
  returnByValue: true
}, sessionId)
console.log(r.result.value)
await send('Target.closeTarget', { targetId })
ws.close()
