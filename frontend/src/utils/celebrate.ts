// 里程碑彩带：庆祝这件事值得 5 秒钟的纸屑，不值得一个依赖库。
//
// 手写实现：往 body 上撒一把彩色小方片，CSS 动画负责下落与旋转，
// 动画结束整批移除。颜色取主题色板（浅深主题都成立），尊重系统的
// 「减少动态效果」设置 —— 那种情况下直接返回，庆祝留在心里。

type Piece = {
  el: HTMLDivElement
  x: number
  fall: number
  drift: number
  spin: number
  delay: number
}

const COLORS = ['#c87864', '#e0a83c', '#45c79a', '#4dd0e1', '#ab8ef2', '#f08a7a']

export function burstConfetti() {
  if (typeof document === 'undefined') return
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return

  const host = document.createElement('div')
  host.className = 'confetti-host'
  host.style.cssText =
    'position:fixed;inset:0;pointer-events:none;overflow:hidden;z-index:9999'
  document.body.appendChild(host)

  const pieces: Piece[] = []
  for (let i = 0; i < 72; i++) {
    const el = document.createElement('div')
    const size = 6 + Math.random() * 6
    el.style.cssText = [
      'position:absolute',
      `width:${size.toFixed(1)}px`,
      `height:${(size * 0.55).toFixed(1)}px`,
      `background:${COLORS[i % COLORS.length]}`,
      'border-radius:1px',
      'left:0;top:0',
      'will-change:transform,opacity'
    ].join(';')
    host.appendChild(el)
    // 从顶部两条「礼炮口」斜着喷出：中间偏左与偏右，落点自然铺开
    const fromLeft = i % 2 === 0
    pieces.push({
      el,
      x: fromLeft ? window.innerWidth * (0.1 + Math.random() * 0.2) : window.innerWidth * (0.7 + Math.random() * 0.2),
      fall: window.innerHeight * (0.9 + Math.random() * 0.4),
      drift: (fromLeft ? 1 : -1) * (60 + Math.random() * 160),
      spin: (Math.random() * 1080 - 540) * (fromLeft ? 1 : -1),
      delay: Math.random() * 350
    })
  }

  const t0 = performance.now()
  const DURATION = 4200
  const frame = (t: number) => {
    const p = (t - t0) / DURATION
    if (p >= 1) {
      host.remove()
      return
    }
    for (const pc of pieces) {
      const local = Math.max(0, Math.min(1, (t - t0 - pc.delay) / (DURATION - pc.delay)))
      if (local <= 0) continue
      // 先快速下坠后飘（ease-in），横向往外漂；快落地时淡出
      const eased = local * local
      const opacity = local > 0.82 ? (1 - local) / 0.18 : 1
      pc.el.style.opacity = String(opacity)
      pc.el.style.transform =
        `translate(${(pc.x + pc.drift * local).toFixed(1)}px, ${(eased * pc.fall).toFixed(1)}px) ` +
        `rotate(${(pc.spin * local).toFixed(1)}deg)`
    }
    requestAnimationFrame(frame)
  }
  requestAnimationFrame(frame)
}
