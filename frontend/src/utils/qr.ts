// 二维码：qrcode-generator 的薄封装。
//
// 为什么用库而不是自己实现：QR 编码（RS 纠错、分块交错、掩码评分、
// 格式/版本信息）是规格密集且「自洽不等于正确」的活 —— 手写版本能
// 反读出正确码字，却没被真实扫码器认过，这种实现拿去做 2FA 绑定的
// 唯一通道是把用户锁在门外。qrcode-generator 是零依赖单文件实现，
// 被大量项目使用了十多年，扫码器兼容性久经检验。
//
// 本文件只做三件事：定档（byte 模式、ECC M）、把模块矩阵转成
// boolean[][]、渲染成 SVG 字符串。判定逻辑不在这里（组件里也不该写）。
import qrcode from 'qrcode-generator'

export interface QRMatrix {
  /** 边长（模块数） */
  size: number
  /** true = 深色模块 */
  modules: boolean[][]
}

/**
 * 生成二维码矩阵。
 * ECC 用 M（约 15% 容错）：认证器截图歪一点、屏幕上反光一点都能扫出来；
 * 更高等级会让模块变多，小屏上反而不易扫。
 */
export function encodeQR(text: string): QRMatrix {
  if (!text) throw new Error('二维码内容不能为空')
  // 0 = 自动选版本；M = 中等容错
  const qr = qrcode(0, 'M')
  qr.addData(text)
  qr.make()
  const size = qr.getModuleCount()
  const modules: boolean[][] = Array.from({ length: size }, (_, r) =>
    Array.from({ length: size }, (_, c) => qr.isDark(r, c))
  )
  return { size, modules }
}

/** 把矩阵渲染成 SVG 字符串（viewBox 单元 = 模块，放大交给 CSS）。 */
export function qrToSvg(qr: QRMatrix, quietZone = 4): string {
  const dim = qr.size + quietZone * 2
  let path = ''
  for (let r = 0; r < qr.size; r++) {
    for (let c = 0; c < qr.size; c++) {
      if (qr.modules[r][c]) path += `M${c + quietZone} ${r + quietZone}h1v1h-1z`
    }
  }
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${dim} ${dim}" ` +
    `shape-rendering="crispEdges"><rect width="${dim}" height="${dim}" fill="#fff"/>` +
    `<path d="${path}" fill="#000"/></svg>`
  )
}
