// 列宽分摊的三条不变量逐条钉住（2026-09-27 从 RequestLogPanel 抽出时立）：
//   1. Σ份额 = leftover ⇒ 合计 ≈ 容器宽（假滚动条不出现）
//   2. 只增不减 ⇒ 内容不被截断
//   3. 份额 ∝ 权重 ⇒ 余量去长文本列、固定窄列不动
// 这三条当初坏过两次（弹性列独吞 582px 的集中空洞、渠道列 44px 空白），
// 都是「看起来正常、直到站主截图」才被发现的那类回归。
import { describe, expect, it } from 'vitest'
import {
  COL_KEYS,
  DISTRIBUTE_WEIGHT,
  WEIGHT_SUM,
  allocateWidths,
  clampColumnWidth,
  type ColKey
} from './logColumns'

/** 面板初值同款的一组声明宽（数字本身不重要，重要的是合计与相对关系） */
const baseW: Record<ColKey, number> = {
  time: 96,
  model: 190,
  channel: 88,
  tokens: 150,
  elapsed: 120,
  cost: 90,
  speed: 118,
  status: 64,
  key: 100,
  action: 64
}
const declared = COL_KEYS.reduce((s, k) => s + baseW[k], 0)

describe('allocateWidths 余量分摊', () => {
  it('各列宽之和等于容器可用宽（Σ份额 = leftover，无假横向滚动）', () => {
    const avail = 1686 // 1920 视口扣侧栏与纵向滚动条的实测值
    const out = allocateWidths(avail, baseW)
    const sum = COL_KEYS.reduce((s, k) => s + out[k], 0)
    // 浮点容差：leftover/13 乘回 13 有 ulp 级误差，亚像素以下无意义
    expect(Math.abs(sum - avail)).toBeLessThan(0.001)
    expect(sum).toBeGreaterThan(declared)
  })

  it('只增不减：每列都不窄于自己的声明宽（内容不会被截断）', () => {
    const out = allocateWidths(declared + 300, baseW)
    for (const k of COL_KEYS) {
      expect(out[k]).toBeGreaterThanOrEqual(baseW[k])
    }
  })

  it('权重 0 的固定窄列（status/action）不拿余量，保持声明宽', () => {
    const out = allocateWidths(declared + 500, baseW)
    expect(out.status).toBe(64)
    expect(out.action).toBe(64)
  })

  it('份额与权重成比例：model（×2）拿的是 time（×1）的两份', () => {
    const out = allocateWidths(declared + 260, baseW)
    expect(out.model - baseW.model).toBeCloseTo((out.time - baseW.time) * 2, 9)
    expect(out.channel - baseW.channel).toBeCloseTo(out.key - baseW.key, 9)
  })

  it('容器比声明合计窄（横向滚动区）不摊，各列保持声明宽', () => {
    const out = allocateWidths(declared - 120, baseW)
    for (const k of COL_KEYS) {
      expect(out[k]).toBe(baseW[k])
    }
  })

  it('恰好等宽（leftover = 0）也是声明宽原样', () => {
    const out = allocateWidths(declared, baseW)
    expect(out).toEqual(baseW)
  })
})

describe('权重表与列键的一致性', () => {
  it('每一列都有权重且非负（0 表示固定列不参与分摊）', () => {
    for (const k of COL_KEYS) {
      expect(DISTRIBUTE_WEIGHT[k]).toBeGreaterThanOrEqual(0)
    }
    expect(Object.keys(DISTRIBUTE_WEIGHT).sort()).toEqual([...COL_KEYS].sort())
  })

  it('Σw = 11（三个 ×2 长文本列 + 五个 ×1 数值列 + 两个 ×0 固定列）', () => {
    // 这条断言立的第一天就抓出注释里的算术错误（原注释写 13）：
    // 合计变了的唯一合法途径是有人动了权重表 —— 那必须是有意的
    expect(WEIGHT_SUM).toBe(11)
  })
})

describe('clampColumnWidth 内容宽夹取', () => {
  it('中间值：量到的宽 + pad + 单元格内边距 16 + 呼吸 6', () => {
    expect(clampColumnWidth(100, 24, 88, 240)).toBe(146)
    expect(clampColumnWidth(100, 0, 90, 140)).toBe(122)
  })

  it('低于下限收到下限（表头与最小形态由下限兜底）', () => {
    expect(clampColumnWidth(10, 0, 88, 240)).toBe(88)
  })

  it('超过上限收到软上限（超出交给 ellipsis + 悬停 title）', () => {
    expect(clampColumnWidth(500, 0, 88, 240)).toBe(240)
  })
})
