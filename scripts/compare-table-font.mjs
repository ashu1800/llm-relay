// 对比我们日志表和参考站日志表的实际字体度量。
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
async function probe(url, label) {
  const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
  await send('Page.enable', {}, sessionId)
  await send('Emulation.setDeviceMetricsOverride', { width: 1600, height: 1000, deviceScaleFactor: 1, mobile: false }, sessionId)
  await send('Page.navigate', { url }, sessionId)
  await new Promise((r) => setTimeout(r, 9000))
  const r = await send('Runtime.evaluate', {
    expression: '(function(){' +
      'var th=document.querySelector(".ant-table-thead th");' +
      'var td=document.querySelector(".ant-table-tbody td");' +
      'if(!th||!td) return "no table";' +
      'function d(e){var s=getComputedStyle(e);return {fs:s.fontSize,fw:s.fontWeight,lh:s.lineHeight,ff:s.fontFamily.slice(0,70),h:Math.round(e.getBoundingClientRect().height)}}' +
      'return JSON.stringify({th:d(th),td:d(td),行数:document.querySelectorAll(".ant-table-tbody tr").length})})()',
    returnByValue: true
  }, sessionId)
  console.log('--- ' + label + ' ---')
  console.log(r.result.value)
  await send('Target.closeTarget', { targetId })
}
await probe('http://127.0.0.1:8888/console/logs', '我们的日志页')
await probe('https://llm.ohub.vip/user/login', '参考站(未登录,仅确认可访问)')
ws.close()
