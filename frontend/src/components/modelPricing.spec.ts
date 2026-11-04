import { describe, expect, it } from 'vitest'
import {
  amountError,
  catalogModeLabel,
  emptyForm,
  fillFormFromCatalog,
  formError,
  formFrom,
  missingTierWarning,
  payloadFrom,
  priceState,
  priceSummary,
  tierPriced,
  trimCatalogNumber
} from './modelPricing'

describe('formFrom 回填', () => {
  it('后端把未填存成 0：0 必须回填空串，不能显示成「配过 0 元」', () => {
    const f = formFrom({
      model_name: 'gpt-5',
      usd_input_per_1m: '1.25',
      usd_output_per_1m: '0',
      rmb_input_per_1m: '0.00000000',
      rmb_output_per_1m: '8'
    } as any)
    expect(f.usd_input_per_1m).toBe('1.25')
    expect(f.usd_output_per_1m).toBe('')
    expect(f.rmb_input_per_1m).toBe('')
    expect(f.rmb_output_per_1m).toBe('8')
  })

  it('null / undefined 得到空表单', () => {
    expect(formFrom(null)).toEqual(emptyForm())
    expect(formFrom(undefined)).toEqual(emptyForm())
  })
})

describe('amountError 金额校验', () => {
  it('空值放行（未填是合法状态）', () => {
    expect(amountError('')).toBe('')
    expect(amountError('   ')).toBe('')
  })
  it('普通十进制放行', () => {
    expect(amountError('0')).toBe('')
    expect(amountError('1.25')).toBe('')
    expect(amountError('0.00000001')).toBe('')
    expect(amountError('1000000')).toBe('')
  })
  it('负数、科学计数法、非数字都拒绝', () => {
    // 科学计数法单独拦：Number('1e5') 是合法的 100000，
    // 而「每百万词元 1e5 元」几乎一定是手滑
    expect(amountError('-1')).toContain('非负')
    expect(amountError('1e5')).toContain('十进制')
    expect(amountError('abc')).toContain('十进制')
    expect(amountError('1.2.3')).toContain('十进制')
    expect(amountError('$1.25')).toContain('十进制')
  })
})

describe('formError 整表校验', () => {
  it('全空通过（允许只建一条占位记录）', () => {
    expect(formError(emptyForm())).toBe('')
  })
  it('指出是哪个币种的哪一档', () => {
    const f = emptyForm()
    f.rmb_output_per_1m = 'abc'
    const err = formError(f)
    expect(err).toContain('人民币')
    expect(err).toContain('输出')
  })
})

describe('填 0 = 未填（与后端 PriceSet.IsZero 对齐）', () => {
  // 后端把「该币种四档全 0」判定为没配价，引擎据此不写价、费用记 0。
  // 若前端把 "0" 当成「已定价」，界面显示「仅美元 $0」而引擎认为没配 ——
  // 同一模型在界面上和账面上是两个状态。钱不会算错（都是 0），
  // 但这正是本仓库最忌讳的「界面说了 A、行为是 B」。
  it('手填 0 不算已定价', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '0'
    expect(tierPriced(f, 'usd')).toBe(false)
    expect(priceState(f)).toBe('none')
    expect(priceSummary(f)).toBe('未定价')
  })

  it('0.0 / 00.000 / 0.00000000 同样算未填', () => {
    for (const zero of ['0', '0.0', '00.000', '0.00000000']) {
      const f = emptyForm()
      f.rmb_output_per_1m = zero
      expect(tierPriced(f, 'rmb'), `${zero} 应算未填`).toBe(false)
    }
  })

  it('非零值仍算已定价（含极小值）', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '0.00000001'
    expect(tierPriced(f, 'usd')).toBe(true)
  })

  it('一档里只要有一个非零就算这一档已定价', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '0'
    f.usd_output_per_1m = '2'
    expect(tierPriced(f, 'usd')).toBe(true)
    expect(priceState(f)).toBe('usd-only')
  })
})

describe('tierPriced 与 priceState', () => {
  it('两档都填 = both', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '1'
    f.rmb_input_per_1m = '7'
    expect(tierPriced(f, 'usd')).toBe(true)
    expect(tierPriced(f, 'rmb')).toBe(true)
    expect(priceState(f)).toBe('both')
  })
  it('只填美元 = usd-only', () => {
    const f = emptyForm()
    f.usd_output_per_1m = '2'
    expect(priceState(f)).toBe('usd-only')
  })
  it('只填人民币 = rmb-only', () => {
    const f = emptyForm()
    f.rmb_output_per_1m = '14'
    expect(priceState(f)).toBe('rmb-only')
  })
  it('什么都没填 = none', () => {
    expect(priceState(emptyForm())).toBe('none')
  })
})

describe('priceSummary 摘要', () => {
  it('双币种并列显示', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '1'
    f.usd_output_per_1m = '10'
    f.rmb_input_per_1m = '7'
    f.rmb_output_per_1m = '70'
    expect(priceSummary(f)).toBe('$1 / $10 · ¥7 / ¥70')
  })
  it('缺的那一档显示 0（一行内保持两列对齐）', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '1'
    expect(priceSummary(f)).toBe('$1 / $0')
  })
  it('全空时明确说未定价', () => {
    expect(priceSummary(emptyForm())).toBe('未定价')
  })
})

describe('missingTierWarning 漏配提示', () => {
  it('人民币渠道在用但只配了美元价 → 点名会记 0 元', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '1'
    const w = missingTierWarning(f, ['CNY', 'USD'])
    expect(w).toContain('人民币')
    expect(w).toContain('0 元')
  })

  it('只在美元渠道用时不提示人民币缺失', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '1'
    expect(missingTierWarning(f, ['USD'])).toBe('')
  })

  it('两档都配了就不提示', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '1'
    f.rmb_input_per_1m = '7'
    expect(missingTierWarning(f, ['USD', 'CNY'])).toBe('')
  })

  it('没有渠道在用时不提示（配不配都不影响账单）', () => {
    expect(missingTierWarning(emptyForm(), [])).toBe('')
  })

  // 后端曾经把 used_currencies 返回成 map[string]bool（{"CNY":true}），
  // 而这里是按数组写的 —— 直接 .includes() 会抛 TypeError，整个弹窗崩掉。
  // 判据函数是最后一道边界，任何形状下都只能回答「缺哪一档」，不能抛。
  it('后端返回 map 形状时不抛错，且判定结果正确', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '1'
    // {"CNY":true,"USD":true} —— 有人民币渠道在用但没填人民币价
    const asMap = { CNY: true, USD: true } as any
    expect(() => missingTierWarning(f, asMap)).not.toThrow()
    expect(missingTierWarning(f, asMap)).toContain('人民币')
  })

  it('map 里值为 false 的币种不算「在用」', () => {
    const f = emptyForm()
    f.usd_input_per_1m = '1'
    const asMap = { CNY: false, USD: true } as any
    expect(missingTierWarning(f, asMap)).toBe('')
  })

  it('null / undefined 按「没有渠道在用」处理，不抛错', () => {
    expect(missingTierWarning(emptyForm(), null as any)).toBe('')
    expect(missingTierWarning(emptyForm(), undefined as any)).toBe('')
  })
})

describe('payloadFrom 提交载荷', () => {
  it('去掉首尾空白，空串原样传（后端按 0 处理）', () => {
    const f = emptyForm()
    f.usd_input_per_1m = ' 1.25 '
    const p = payloadFrom('gpt-5', f)
    expect(p.model_name).toBe('gpt-5')
    expect(p.usd_input_per_1m).toBe('1.25')
    expect(p.rmb_output_per_1m).toBe('')
  })
})

describe('参考价库回填 fillFormFromCatalog', () => {
  it('折算目录条目为美元档字符串', () => {
    const fill = fillFormFromCatalog({
      mode: 'chat',
      provider: 'openai',
      in: 2.5,
      out: 10,
      cr: 1.25,
      cw: 3.75
    })
    expect(fill.usd_input_per_1m).toBe('2.5')
    expect(fill.usd_output_per_1m).toBe('10')
    expect(fill.usd_cache_read_per_1m).toBe('1.25')
    expect(fill.usd_cache_write_per_1m).toBe('3.75')
  })

  it('非 token 维度照单全收（每单位口径，不折算）', () => {
    const fill = fillFormFromCatalog({
      mode: 'image_generation',
      provider: 'openai',
      img: 0.04,
      sec: 0.0001,
      chr: 0.000015
    })
    expect(fill.usd_per_image).toBe('0.04')
    expect(fill.usd_per_second).toBe('0.0001')
    expect(fill.usd_per_character).toBe('0.000015')
  })

  it('0 / 缺失的档不产出：已填过的档不被一次填充抹掉', () => {
    const fill = fillFormFromCatalog({ mode: 'chat', provider: 'openai', in: 2.5 })
    expect('usd_output_per_1m' in fill).toBe(false)
    expect('usd_per_image' in fill).toBe(false)
  })

  it('人民币档永不产出：参考价是美元口径，换算需要汇率', () => {
    const fill = fillFormFromCatalog({
      mode: 'chat',
      provider: 'openai',
      in: 2.5,
      out: 10
    }) as Record<string, unknown>
    for (const k of Object.keys(fill)) {
      expect(k.startsWith('rmb_')).toBe(false)
    }
  })
})

describe('trimCatalogNumber 金额字符串', () => {
  it('去尾零、去孤立小数点', () => {
    expect(trimCatalogNumber(2.5)).toBe('2.5')
    expect(trimCatalogNumber(10)).toBe('10')
    expect(trimCatalogNumber(0.000015)).toBe('0.000015')
  })
  it('小金额不得出科学计数法（金额输入框拒绝 e 记数）', () => {
    expect(trimCatalogNumber(1.5e-5)).not.toContain('e')
    expect(trimCatalogNumber(1.5e-5)).toBe('0.000015')
  })
  it('非正数 / 非有限值给空串', () => {
    expect(trimCatalogNumber(0)).toBe('')
    expect(trimCatalogNumber(-1)).toBe('')
    expect(trimCatalogNumber(NaN)).toBe('')
    expect(trimCatalogNumber(Infinity)).toBe('')
  })
})

describe('catalogModeLabel', () => {
  it('常见 mode 有中文文案，未知 mode 原样透出', () => {
    expect(catalogModeLabel('chat')).toBe('对话')
    expect(catalogModeLabel('image_generation')).toBe('文生图')
    expect(catalogModeLabel('some_new_mode')).toBe('some_new_mode')
    expect(catalogModeLabel('')).toBe('（未标注）')
  })
})
