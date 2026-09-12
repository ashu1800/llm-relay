// 端到端核对热力图：接口返回的每个数据点，都必须在页面上出现在正确的行与列。
//
// 页面侧只负责把 DOM 里的原始文本取出来，判断全部放在 Node 里做。
// 上一版把正则写进页面侧的表达式，嵌套模板字符串把 \d 多转义了一层，
// 变成匹配「反斜杠 + d」，于是永远匹配不上、报告「没有任何数据格子」——
// 而实际上数据是全对的。差点据此去改一段本来正确的代码。
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
await send('Page.navigate', { url: 'http://127.0.0.1:8888/console/dashboard' }, sessionId)
await new Promise((r) => setTimeout(r, 8000))

async function evaluate(expression, awaitPromise = false) {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise }, sessionId)
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300))
  return r.result.value
}

const raw = await evaluate(
  'JSON.stringify({' +
  '  rowLabels: Array.from(document.querySelectorAll(".heatmap-row-labels .heatmap-row-label")).map(function(e){return e.innerText}),' +
  '  keys: Array.from(document.querySelectorAll(".heatmap-cells .heatmap-cell")).map(function(e){return e.dataset.key}),' +
  '  levels: Array.from(document.querySelectorAll(".heatmap-cells .heatmap-cell")).map(function(e){return e.className.split("heatmap-cell-level-")[1] || ""}),' +
  '  summary: (document.querySelector(".panel-note") || {}).innerText || "",' +
  '  gridBottom: Math.round((document.querySelector(".summary-grid") || {getBoundingClientRect: function(){return {bottom: -1}}}).getBoundingClientRect().bottom),' +
  '  panelBottom: Math.round((Array.from(document.querySelectorAll(".panel")).find(function(p){return p.innerText.indexOf("请求热力图") >= 0}) || {getBoundingClientRect: function(){return {bottom: -2}}}).getBoundingClientRect().bottom),' +
  '  cellsW: Math.round((document.querySelector(".heatmap-cells") || {getBoundingClientRect: function(){return {width: 0}}}).getBoundingClientRect().width),' +
  '  title: (document.querySelector(".section-title") || {}).innerText || ""' +
  '})'
)
const dom = JSON.parse(raw)
const api = JSON.parse(await evaluate(
  'fetch("/api/admin/stats/heatmap?days=7").then(function(r){return r.json()}).then(function(d){return JSON.stringify(d.items)})',
  true
))

let pass = 0, fail = 0
function chk(name, cond, detail) {
  if (cond) { pass++; console.log('  [通过] ' + name) }
  else { fail++; console.log('  [失败] ' + name + (detail ? '  ' + detail : '')) }
}

console.log('面板标题: ' + dom.title + '   摘要: ' + dom.summary)
console.log('行标签: ' + dom.rowLabels.join(' '))
console.log('格子数: ' + dom.keys.length)
console.log('接口点数: ' + api.length)
console.log('')

// 标题不再带天数（用户要求去掉），这里只断言标题本身
chk('标题是「请求热力图」', dom.title === '请求热力图', dom.title)
chk('行数 = 7', dom.rowLabels.length === 7, String(dom.rowLabels.length))
chk('格子数 = 7 x 24', dom.keys.length === 168, String(dom.keys.length))

const today = new Date()
const pad = (n) => String(n).padStart(2, '0')
const todayLabel = pad(today.getMonth() + 1) + '-' + pad(today.getDate())
chk('最后一行是今天', dom.rowLabels[6] === todayLabel, dom.rowLabels[6] + ' vs ' + todayLabel)
const days = []
for (let i = 6; i >= 0; i--) {
  const d = new Date(); d.setDate(d.getDate() - i)
  days.push(pad(d.getMonth() + 1) + '-' + pad(d.getDate()))
}
chk('行标签是从早到晚且连续', JSON.stringify(dom.rowLabels) === JSON.stringify(days),
    dom.rowLabels.join(',') + ' vs ' + days.join(','))

// 接口的每个点都要落在正确的行列，且档位与请求数相符。
// 位置用 data-key 核对，数值用档位核对（0~4 表示相对强度）。
const maxReq = api.reduce((a, b) => Math.max(a, b.requests), 0)
function expectLevel(n) {
  if (!n || n <= 0) return '0'
  if (maxReq <= 1) return '4'
  const r = n / maxReq
  if (r <= 0.25) return '1'
  if (r <= 0.5) return '2'
  if (r <= 0.75) return '3'
  return '4'
}
let missing = 0, wrong = 0
for (const it of api) {
  const idx = days.indexOf(it.day.slice(5))
  if (idx < 0) { missing++; continue }
  const cellIdx = idx * 24 + it.hour
  const want = it.day + '#' + it.hour
  const got = dom.keys[cellIdx] || ''
  const lv = dom.levels[cellIdx] || ''
  if (got !== want || lv !== expectLevel(it.requests)) {
    wrong++
    console.log('    行 ' + it.day + ' ' + it.hour + '时 期望 key=' + want + ' 档' + expectLevel(it.requests) +
                '，实际 key=' + got + ' 档' + lv)
  }
}
chk('接口的每个点都出现在正确的行列', missing === 0 && wrong === 0,
    '缺失 ' + missing + ' 错位 ' + wrong)
chk('接口总次数等于面板摘要', dom.summary.indexOf(String(api.reduce((a, b) => a + b.requests, 0))) >= 0,
    dom.summary)

// 布局：面板底边必须和左侧卡片区齐平。
// .panel 全局带 margin-bottom: 8px，作为网格项时会吃掉面板高度，
// 曾经因此比卡片区矮 8px、底边错开。
chk('热力图面板底边与卡片区齐平', dom.gridBottom === dom.panelBottom,
    '卡片区 ' + dom.gridBottom + ' vs 面板 ' + dom.panelBottom)

// 格子要铺满可用宽度，不能缩在一侧留出空白
const panelW = JSON.parse(await evaluate(
  'JSON.stringify(Math.round((Array.from(document.querySelectorAll(".panel")).find(function(p){return p.innerText.indexOf("请求热力图") >= 0})).getBoundingClientRect().width))'
))
chk('格子区铺满面板宽度', dom.cellsW > panelW * 0.8, dom.cellsW + ' / 面板 ' + panelW)

await send('Target.closeTarget', { targetId })
ws.close()
console.log('')
console.log('通过 ' + pass + ' 项，失败 ' + fail + ' 项')
console.log(fail === 0 ? 'ALL_PASS' : 'HAS_FAILURE')
process.exit(fail === 0 ? 0 : 1)
