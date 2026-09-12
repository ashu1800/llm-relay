// 量请求日志页「模型胶囊」的最终颜色，两件事必须成立：
//   1. 不同的模型真的不同色 —— 色相由模型名派生，撞色就等于没做
//   2. 胶囊文字对底色的对比度 ≥ 4.5:1（WCAG AA 正文标准），浅色与深色主题各测一遍
//
// 用法: NODE_USE_ENV_PROXY=0 node scripts/check-model-colors.mjs
// 依赖: Chrome 带 --remote-debugging-port=9222 在跑（与其它 capture-*.mjs 相同）
//
// 为什么必须在浏览器里量：oklch 的实际渲染值、半透明底色与面板底色的合成结果、
// 深浅主题各自的明度，都只有浏览器算得准；拿 CSS 源码推是推不出结论的。
import fs from 'node:fs'

const URL_LOG = 'http://127.0.0.1:8888/console/logs'
const OUT_DIR = '.shots'
const THEMES = ['light', 'dark']

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
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
await send('Page.enable', {}, sessionId)
await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false }, sessionId)

const evalJs = async (expression) => {
  const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }, sessionId)
  if (r.exceptionDetails) throw new Error('页面内报错: ' + JSON.stringify(r.exceptionDetails).slice(0, 300))
  return r.result.value
}

// 页内量色：canvas 解析颜色 -> sRGB，再按 WCAG 算对比度
const MEASURE = `(function(){
  var cv=document.createElement('canvas');cv.width=cv.height=1
  var ctx=cv.getContext('2d',{willReadFrequently:true})
  function rgb(c){ctx.clearRect(0,0,1,1);ctx.fillStyle=c;ctx.fillRect(0,0,1,1)
    var d=ctx.getImageData(0,0,1,1).data;return [d[0],d[1],d[2],d[3]/255]}
  function lum(p){function f(v){v/=255;return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4)}
    return 0.2126*f(p[0])+0.7152*f(p[1])+0.0722*f(p[2])}
  function over(fg,bg){return [0,1,2].map(function(i){return Math.round(fg[i]*fg[3]+bg[i]*(1-fg[3]))})}
  function ratio(a,b){var l1=lum(a),l2=lum(b),hi=l1>l2?l1:l2,lo=l1>l2?l2:l1;return (hi+0.05)/(lo+0.05)}
  // 表格单元格自身透明，直接取会合成成黑色，必须向上找第一个不透明祖先
  function baseBg(el){var n=el
    while(n&&n!==document.documentElement){var c=rgb(getComputedStyle(n).backgroundColor)
      if(c[3]>0.9)return [c[0],c[1],c[2]];n=n.parentElement}
    return [255,255,255]}
  var rows=[]
  document.querySelectorAll('.model-tag').forEach(function(el){
    var cs=getComputedStyle(el),bg=baseBg(el),eff=over(rgb(cs.backgroundColor),bg)
    var t=el.getBoundingClientRect()
    rows.push({
      模型: el.textContent.trim(),
      色相: el.style.getPropertyValue('--mt-h').trim(),
      文字色: cs.color,
      合成底色: 'rgb('+eff.join(',')+')',
      对比度: Number(ratio(rgb(cs.color),eff).toFixed(2)),
      宽度: Math.round(t.width),
      列宽: Math.round(el.parentElement.getBoundingClientRect().width),
      溢出: el.scrollWidth > el.clientWidth + 1
    })
  })
  var table=document.querySelector('.ant-table')
  var b=table?table.getBoundingClientRect():null
  return JSON.stringify({
    主题: document.documentElement.getAttribute('data-theme'),
    行数: rows.length,
    明细: rows,
    表格框: b?{x:b.x,y:b.y,width:Math.min(b.width,1180),height:Math.min(b.height,520)}:null
  })
})()`

// 记住用户原本的主题，测完要还回去 —— 这是用户自己的 Chrome 配置
await send('Page.navigate', { url: URL_LOG }, sessionId)
await sleep(6000)
const original = await evalJs("localStorage.getItem('llm-relay-theme') || 'light'")

const results = {}
for (const theme of THEMES) {
  await evalJs(`localStorage.setItem('llm-relay-theme', '${theme}')`)
  await send('Page.navigate', { url: URL_LOG }, sessionId)
  await sleep(7000)
  const raw = JSON.parse(await evalJs(MEASURE))
  results[theme] = raw
  if (raw.表格框) {
    const s = await send('Page.captureScreenshot', {
      format: 'png',
      clip: { x: raw.表格框.x, y: raw.表格框.y, width: raw.表格框.width, height: raw.表格框.height, scale: 2 }
    }, sessionId)
    fs.writeFileSync(`${OUT_DIR}/model-tags-${theme}.png`, Buffer.from(s.data, 'base64'))
  }
}

// 还原主题
await evalJs(`localStorage.setItem('llm-relay-theme', '${original}')`)
await send('Target.closeTarget', { targetId })
ws.close()

// ---- 汇总 ----
let failed = 0
const NAME = { light: '浅色主题', dark: '深色主题' }
for (const theme of THEMES) {
  const r = results[theme]
  const rows = r.明细
  const models = [...new Set(rows.map((x) => x.模型))]
  const byHue = new Map()
  for (const x of rows) {
    if (!byHue.has(x.色相)) byHue.set(x.色相, [])
    byHue.get(x.色相).push(x.模型)
  }
  const clash = [...byHue.entries()].filter(([, names]) => new Set(names).size > 1)
  const min = rows.length ? Math.min(...rows.map((x) => x.对比度)) : 0
  const bad = rows.filter((x) => x.对比度 < 4.5)
  const overflow = rows.filter((x) => x.溢出)

  console.log(`\n=== ${NAME[theme]}（data-theme=${r.主题}）===`)
  console.log(`  胶囊 ${rows.length} 个，模型 ${models.length} 种，色相 ${byHue.size} 种`)
  for (const x of rows.slice(0, 8)) {
    console.log(`    ${x.模型.padEnd(24)} 色相 ${String(x.色相).padStart(3)}  ${x.文字色.padEnd(28)} 底 ${x.合成底色.padEnd(18)} 对比度 ${x.对比度}`)
  }
  if (rows.length > 8) console.log(`    ...（共 ${rows.length} 行，其余略）`)
  console.log(`  对比度最低 ${min} —— ${bad.length ? '不达标 ' + bad.length + ' 个' : '全部达标（≥4.5）'}`)
  if (clash.length) console.log(`  撞色：${clash.map(([h, n]) => h + ' -> ' + [...new Set(n)].join('/')).join('，')}`)
  else console.log('  撞色：无')
  if (overflow.length) console.log(`  长名字被省略号截断（有 title 提示，可接受）: ${[...new Set(overflow.map((x) => x.模型))].join(', ')}`)

  if (bad.length || clash.length) failed++
}

console.log(`\n结论：${failed ? '不通过（见上）' : '通过'}；截图已存到 ${OUT_DIR}/model-tags-light.png 与 ${OUT_DIR}/model-tags-dark.png`)
process.exit(failed ? 1 : 0)
