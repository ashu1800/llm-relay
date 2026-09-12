// 通过 Chrome DevTools Protocol 抓取参考站的真实计算样式，产出 ui-spec。
// 用法: node scripts/capture-ui.mjs [输出目录]
// 注意: 需禁用 Node 的环境代理（NODE_USE_ENV_PROXY=0），否则 127.0.0.1 会被代理劫持。
import { writeFileSync, mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import http from 'node:http'

const CDP_HOST = '127.0.0.1'
const CDP_PORT = 9222
const outDir = resolve(process.argv[2] || 'docs')

// 直接用 node:http，绕开 undici 的环境代理行为
function httpGet(path) {
  return new Promise((ok, fail) => {
    const req = http.request({ host: CDP_HOST, port: CDP_PORT, path: path, method: 'GET' }, (res) => {
      let buf = ''
      res.setEncoding('utf8')
      res.on('data', (c) => { buf += c })
      res.on('end', () => ok(buf))
    })
    req.on('error', fail)
    req.setTimeout(15000, () => { req.destroy(new Error('请求 ' + path + ' 超时')) })
    req.end()
  })
}

async function findTarget() {
  const list = JSON.parse(await httpGet('/json/list'))
  const pages = list.filter((t) => t.type === 'page' && t.webSocketDebuggerUrl)
  const target = pages.find((p) => p.url.indexOf('ohub') >= 0) || pages[0]
  if (!target) throw new Error('未找到可用的 Chrome 页面')
  return target
}

function connect(url) {
  return new Promise((ok, fail) => {
    const ws = new WebSocket(url)
    ws.onopen = () => ok(ws)
    ws.onerror = (e) => fail(new Error('WebSocket 连接失败: ' + String(e && e.message)))
  })
}

let msgId = 0
function send(ws, method, params) {
  const id = ++msgId
  return new Promise((ok, fail) => {
    const timer = setTimeout(() => fail(new Error(method + ' 超时')), 30000)
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

const EXTRACT = [
  '(() => {',
  '  const out = {};',
  '  const box = (el) => {',
  '    if (!el) return null;',
  '    const r = el.getBoundingClientRect();',
  '    const s = getComputedStyle(el);',
  '    return {',
  '      w: Math.round(r.width), h: Math.round(r.height),',
  '      padding: s.padding, margin: s.margin, gap: s.gap,',
  '      borderRadius: s.borderRadius, background: s.backgroundColor,',
  '      boxShadow: s.boxShadow, border: s.border,',
  '      fontSize: s.fontSize, fontWeight: s.fontWeight, fontFamily: s.fontFamily,',
  '      color: s.color, lineHeight: s.lineHeight, display: s.display',
  '    };',
  '  };',
  '  out.url = location.href;',
  '  out.title = document.title;',
  '  const vars = {};',
  '  for (const sheet of document.styleSheets) {',
  '    try {',
  '      for (const rule of sheet.cssRules) {',
  '        if (!rule.style) continue;',
  '        for (let i = 0; i < rule.style.length; i++) {',
  '          const n = rule.style[i];',
  '          if (n.indexOf("--") === 0) vars[n] = rule.style.getPropertyValue(n).trim();',
  '        }',
  '      }',
  '    } catch (e) {}',
  '  }',
  '  out.cssVars = vars;',
  '  out.htmlStyle = box(document.documentElement);',
  '  out.bodyStyle = box(document.body);',
  '  const sel = {',
  '    sider: ".ant-layout-sider",',
  '    siderChildren: ".ant-layout-sider-children",',
  '    menu: ".ant-menu",',
  '    menuItem: ".ant-menu-item",',
  '    menuItemSelected: ".ant-menu-item-selected",',
  '    header: ".ant-layout-header",',
  '    content: ".ant-layout-content",',
  '    card: ".ant-card",',
  '    cardHead: ".ant-card-head",',
  '    cardBody: ".ant-card-body",',
  '    table: ".ant-table",',
  '    tableHeadCell: ".ant-table-thead > tr > th",',
  '    tableCell: ".ant-table-tbody > tr > td",',
  '    btnPrimary: ".ant-btn-primary",',
  '    btn: ".ant-btn",',
  '    tag: ".ant-tag",',
  '    statTitle: ".ant-statistic-title",',
  '    statValue: ".ant-statistic-content-value",',
  '    select: ".ant-select-selector",',
  '    input: ".ant-input",',
  '    pagination: ".ant-pagination",',
  '    breadcrumb: ".ant-breadcrumb",',
  '    avatar: ".ant-avatar",',
  '    tabs: ".ant-tabs-nav",',
  '    progress: ".ant-progress"',
  '  };',
  '  out.elements = {};',
  '  for (const k in sel) {',
  '    const el = document.querySelector(sel[k]);',
  '    out.elements[k] = el ? Object.assign({ selector: sel[k], text: (el.textContent || "").trim().slice(0, 80) }, box(el)) : null;',
  '  }',
  '  out.menuItems = Array.prototype.slice.call(document.querySelectorAll(".ant-menu-item, .ant-menu-submenu-title")).map(function (el) {',
  '    return Object.assign({ text: (el.textContent || "").trim().slice(0, 40) }, box(el));',
  '  });',
  '  const walk = function (el, d) {',
  '    if (!el || d > 5) return null;',
  '    const cls = typeof el.className === "string" ? el.className.slice(0, 140) : "";',
  '    return {',
  '      tag: el.tagName.toLowerCase(),',
  '      cls: cls,',
  '      kids: Array.prototype.slice.call(el.children, 0, 14).map(function (c) { return walk(c, d + 1); }).filter(Boolean)',
  '    };',
  '  };',
  '  out.tree = walk(document.querySelector("#app") || document.body, 0);',
  '  out.texts = (document.body.innerText || "").split("\\n").map(function (s) { return s.trim(); }).filter(Boolean).slice(0, 400);',
  '  return JSON.stringify(out);',
  '})()'
].join('\n')

async function main() {
  const target = await findTarget()
  console.log('目标页面: ' + target.url)
  const ws = await connect(target.webSocketDebuggerUrl)
  await send(ws, 'Runtime.enable', {})
  const res = await send(ws, 'Runtime.evaluate', { expression: EXTRACT, returnByValue: true, awaitPromise: false })
  if (res.exceptionDetails) throw new Error('页面内提取失败: ' + JSON.stringify(res.exceptionDetails).slice(0, 500))
  const data = JSON.parse(res.result.value)
  mkdirSync(outDir, { recursive: true })
  const jsonPath = resolve(outDir, 'ui-spec.json')
  writeFileSync(jsonPath, JSON.stringify(data, null, 2), 'utf8')
  console.log('已写入 ' + jsonPath)
  console.log('CSS变量: ' + Object.keys(data.cssVars || {}).length)
  console.log('菜单项: ' + (data.menuItems || []).length)
  console.log('可见文本行: ' + (data.texts || []).length)
  ws.close()
}

main().catch((e) => { console.error('抓取失败: ' + e.message); process.exit(1) })
