// 深度抓取参考站 DOM：class 样式映射 + 布局树 + 页面结构
// 用法: node scripts/capture-layout.mjs <输出文件> [导航URL]
import { writeFileSync, mkdirSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import http from 'node:http'

const CDP_HOST = '127.0.0.1'
const CDP_PORT = 9222
const outFile = resolve(process.argv[2] || 'docs/layout-spec.json')
const navUrl = process.argv[3] || ''

function httpGet(path) {
  return new Promise((ok, fail) => {
    const req = http.request({ host: CDP_HOST, port: CDP_PORT, path: path, method: 'GET' }, (res) => {
      let buf = ''
      res.setEncoding('utf8')
      res.on('data', (c) => { buf += c })
      res.on('end', () => ok(buf))
    })
    req.on('error', fail)
    req.setTimeout(15000, () => req.destroy(new Error('超时')))
    req.end()
  })
}

async function findTarget() {
  const list = JSON.parse(await httpGet('/json/list'))
  const pages = list.filter((t) => t.type === 'page' && t.webSocketDebuggerUrl)
  const t = pages.find((p) => p.url.indexOf('ohub') >= 0) || pages[0]
  if (!t) throw new Error('未找到页面')
  return t
}

function connect(url) {
  return new Promise((ok, fail) => {
    const ws = new WebSocket(url)
    ws.onopen = () => ok(ws)
    ws.onerror = () => fail(new Error('WebSocket 连接失败'))
  })
}

let msgId = 0
function send(ws, method, params) {
  const id = ++msgId
  return new Promise((ok, fail) => {
    const timer = setTimeout(() => fail(new Error(method + ' 超时')), 40000)
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

async function evaluate(ws, expr) {
  const res = await send(ws, 'Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true })
  if (res.exceptionDetails) throw new Error('页面内执行失败: ' + JSON.stringify(res.exceptionDetails).slice(0, 400))
  return res.result.value
}

// 收集每个 class 的典型样式
const CLASS_MAP = [
  '(() => {',
  '  const map = {};',
  '  const els = document.querySelectorAll("*")',
  '  for (const el of els) {',
  '    const cls = typeof el.className === "string" ? el.className.trim() : "";',
  '    if (!cls) continue;',
  '    const s = getComputedStyle(el);',
  '    const r = el.getBoundingClientRect();',
  '    for (const c of cls.split(/\\s+/)) {',
  '      if (!c || map[c]) continue;',
  '      map[c] = {',
  '        tag: el.tagName.toLowerCase(),',
  '        w: Math.round(r.width), h: Math.round(r.height),',
  '        bg: s.backgroundColor, color: s.color,',
  '        fs: s.fontSize, fw: s.fontWeight, lh: s.lineHeight,',
  '        pad: s.padding, mar: s.margin, br: s.borderRadius,',
  '        bs: s.boxShadow, border: s.borderWidth + " " + s.borderStyle + " " + s.borderColor,',
  '        display: s.display, pos: s.position, gap: s.gap,',
  '        text: (el.textContent || "").trim().replace(/\\s+/g, " ").slice(0, 50)',
  '      };',
  '    }',
  '  }',
  '  return JSON.stringify(map);',
  '})()'
].join('\n')

// 布局树：只保留有 class 或有尺寸意义的结构
const TREE = [
  '(() => {',
  '  const walk = (el, d) => {',
  '    if (!el || d > 7) return null;',
  '    const cls = typeof el.className === "string" ? el.className.trim() : "";',
  '    const s = getComputedStyle(el);',
  '    const r = el.getBoundingClientRect();',
  '    const node = {',
  '      t: el.tagName.toLowerCase(),',
  '      c: cls,',
  '      w: Math.round(r.width), h: Math.round(r.height),',
  '      d: s.display, bg: s.backgroundColor, fs: s.fontSize, pad: s.padding,',
  '      br: s.borderRadius, bs: s.boxShadow, txt: (el.textContent || "").trim().replace(/\\s+/g, " ").slice(0, 30)',
  '    };',
  '    if (d < 6) {',
  '      const kids = [];',
  '      for (const ch of el.children) {',
  '        const n = walk(ch, d + 1);',
  '        if (n) kids.push(n);',
  '      }',
  '      if (kids.length) node.k = kids;',
  '    }',
  '    return node;',
  '  };',
  '  return JSON.stringify(walk(document.querySelector("#app") || document.body, 0));',
  '})()'
].join('\n')

async function main() {
  const target = await findTarget()
  const ws = await connect(target.webSocketDebuggerUrl)
  await send(ws, 'Runtime.enable', {})
  await send(ws, 'Page.enable', {})

  if (navUrl) {
    console.log('导航到 ' + navUrl)
    await send(ws, 'Page.navigate', { url: navUrl })
    await new Promise((r) => setTimeout(r, 6000))
  }

  const info = await evaluate(ws, 'JSON.stringify({url: location.href, title: document.title, vw: innerWidth, vh: innerHeight})')
  console.log('当前页面: ' + info)

  const classMap = JSON.parse(await evaluate(ws, CLASS_MAP))
  const tree = JSON.parse(await evaluate(ws, TREE))

  mkdirSync(dirname(outFile), { recursive: true })
  writeFileSync(outFile, JSON.stringify({ page: JSON.parse(info), classMap: classMap, tree: tree }, null, 2), 'utf8')
  console.log('已写入 ' + outFile)
  console.log('class 数量: ' + Object.keys(classMap).length)
  ws.close()
}

main().catch((e) => { console.error('失败: ' + e.message); process.exit(1) })
