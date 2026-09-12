// 把调试用的 Chrome 打开到参考站登录页并置顶，供用户手动登录。
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
// 复用现有标签页而不是新开，避免留一堆窗口
const targets = await send('Target.getTargets')
const page = targets.targetInfos.filter(function (t) { return t.type === 'page' })[0]
const targetId = page ? page.targetId : (await send('Target.createTarget', { url: 'about:blank' })).targetId
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
await send('Page.enable', {}, sessionId)
await send('Page.navigate', { url: 'https://llm.ohub.vip/user/login' }, sessionId)
await send('Page.bringToFront', {}, sessionId)
await new Promise((r) => setTimeout(r, 5000))
const r = await send('Runtime.evaluate', { expression: 'location.href + " | " + document.title', returnByValue: true }, sessionId)
console.log('已打开: ' + r.result.value)
ws.close()
