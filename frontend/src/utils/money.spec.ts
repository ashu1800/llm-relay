import { describe, expect, it } from 'vitest'
import { amountText, costText, currencyKeys, moneyText, primaryCurrency, splitCostParts } from './money'

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

describe('currencyKeys 固定顺序（只用于并列兜底与列表展示）', () => {
  it('CNY 在 USD 前，未知币种按字母排在后面', () => {
    expect(currencyKeys({ USD: '1', CNY: '2', EUR: '3' })).toEqual(['CNY', 'USD', 'EUR'])
  })

  it('空值返回空数组', () => {
    expect(currencyKeys(undefined)).toEqual([])
    expect(currencyKeys(null)).toEqual([])
    expect(currencyKeys({})).toEqual([])
  })
})

describe('primaryCurrency 按折算后的价值排大字（2026-11-05 站主要求）', () => {
  it('折算后美元更值钱时美元当大字（截图里那张卡：$2.84 折 ¥20.2 > ¥11.23）', () => {
    expect(primaryCurrency({ CNY: '11.23160000', USD: '2.83720000' }, 7.1)).toBe('USD')
  })

  it('折算后仍是人民币更大时人民币当大字', () => {
    expect(primaryCurrency({ CNY: '11.23160000', USD: '1.00000000' }, 7.1)).toBe('CNY')
  })

  it('不传汇率时退回比账面数字（旧后端没有这个字段，不凭空造汇率）', () => {
    expect(primaryCurrency({ CNY: '11.23160000', USD: '2.83720000' })).toBe('CNY')
    expect(primaryCurrency({ CNY: '0.00000000', USD: '0.03610000' })).toBe('USD')
  })

  it('汇率非法（0 / 负数 / NaN）同样退回账面比较，而不是把美元折成 0', () => {
    const costs = { CNY: '11.23160000', USD: '2.83720000' }
    for (const r of [0, -7.1, NaN]) {
      expect(primaryCurrency(costs, r)).toBe('CNY')
    }
  })

  it('按数字比较，不受字符串字典序影响（9 vs 10）', () => {
    expect(primaryCurrency({ CNY: '9', USD: '10' })).toBe('USD')
    expect(primaryCurrency({ CNY: '10', USD: '9' })).toBe('CNY')
  })

  it('只有一个币种时就是它', () => {
    expect(primaryCurrency({ USD: '0.00093800' }, 7.1)).toBe('USD')
    expect(primaryCurrency({ CNY: '0' }, 7.1)).toBe('CNY')
  })

  it('并列 / 都为 0 / 非法值 → 固定顺序兜底，刷新之间不跳位置', () => {
    expect(primaryCurrency({ USD: '1', CNY: '1' })).toBe('CNY')
    expect(primaryCurrency({ CNY: '0', USD: '0' }, 7.1)).toBe('CNY')
    expect(primaryCurrency({ USD: 'abc', CNY: 'xyz' }, 7.1)).toBe('CNY')
  })

  it('没有金额时返回空串（卡片显示 -- 而不是凭空造一个币种）', () => {
    expect(primaryCurrency(undefined, 7.1)).toBe('')
    expect(primaryCurrency({}, 7.1)).toBe('')
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

