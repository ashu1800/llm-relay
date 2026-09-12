// 悬停到今天的 12 点格子上，确认自定义提示框出现且内容正确。
//
// 注意：页面侧表达式一律用「单引号字符串拼接」写，不要嵌模板字符串、
// 也不要在里面写带转义的正则/选择器 —— 之前就是这么把 \d 多转义一层
// 导致断言假失败，也导致过这里的 SyntaxError。
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
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 2, mobile: false }, sessionId)
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/dashboard' }, sessionId)
await new Promise((r) => setTimeout(r, 7000))

async function evaluate(expression, awaitPromise = false) {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise }, sessionId)
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400))
  return r.result.value
}

let pass = 0, fail = 0
function chk(name, cond, detail) {
  if (cond) { pass++; console.log('  [通过] ' + name) }
  else { fail++; console.log('  [失败] ' + name + (detail ? '  ' + detail : '')) }
}

const api = JSON.parse(await evaluate(
  'fetch("/api/admin/stats/heatmap?days=7").then(function(a){return a.json()}).then(function(d){return JSON.stringify(d.items)})', true))
console.log('接口返回: ' + JSON.stringify(api[0] || null))

chk('接口带 cost 字段', api.length > 0 && api[0].cost !== undefined)
chk('接口带 tokens 字段', api.length > 0 && api[0].tokens !== undefined)
chk('标题已去掉天数', await evaluate('document.querySelector(".section-title").innerText') === '请求热力图')
chk('格子不再用原生 title', await evaluate('!document.querySelector(".heatmap-cell").getAttribute("title")'))

// 悬停到今天 12 点（最后一行第 13 列）
const box = JSON.parse(await evaluate(
  'JSON.stringify((function(){' +
  'var a=Array.from(document.querySelectorAll(".heatmap-cell"));' +
  'var t=a[a.length-24+12];var b=t.getBoundingClientRect();' +
  'return {x:Math.round(b.x+b.width/2),y:Math.round(b.y+b.height/2)}})())'
))
await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: box.x, y: box.y, buttons: 0 }, sessionId)
await new Promise((r) => setTimeout(r, 800))

const tip = await evaluate(
  '(function(){var t=document.querySelector(".heat-tip");' +
  'if(!t) return null;' +
  // 不能用 split("\\n")：这层单引号字符串会把 \\n 解析成真正的换行，
  // 被求值的表达式里就会出现字符串中间裸换行 -> SyntaxError。
  // 用 fromCharCode 彻底绕开嵌套转义。
  'return JSON.stringify({lines:t.innerText.split(String.fromCharCode(10))})})()'
)
if (tip === null) {
  chk('悬停后出现提示框', false, '没找到 .heat-tip')
} else {
  const lines = JSON.parse(tip).lines
  console.log('提示框内容: ' + JSON.stringify(lines))
  chk('悬停后出现提示框', true)
  chk('四行内容', lines.length === 4, String(lines.length))
  chk('第一行是日期与小时', /^\d{4}-\d{2}-\d{2} \d{1,2}:00$/.test(lines[0]), lines[0])
  const hour12 = api.find((x) => x.hour === 12) || { requests: -1, cost: '-', tokens: -1 }
  chk('请求数与接口一致', lines[1] === hour12.requests + ' 次请求', lines[1] + ' vs ' + hour12.requests)
  chk('Token 与接口一致', lines[3].replace(/\D/g, '') === String(hour12.tokens), lines[3] + ' vs ' + hour12.tokens)
  const panel = JSON.parse(await evaluate(
    'JSON.stringify((function(){' +
    'var p=Array.from(document.querySelectorAll(".panel")).filter(function(e){return e.innerText.indexOf("请求热力图")>=0})[0];' +
    'var b=p.getBoundingClientRect();return {x:b.x,y:b.y,width:b.width,height:b.height}})())'
  ))
  const shot = await send('Page.captureScreenshot', {
    format: 'png',
    clip: { x: panel.x, y: Math.max(0, panel.y - 110), width: panel.width, height: panel.height + 120, scale: 2 }
  }, sessionId)
  fs.writeFileSync('.shots/heat-tip.png', Buffer.from(shot.data, 'base64'))
  console.log('已保存 .shots/heat-tip.png')
}

// 移开鼠标后应当消失
await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: 5, y: 5, buttons: 0 }, sessionId)
await new Promise((r) => setTimeout(r, 500))
chk('移开后提示框消失', await evaluate('!document.querySelector(".heat-tip")'))

await send('Target.closeTarget', { targetId })
ws.close()
console.log('')
console.log('通过 ' + pass + ' 项，失败 ' + fail + ' 项')
console.log(fail === 0 ? 'ALL_PASS' : 'HAS_FAILURE')
process.exit(fail === 0 ? 0 : 1)
