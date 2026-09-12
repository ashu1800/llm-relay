// 截图任意页面到 PNG（CDP），用于「必须看一眼」的视觉验收。
//
// 为什么单独一个脚本：verify 脚本只能量数值，而图标、配色、间距这类东西
// 只能看图。
// 用法：node scripts/shot-page.mjs <url> <输出文件> [宽] [高] [等待毫秒] [截图前执行的表达式文件]
import { readFileSync, writeFileSync } from 'node:fs'

const [url, out, w, h, waitMs, evalFile] = process.argv.slice(2)
if (!url || !out) {
  console.error('用法: node scripts/shot-page.mjs <url> <输出文件> [宽] [高] [等待毫秒] [截图前执行的表达式文件]')
  process.exit(2)
}
const width = Number(w) || 1280
const height = Number(h) || 800
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
await send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 2, mobile: false }, sessionId)
await send('Page.navigate', { url }, sessionId)
await new Promise((r) => setTimeout(r, Number(waitMs) || 4000))
// 需要先点开弹窗、切页签的截图，用第 6 个参数在截图前跑一段页面脚本。
// 没有这一步就只能截「页面刚打开的样子」，而弹窗往往才是要看的东西
if (evalFile) {
  // 与 probe-page.mjs 用同一个约定：@ 开头表示「从文件读表达式」，
  // 免得在 PowerShell 里为一堆引号转义（裸 @x 还会被当成数组语法）
  const expr = readFileSync(evalFile.startsWith('@') ? evalFile.slice(1) : evalFile, 'utf8')
  const res = await send(
    'Runtime.evaluate',
    { expression: expr, awaitPromise: true, returnByValue: true },
    sessionId
  )
  if (res.exceptionDetails) {
    console.error('表达式执行失败: ' + JSON.stringify(res.exceptionDetails.exception))
  } else if (res.result && res.result.value !== undefined) {
    console.log('表达式结果: ' + res.result.value)
  }
  // 表达式里通常已经等过动画，这里再留一点余量给弹窗的淡入
  await new Promise((r) => setTimeout(r, 400))
}
const shot = await send('Page.captureScreenshot', { format: 'png' }, sessionId)
writeFileSync(out, Buffer.from(shot.data, 'base64'))
console.log('已保存 ' + out)
await send('Target.closeTarget', { targetId })
ws.close()
