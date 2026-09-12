// 实测表格容器与内容宽度，判断横向溢出多少。
import fs from 'node:fs'
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
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/channels' }, sessionId)
await new Promise((r) => setTimeout(r, 5000))

const expr = `(() => {
  const c = document.querySelector('.ant-table-container')
  const body = document.querySelector('.ant-table-body') || document.querySelector('.ant-table-content')
  const tbl = document.querySelector('.ant-table-content table') || document.querySelector('.ant-table-body table')
  const heads = [...document.querySelectorAll('.ant-table-thead th')].map(th => th.innerText.trim() + '=' + Math.round(th.getBoundingClientRect().width))
  return JSON.stringify({
    容器宽: c ? Math.round(c.getBoundingClientRect().width) : null,
    滚动区宽: body ? Math.round(body.getBoundingClientRect().width) : null,
    滚动区内容宽: body ? body.scrollWidth : null,
    表格宽: tbl ? Math.round(tbl.getBoundingClientRect().width) : null,
    表头: heads
  }, null, 1)
})()`
const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true }, sessionId)
console.log(r.result.value)
await send('Target.closeTarget', { targetId })
ws.close()
