// 验证「加载失败」不会显示成空列表。
//
// 做法：用 CDP 把渠道接口拦掉，再看页面 DOM。
// 这是这个改动唯一有意义的验证方式 —— 组件逻辑本身很简单，
// 真正要确认的是「接口挂了的时候用户看到的是什么」。
//
// 用 Node 24 内置的 WebSocket，不依赖 ws 包。
const CDP = 'http://127.0.0.1:9222/json/version'
const BASE = 'http://127.0.0.1:8888'

const ver = await (await fetch(CDP)).json()
const ws = new WebSocket(ver.webSocketDebuggerUrl)
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej })

let id = 0
const pending = new Map()
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data)
  if (m.id && pending.has(m.id)) {
    const { resolve, reject } = pending.get(m.id)
    pending.delete(m.id)
    m.error ? reject(new Error(JSON.stringify(m.error))) : resolve(m.result)
  }
}
function send(method, params, sessionId) {
  const mid = ++id
  return new Promise((resolve, reject) => {
    pending.set(mid, { resolve, reject })
    ws.send(JSON.stringify({ id: mid, method, params: params || {}, sessionId }))
  })
}

const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
await send('Page.enable', {}, sessionId)
await send('Network.enable', {}, sessionId)
// 一开始不拦截。之前这里直接就把接口拦上了，导致「正常情况」那一组
// 也在被拦截的状态下跑，凭空多出几条失败。
await send('Network.setBlockedURLs', { urls: [] }, sessionId)

async function goto(url) {
  await send('Page.navigate', { url }, sessionId)
  await new Promise((r) => setTimeout(r, 4000))
}
async function evalJs(expr) {
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true }, sessionId)
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300))
  return r.result.value
}

let pass = 0, fail = 0
function chk(name, cond, detail) {
  if (cond) { pass++; console.log('  [通过] ' + name) }
  else { fail++; console.log('  [失败] ' + name + (detail ? '  ' + detail : '')) }
}

console.log('=== 接口正常时：应显示表格 ===')
await goto(BASE + '/console/channels')
let text = await evalJs('document.body.innerText')
chk('页面渲染出表格行', /Ohub|NecoApi/.test(text), text.slice(0, 120).replace(/\n/g, ' | '))
chk('没有出现加载失败面板', !text.includes('渠道列表加载失败'))

console.log('')
console.log('=== 接口被拦掉后重新加载：应显示失败面板，而不是空表格 ===')
await send('Network.setBlockedURLs', { urls: ['*/api/admin/channels*'] }, sessionId)
await goto(BASE + '/console/channels')
await send('Page.reload', {}, sessionId)
await new Promise((r) => setTimeout(r, 4500))
text = await evalJs('document.body.innerText')
console.log('  页面文本片段: ' + JSON.stringify(text.replace(/\n+/g, ' | ').slice(0, 200)))
chk('显示了失败面板标题', text.includes('渠道列表加载失败'))
chk('给出了重试入口', text.includes('重试'))
chk('没有把失败显示成「暂无数据」', !text.includes('暂无数据'))
chk('没有把失败显示成空状态文案', !text.includes('还没有渠道'))
chk('提示是能看懂的中文，而不是浏览器的 Failed to fetch',
  text.includes('无法连接到后端服务'), text.replace(/\n+/g, ' | ').slice(0, 160))

// 恢复拦截后再验一次，确认失败面板会消失
console.log('')
console.log('=== 解除拦截后：应恢复为表格 ===')
await send('Network.setBlockedURLs', { urls: [] }, sessionId)
await send('Page.reload', {}, sessionId)
await new Promise((r) => setTimeout(r, 4500))
text = await evalJs('document.body.innerText')
chk('失败面板已消失', !text.includes('渠道列表加载失败'))
chk('数据重新出现', /Ohub|NecoApi/.test(text))

await send('Target.closeTarget', { targetId })
console.log('')
console.log('通过 ' + pass + ' 项，失败 ' + fail + ' 项')
console.log(fail === 0 ? 'ALL_PASS' : 'HAS_FAILURE')
ws.close()
process.exit(fail === 0 ? 0 : 1)
