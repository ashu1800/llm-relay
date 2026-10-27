import { describe, expect, it } from 'vitest'
import { encodeQR, qrToSvg } from './qr'

// QR 封装层单测。真正的「扫得出来」由 zxing 在开发期验证过（见开发记录），
// 这里守的是封装契约：尺寸合法、三个角有定位图案、时长内容选版正确、
// SVG 与矩阵一致 —— 这些一旦被改坏，界面上的二维码就会变成一张装饰画。
describe('encodeQR', () => {
  it('生成合法的模块数（版本 1 起，边长 = 17 + 4n）', () => {
    const { size, modules } = encodeQR('HELLO')
    expect(size).toBeGreaterThanOrEqual(21)
    expect((size - 17) % 4).toBe(0)
    expect(modules).toHaveLength(size)
    // 每行都是满宽的 boolean 行
    for (const row of modules) expect(row).toHaveLength(size)
  })

  it('三个角上有定位图案（7×7 外框 + 3×3 中心）', () => {
    const { size, modules } = encodeQR('HELLO')
    // 左上角外框的第一行与第一列都是深色
    for (let i = 0; i < 7; i++) {
      expect(modules[0][i]).toBe(true)
      expect(modules[i][0]).toBe(true)
    }
    // 中心 3×3 深色
    for (let r = 2; r <= 4; r++) for (let c = 2; c <= 4; c++) expect(modules[r][c]).toBe(true)
    // 三个角都要有：右上与左下
    expect(modules[0][size - 1]).toBe(true)
    expect(modules[size - 1][0]).toBe(true)
  })

  it('内容越长版本越大（otpauth URI 落在有对齐图案的版本上）', () => {
    const small = encodeQR('A')
    const big = encodeQR(
      'otpauth://totp/LLM%20Relay:admin?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=LLM%20Relay&algorithm=SHA1&digits=6&period=30'
    )
    expect(big.size).toBeGreaterThan(small.size)
  })

  it('空内容直接报错（不生成一张空白图）', () => {
    expect(() => encodeQR('')).toThrow()
  })
})

describe('qrToSvg', () => {
  it('viewBox 含静区，深色模块数与矩阵一致', () => {
    const qr = encodeQR('HELLO')
    const svg = qrToSvg(qr, 4)
    const dim = qr.size + 8
    expect(svg).toContain(`viewBox="0 0 ${dim} ${dim}"`)
    // 每个深色模块一条 path 指令
    const dark = qr.modules.flat().filter(Boolean).length
    expect((svg.match(/h1v1h-1z/g) || []).length).toBe(dark)
  })
})
