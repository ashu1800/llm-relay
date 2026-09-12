// CDP 截图工具：为指定 URL 新建标签页并截图，不影响已有标签
// 用法: node scripts/screenshot.mjs <url> <输出png> [宽] [高]
import { writeFileSync, mkdirSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import http from 'node:http'

const url = process.argv[2]
const outPng = resolve(process.argv[3])
const width = parseInt(process.argv[4] || '1440', 10)
const height = parseInt(process.argv[5] || '900', 10)
if (!url || !outPng) { console.error('用法: node screenshot.mjs <url> <out.png> [w] [h]'); process.exit(1) }

function httpReq(method, path) {
  return new Promise((ok, fail) => {
    const req = http.request({ host: '127.0.0.1', port: 9222, path: path, method: method }, (res) => {
      let buf = ''
      res.setEncoding('utf8')
      res.on('data', (c) => { buf += c })
      res.on('end', () => ok(buf))
    })
    req.on('error', fail)
    req.setTimeout(20000, () => req.destroy(new Error('超时')))
    req.end()
  })
}

function connect(wsUrl) {
  return new Promise((ok, fail) => {
    const ws = new WebSocket(wsUrl)
    ws.onopen = () => ok(ws)
    ws.onerror = () => fail(new Error('WebSocket 连接失败'))
  })
}

let msgId = 0
function send(ws, method, params) {
  const id = ++msgId
  return new Promise((ok, fail) => {
    const timer = setTimeout(() => fail(new Error(method + ' 超时')), 60000)
    const onMsg = (ev) => {
      const m = JSON.parse(ev.data)
      if (m.id !== id) return
      clearTimeout(timer)
      ws.removeEventListener('message', onMsg)
      if (m.error) fail(new Error(method + ': ' + JSON.stringify(m.error)))
      else ok(m.result)
    }
    ws.addEventListener('message', onMsg)
    ws.send(JSON.stringify({ id: id, method: method, params: params }))
  })
}

async function main() {
  const created = JSON.parse(await httpReq('PUT', '/json/new?' + encodeURIComponent(url)))
  if (!created.webSocketDebuggerUrl) throw new Error('创建标签页失败: ' + JSON.stringify(created).slice(0, 200))
  const ws = await connect(created.webSocketDebuggerUrl)
  await send(ws, 'Page.enable', {})
  await send(ws, 'Emulation.setDeviceMetricsOverride', { width: width, height: height, deviceScaleFactor: 1, mobile: false })
  await new Promise((r) => setTimeout(r, 6000))
  const shot = await send(ws, 'Page.captureScreenshot', { format: 'png', captureBeyondViewport: false })
  mkdirSync(dirname(outPng), { recursive: true })
  writeFileSync(outPng, Buffer.from(shot.data, 'base64'))
  console.log('已保存 ' + outPng)
  ws.close()
  await httpReq('GET', '/json/close/' + created.id).catch(() => {})
}

main().catch((e) => { console.error('截图失败: ' + e.message); process.exit(1) })
