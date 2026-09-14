// 抓参考站请求日志表格的真实计算样式（登录态）。
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
const t = await send('Target.getTargets')
const page = t.targetInfos.filter(function (x) { return x.type === 'page' })[0]
const { sessionId } = await send('Target.attachToTarget', { targetId: page.targetId, flatten: true })
await send('Page.enable', {}, sessionId)
await send('Emulation.setDeviceMetricsOverride', { width: 1600, height: 1000, deviceScaleFactor: 2, mobile: false }, sessionId)
await new Promise((r) => setTimeout(r, 2500))

const expr = '(function(){' +
  'function pick(e){if(!e)return null;var s=getComputedStyle(e);var b=e.getBoundingClientRect();' +
  'return {文本:(e.innerText||"").replace(/\\s+/g," ").trim().slice(0,24),字族:s.fontFamily,字号:s.fontSize,字重:s.fontWeight,' +
  '行高:s.lineHeight,字距:s.letterSpacing,数字变体:s.fontVariantNumeric,特性:s.fontFeatureSettings,' +
  '高:Math.round(b.height),内边距:s.padding,颜色:s.color}}' +
  'var th=document.querySelector(".ant-table-thead th");' +
  'var tds=Array.from(document.querySelectorAll(".ant-table-tbody tr")).slice(0,2).map(function(tr){' +
  '  return Array.from(tr.querySelectorAll("td")).slice(0,5).map(pick)});' +
  'var ctx=document.createElement("canvas").getContext("2d");' +
  'var fam="Harding, STSong, SimSun, serif";' +
  'function w(f,t){ctx.font="14px "+f;return Math.round(ctx.measureText(t).width*100)/100}' +
  'return JSON.stringify({表头:pick(th),数据行:tds,' +
  '  纯Harding: w("Harding","2026-09-11 20:50:12 deepseek"),' +
  '  纯宋体: w("SimSun","2026-09-11 20:50:12 deepseek"),' +
  '  参考站链: w(fam,"2026-09-11 20:50:12 deepseek"),' +
  '  中文_链: w(fam,"请求时间"),' +
  '  中文_宋体: w("SimSun","请求时间")},null,1)})()'
const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true }, sessionId)
console.log(r.exceptionDetails ? 'ERR ' + JSON.stringify(r.exceptionDetails).slice(0, 400) : r.result.value)

const box = JSON.parse((await send('Runtime.evaluate', {
  expression: 'JSON.stringify((function(){var e=document.querySelector(".ant-table");if(!e)return {x:0,y:0,width:1200,height:600};var b=e.getBoundingClientRect();return {x:b.x,y:b.y,width:b.width,height:Math.min(b.height,700)}})())',
  returnByValue: true
}, sessionId)).result.value)
const shot = await send('Page.captureScreenshot', { format: 'png', clip: { x: box.x, y: box.y, width: box.width, height: box.height, scale: 2 } }, sessionId)
fs.writeFileSync(new URL('../.shots/ref-logs.png', import.meta.url), Buffer.from(shot.data, 'base64'))
console.log('已保存 .shots/ref-logs.png')
ws.close()
