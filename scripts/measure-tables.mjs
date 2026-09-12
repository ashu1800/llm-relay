// 逐个列表页实测：表格内容宽 vs 容器宽。
// 溢出时 antd 会横向滚动，固定在右侧的列就会盖住左边最后一列。
const pages = [
  ['渠道管理', '/console/channels'],
  ['分组管理', '/console/groups'],
  ['密钥信息', '/console/keys'],
  ['请求日志', '/console/logs'],
  ['模型定价', '/console/pricing']
]
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

const expr = `(() => {
  const c = document.querySelector('.ant-table-container')
  const body = document.querySelector('.ant-table-body') || document.querySelector('.ant-table-content')
  const tbl = document.querySelector('.ant-table-content table') || document.querySelector('.ant-table-body table')
  if (!c || !tbl) return null
  const heads = [...document.querySelectorAll('.ant-table-thead th')].map(th => th.innerText.trim() + '=' + Math.round(th.getBoundingClientRect().width))
  return JSON.stringify({
    container: Math.round(c.getBoundingClientRect().width),
    table: Math.round(tbl.getBoundingClientRect().width),
    scrollArea: body ? Math.round(body.getBoundingClientRect().width) : null,
    heads
  })
})()`

for (const [name, p] of pages) {
  await send('Page.navigate', { url: 'http://127.0.0.1:8888' + p }, sessionId)
  await new Promise((r) => setTimeout(r, 4200))
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true }, sessionId)
  if (!r.result.value) { console.log(name.padEnd(10) + ' 没有表格'); continue }
  const d = JSON.parse(r.result.value)
  const over = d.table - d.container
  console.log(
    name.padEnd(10) + ' 容器=' + String(d.container).padEnd(6) +
    ' 表格=' + String(d.table).padEnd(6) +
    (over > 0 ? ' 溢出 ' + over + 'px  <== 固定列会盖住最后一列' : ' 装得下')
  )
  if (over > 0) console.log('           ' + d.heads.join('  '))
}
await send('Target.closeTarget', { targetId })
ws.close()
