import { describe, expect, it } from 'vitest'
import { amountText, costText, moneyText, splitCostParts } from './money'

// 金额展示规则。这个文件此前没有测试，而它是全站唯一一份小数位规则 ——
// 看板、日志列表、日志详情、价格试算都走它。规则悄悄变了的表现是
// 「同一个数字在四个地方是四个样子」，用户核对账单时对不上。

describe('amountText 按量级分档（默认规则，不能随便改）', () => {
  it('≥ 0.01 用四位', () => {
    expect(amountText(12.3456789)).toBe('12.3457')
    expect(amountText('0.0375')).toBe('0.0375')
    expect(amountText(1)).toBe('1.0000')
  })

  it('< 0.01 用六位：这一档用四位就全变成 0.00xx 看不出来了', () => {
    // 库里最便宜的一单
    expect(amountText('0.000938')).toBe('0.000938')
    expect(amountText(0.0038)).toBe('0.003800')
  })

  it('0 显示 0.00（两个零），不是 0.0000 —— 四个零看起来像「算过但是零」', () => {
    expect(amountText(0)).toBe('0.00')
    expect(amountText('0')).toBe('0.00')
    expect(amountText(null)).toBe('0.00')
    expect(amountText(undefined)).toBe('0.00')
  })

  it('非法数字不抛错，按 0 处理', () => {
    expect(amountText('abc')).toBe('0.00')
    expect(amountText(NaN)).toBe('0.00')
    expect(amountText(Infinity)).toBe('0.00')
  })
})

describe('costText 空值语义', () => {
  it('没有金额时是 - 而不是 ¥0.00（0 在日志里代表「没计价」，不是「很便宜」）', () => {
    expect(costText(0, 'CNY')).toBe('-')
    expect(costText(null, 'USD')).toBe('-')
    expect(costText(-1, 'USD')).toBe('-')
    expect(costText('abc', 'USD')).toBe('-')
  })

  it('带币种符号', () => {
    expect(costText('12.3456', 'CNY')).toBe('¥12.3456')
    expect(costText('12.3456', 'USD')).toBe('$12.3456')
  })
})

describe('costText 指定小数位（请求日志费用列用四位）', () => {
  it('固定四位，不再按量级分档', () => {
    // 这一条是本文件存在的理由：列表里同一列必须是同一个量纲
    expect(costText('0.000938', 'USD', 4)).toBe('$0.0009')
    expect(costText('0.0375', 'CNY', 4)).toBe('¥0.0375')
    expect(costText('12.3456789', 'USD', 4)).toBe('$12.3457')
  })

  it('小于 0.00005 的一律显示 0.0000（已知代价，精确值去详情看）', () => {
    expect(costText('0.00001', 'USD', 4)).toBe('$0.0000')
  })

  it('0 仍然是 -，不受 decimals 影响', () => {
    expect(costText(0, 'USD', 4)).toBe('-')
    expect(costText(null, 'CNY', 4)).toBe('-')
  })

  it('别的位数也能用（目前没人用，但不该写死 4）', () => {
    expect(costText('1.23456', 'USD', 2)).toBe('$1.23')
    expect(costText('1.23456', 'USD', 6)).toBe('$1.234560')
  })
})

describe('moneyText', () => {
  it('看板合计：0 就是 0，写 ¥0.00 是对的', () => {
    expect(moneyText(0, 'CNY')).toBe('¥0.00')
    expect(moneyText('12.5', 'USD')).toBe('$12.5000')
  })
})

describe('splitCostParts 阶梯拆解', () => {
  it('拆解四位小数', () => {
    expect(splitCostParts('$12.3456')).toEqual({ head: '$12.34', tail: '56' })
    expect(splitCostParts('¥0.0038')).toEqual({ head: '¥0.00', tail: '38' })
  })

  it('不足两位小数或无小数时不拆分 tail', () => {
    expect(splitCostParts('$12.3')).toEqual({ head: '$12.3', tail: '' })
    expect(splitCostParts('¥10')).toEqual({ head: '¥10', tail: '' })
    expect(splitCostParts('-')).toEqual({ head: '-', tail: '' })
    expect(splitCostParts('')).toEqual({ head: '-', tail: '' })
  })
})

