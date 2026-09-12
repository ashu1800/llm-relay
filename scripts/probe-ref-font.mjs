// 判断 Harding 是否包含中文字形：同一串中文用 Harding 和用系统字体量出来的宽度
// 若相同，说明 Harding 没有这些字形、实际是回退渲染的。
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
await send('Page.navigate', { url: 'https://llm.ohub.vip/' }, sessionId)
await new Promise((r) => setTimeout(r, 9000))
await send('Runtime.evaluate', { expression: 'document.fonts.ready' }, sessionId)
await new Promise((r) => setTimeout(r, 1500))

const r = await send('Runtime.evaluate', {
  expression: '(function(){' +
    'var c=document.createElement("canvas").getContext("2d");' +
    'function w(f,t){c.font=f;return Math.round(c.measureText(t).width*100)/100}' +
    'return JSON.stringify({' +
    '  中文_Harding: w("16px Harding","请求数量"),' +
    '  中文_宋体: w("16px SimSun","请求数量"),' +
    '  中文_无衬线: w("16px sans-serif","请求数量"),' +
    '  英文_Harding: w("16px Harding","Request"),' +
    '  英文_宋体: w("16px SimSun","Request"),' +
    '  英文_无衬线: w("16px sans-serif","Request"),' +
    '  数字_Harding: w("16px Harding","0123456789"),' +
    '  数字_无衬线: w("16px sans-serif","0123456789")' +
    '},null,1)})()',
  returnByValue: true
}, sessionId)
console.log(r.result.value)
await send('Target.closeTarget', { targetId })
ws.close()
