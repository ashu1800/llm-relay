(async () => {
  const cv = document.createElement('canvas')
  cv.width = cv.height = 1
  const ctx = cv.getContext('2d', { willReadFrequently: true })
  const toRGB = (c) => {
    ctx.clearRect(0, 0, 1, 1)
    ctx.fillStyle = c
    ctx.fillRect(0, 0, 1, 1)
    const d = ctx.getImageData(0, 0, 1, 1).data
    return [d[0], d[1], d[2], d[3] / 255]
  }
  const lum = (p) => {
    const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4) }
    return 0.2126 * f(p[0]) + 0.7152 * f(p[1]) + 0.0722 * f(p[2])
  }
  const over = (fg, bg) => [0, 1, 2].map((i) => Math.round(fg[i] * fg[3] + bg[i] * (1 - fg[3])))
  const ratio = (a, b) => {
    const l1 = lum(a), l2 = lum(b)
    const hi = l1 > l2 ? l1 : l2, lo = l1 > l2 ? l2 : l1
    return (hi + 0.05) / (lo + 0.05)
  }
  // 向上找第一个不透明祖先背景。表格单元格自身是透明的，
  // 直接取会得到 rgba(0,0,0,0)，合成后变成黑色，对比度就全测错了。
  const baseBg = (el) => {
    let n = el
    while (n && n !== document.documentElement) {
      const c = toRGB(getComputedStyle(n).backgroundColor)
      if (c[3] > 0.9) return [c[0], c[1], c[2]]
      n = n.parentElement
    }
    return [255, 255, 255]
  }
  await new Promise((s) => setTimeout(s, 1500))
  const seen = {}, uniq = []
  let n = 0
  document.querySelectorAll('.provider-tag').forEach((el) => {
    n++
    const cs = getComputedStyle(el)
    const bg = baseBg(el)
    const fg = toRGB(cs.color)
    const tint = toRGB(cs.backgroundColor)
    const eff = over(tint, bg)
    const r = ratio(fg, eff)
    const k = el.textContent.trim()
    if (!seen[k]) {
      seen[k] = 1
      uniq.push({ 标签: k, 文字色: cs.color, 底色: eff, 对比度: Number(r.toFixed(2)), 达标: r >= 4.5 })
    }
  })
  return { 标签元素数: n, 明细: uniq }
})()