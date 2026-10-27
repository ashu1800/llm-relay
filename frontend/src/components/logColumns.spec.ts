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
  thinking: 88,
  channel: 88,
  tokens: 150,
  elapsed: 120,
  cost: 90,
  speed: 118,
  status: 64,
  key: 100,
  client: 112,
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

  // ---- 列显隐（2026-10-10）：keys 子集上的三条不变量 ----
  //
  // 关掉几列之后，被关掉的列不渲染也不该分余量；分摊必须在**可见子集**上
  // 重新成立（Σ份额 = 容器宽 − 可见列声明合计）。这三条如果坏掉，
  // 表现是「关掉两列后表格右边多出一条空白」或「可见列反而变窄」。
  describe('只对可见列分摊（keys 子集）', () => {
    const shown: ColKey[] = ['time', 'model', 'channel', 'status', 'action']
    const shownDeclared = shown.reduce((s, k) => s + baseW[k], 0)

    it('可见列之和 ≈ 容器可用宽（Σ份额 = leftover）', () => {
      const avail = 1400
      const out = allocateWidths(avail, baseW, shown)
      const sum = shown.reduce((s, k) => s + out[k], 0)
      expect(sum).toBeCloseTo(avail, 6)
    })

    it('隐藏列不拿份额，宽度保持声明宽', () => {
      const out = allocateWidths(1400, baseW, shown)
      for (const k of COL_KEYS) {
        if (!shown.includes(k)) expect(out[k]).toBe(baseW[k])
      }
    })

    it('只剩权重 0 的列时不做除法（无 NaN），原样返回声明宽', () => {
      // status / action 的权重都是 0：weightSum = 0，若不判零就是 0/0 = NaN，
      // 列宽会变成 NaN 并被写进 :width（表格直接崩掉）
      const only: ColKey[] = ['status', 'action']
      const out = allocateWidths(2000, baseW, only)
      expect(out.status).toBe(baseW.status)
      expect(out.action).toBe(baseW.action)
      expect(Number.isNaN(out.status)).toBe(false)
    })

    it('可见子集比容器宽时同样不摊（横向滚动区）', () => {
      const out = allocateWidths(shownDeclared - 50, baseW, shown)
      for (const k of shown) expect(out[k]).toBe(baseW[k])
    })
  })
})

describe('权重表与列键的一致性', () => {
  it('每一列都有权重且非负（0 表示固定列不参与分摊）', () => {
    for (const k of COL_KEYS) {
      expect(DISTRIBUTE_WEIGHT[k]).toBeGreaterThanOrEqual(0)
    }
    expect(Object.keys(DISTRIBUTE_WEIGHT).sort()).toEqual([...COL_KEYS].sort())
  })

  it('Σw = 12（三个 ×2 长文本列 + 五个 ×1 数值列 + 四个 ×0 固定列）', () => {
    // 这条断言立的第一天就抓出注释里的算术错误（原注释写 13）：
    // 合计变了的唯一合法途径是有人动了权重表 —— 那必须是有意的。
    // 2026-10-14 客户端列加入：它不截断（显示名可枚举、长度有上界），
    // 与数值列同档 ×1，合计从 11 变 12。
    // 2026-10-27 思考强度列加入：内容是封闭的档位词集合，宽度与容器无关，
    // 取 ×0（与 status/action 同类）；合计因此仍是 12（+1 个 ×0 列，
    // 同时 ×1 列数没变）。这条断言没动，正好证明那次的权重选择是「不摊余量」。
    expect(WEIGHT_SUM).toBe(12)
  })

  it('思考强度列权重为 0（内容是定长档位词，加宽只产生空白）', () => {
    expect(DISTRIBUTE_WEIGHT.thinking).toBe(0)
  })

  it('客户端列权重为 1（它不会截断，不与长文本列同档）', () => {
    // 单独钉一条：把 client 改成 2 会让余量往这一列多流，
    // 而它的内容（"Claude Code" 这类短名）根本用不上那些宽度
    expect(DISTRIBUTE_WEIGHT.client).toBe(1)
  })
})

describe('列序契约', () => {
  it('客户端列在密钥之后、操作之前（模板 / CSV / 列设置三处共用这一顺序）', () => {
    // 列序是契约：模板里的渲染顺序、后端 CSV 的表头顺序、列设置面板的
    // 勾选顺序都跟着 COL_KEYS 走。插错位置不会报错，只会让三处对不上
    // —— 用户看到「设置里的顺序与表头不一致」这种没人能解释的现象。
    expect(COL_KEYS.indexOf('client')).toBe(COL_KEYS.indexOf('key') + 1)
    expect(COL_KEYS.indexOf('action')).toBe(COL_KEYS.indexOf('client') + 1)
  })

  it('思考强度列在模型与渠道之间（2026-10-27 需求原话的位置）', () => {
    // 「单独加一列在模型和渠道列之间」：位置是需求的一部分 ——
    // 挨着模型放，扫列表时「哪个模型 + 什么思考强度」是一次眼睛移动。
    expect(COL_KEYS.indexOf('thinking')).toBe(COL_KEYS.indexOf('model') + 1)
    expect(COL_KEYS.indexOf('channel')).toBe(COL_KEYS.indexOf('thinking') + 1)
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
