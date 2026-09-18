// 页签心跳：让浏览器标签页自己变成一块迷你仪表。
//
// 站主不会一整天盯着看板 —— 人经常在别的标签页里干活。这个模块把
// 「系统活着、在进请求、有没有出事」搬到了页签图标上：
//   - 右下角角标实时显示今日请求数（品牌暖色）；
//   - 收到熔断 / 预算超支告警时角标转红，恢复后自动褪回；
//   - 没收到任何推送时保持原样，页签看起来和以前一模一样。
//
// 数据全部来自已有的 live 推送，零额外请求。重画有 10 秒节流：
// favicon 换得太勤除了费电没有任何收益。
import { onLive } from '@/composables/useLive'

let started = false
let baseIcon: HTMLImageElement | null = null
let baseReady = false
/** 今日请求数；null 表示还没收到过 stats 帧（此时不画角标） */
let requestsToday: number | null = null
/** 熔断中或预算超支：角标转红。recovered 后解除 */
let alarm = false
let lastDrawn = 0
let redrawTimer: number | null = null

/** 启动页签心跳。MainLayout 挂载时调用一次即可，重复调用是空操作。 */
export function startTabPulse() {
  if (started || typeof document === 'undefined') return
  started = true

  // 原图标作为画布底图：SVG 与页面同源，画进 canvas 不会污染导出
  baseIcon = new Image()
  baseIcon.onload = () => {
    baseReady = true
    draw()
  }
  baseIcon.src = '/favicon.svg'

  onLive('stats', (data: { requests?: number }) => {
    if (data && typeof data.requests === 'number') {
      requestsToday = data.requests
      scheduleRedraw()
    }
  })
  onLive('channel_health', (e: { kind?: string }) => {
    const was = alarm
    alarm = !!e && e.kind !== 'recovered'
    if (alarm !== was) scheduleRedraw()
  })
  onLive('budget_alert', () => {
    // 超支不设自动解除：后端预算告警每天每档只发一次，红色留到跨天更符合直觉
    if (!alarm) {
      alarm = true
      scheduleRedraw()
    }
  })
}

function scheduleRedraw() {
  const wait = Math.max(0, lastDrawn + 10000 - Date.now())
  if (redrawTimer !== null) return
  redrawTimer = window.setTimeout(() => {
    redrawTimer = null
    draw()
  }, wait)
}

/** 999 以内原样，往上进位成 1.2k / 12k，再大就封顶 —— 16px 的角标放不下更多字 */
function fmtCount(n: number) {
  if (n >= 100000) return '99k+'
  if (n >= 10000) return Math.round(n / 1000) + 'k'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k'
  return String(n)
}

function draw() {
  if (!baseReady || !baseIcon) return
  lastDrawn = Date.now()

  const canvas = document.createElement('canvas')
  canvas.width = 64
  canvas.height = 64
  const ctx = canvas.getContext('2d')
  if (!ctx) return
  ctx.drawImage(baseIcon, 0, 0, 64, 64)

  if (requestsToday !== null) {
    // 角标：右下角实心圆 + 白字。alarm 时转红 —— 颜色是这里唯一的告警语言，
    // 形状保持一致，余光才能「只注意到变色」而不是「重新认一遍图标」
    const label = fmtCount(requestsToday)
    ctx.font = '700 22px system-ui, sans-serif'
    const w = ctx.measureText(label).width
    const pad = 9
    const cx = 64 - (w / 2 + pad)
    const cy = 64 - 13
    const r = Math.max(w / 2 + pad, 14)
    ctx.beginPath()
    ctx.arc(cx, cy, r, 0, Math.PI * 2)
    ctx.fillStyle = alarm ? '#d5453a' : '#c87864'
    ctx.fill()
    ctx.fillStyle = '#fff'
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText(label, cx, cy + 1)
  } else if (alarm) {
    // 没有请求统计但有告警（比如页面刚开就熔断）：画一个纯红点
    ctx.beginPath()
    ctx.arc(52, 52, 12, 0, Math.PI * 2)
    ctx.fillStyle = '#d5453a'
    ctx.fill()
  }

  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  link.href = canvas.toDataURL('image/png')
}
