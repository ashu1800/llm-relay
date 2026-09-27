// 耗时档位的边界钉死（2026-09-27）：阈值是站主定的，契约检查只保证
// 「源码里写着那两个数」，这里保证「那两个数的行为正确」——
// 闭区间边界（≤fast 进 fast、fast+1 进 mid）与档位间的无缝衔接
// 是肉眼对不出来的，改阈值时这两组用例必须跟着改。
import { describe, expect, it } from 'vitest'
import { fmtMs, latencyClass, latencyTitle, latencyWaterfall } from './latency'
import type { RequestLog } from '@/api/types'

/** RequestLog 字段很多，用例只关心耗时三件套 —— 缺的字段对纯函数不可见 */
function makeLog(patch: Partial<RequestLog>): RequestLog {
  return { ...({} as RequestLog), ...patch }
}

describe('fmtMs', () => {
  it('不足 1 秒按毫秒显示（0 抹成 0.00s 等于把「很快」显示成「没有耗时」）', () => {
    expect(fmtMs(2)).toBe('2ms')
    expect(fmtMs(170)).toBe('170ms')
    expect(fmtMs(999)).toBe('999ms')
  })

  it('秒一律补齐两位小数（扫列时小数点在同一列上）', () => {
    expect(fmtMs(1000)).toBe('1.00s')
    expect(fmtMs(12500)).toBe('12.50s')
    expect(fmtMs(60000)).toBe('60.00s')
  })

  it('0（没有数据）显示占位符而不是 0ms', () => {
    expect(fmtMs(0)).toBe('-')
  })
})

describe('latencyClass 档位边界', () => {
  it('空值与 0 不着色（避免把「没有数据」显示成「很快」）', () => {
    expect(latencyClass(null)).toBe('lat-none')
    expect(latencyClass(0)).toBe('lat-none')
    expect(latencyClass(undefined, 'first')).toBe('lat-none')
  })

  it('首字档 ≤10s 绿 / 10-30s 橙 / >30s 红，边界闭区间无缝衔接', () => {
    expect(latencyClass(10000, 'first')).toBe('lat-fast')
    expect(latencyClass(10001, 'first')).toBe('lat-mid')
    expect(latencyClass(30000, 'first')).toBe('lat-mid')
    expect(latencyClass(30001, 'first')).toBe('lat-slow')
  })

  it('耗时档 ≤20s 绿 / 20-60s 橙 / >60s 红（不传 kind 走耗时档）', () => {
    expect(latencyClass(20000)).toBe('lat-fast')
    expect(latencyClass(20001)).toBe('lat-mid')
    expect(latencyClass(60000)).toBe('lat-mid')
    expect(latencyClass(60001)).toBe('lat-slow')
  })
})

describe('latencyTitle 档位文案', () => {
  it('两套档位各自的三段文案', () => {
    expect(latencyTitle(9999, 'first')).toBe('10 秒内')
    expect(latencyTitle(15000, 'first')).toBe('10-30 秒')
    expect(latencyTitle(31000, 'first')).toBe('超过 30 秒')
    expect(latencyTitle(19999, 'total')).toBe('20 秒内')
    expect(latencyTitle(30000, 'total')).toBe('20-60 秒')
    expect(latencyTitle(61000, 'total')).toBe('超过 60 秒')
  })
})

describe('latencyWaterfall 耗时瀑布', () => {
  it('三段齐全时按占总耗时比例给条长', () => {
    const rows = latencyWaterfall(makeLog({ upstream_ms: 100, first_byte_ms: 500, total_ms: 1000 }))
    expect(rows.map((r) => r.label)).toEqual(['上游握手', '首字', '总共'])
    expect(rows.map((r) => r.pct)).toEqual([10, 50, 100])
    // 条色沿用耗时分级：握手/首字按 first 档、总共按 total 档
    expect(rows[0].cls).toBe('lat-fast')
    expect(rows[2].cls).toBe('lat-fast')
    expect(rows.map((r) => r.text)).toEqual(['100ms', '500ms', '1.00s'])
  })

  it('没有记录的阶段不画（0 宽的条只会撑出空白行）', () => {
    const rows = latencyWaterfall(makeLog({ first_byte_ms: 400, total_ms: 800 }))
    expect(rows.map((r) => r.label)).toEqual(['首字', '总共'])
    const none = latencyWaterfall(makeLog({ total_ms: 300 }))
    expect(none.map((r) => r.label)).toEqual(['总共'])
  })

  it('分母缺失（total_ms = 0）时比例为 0 而不是 NaN', () => {
    const rows = latencyWaterfall(makeLog({ first_byte_ms: 500, total_ms: 0 }))
    expect(rows).toHaveLength(1)
    expect(rows[0].pct).toBe(0)
  })

  it('阶段耗时超过总耗时（上游字段无保证）时封顶 100%', () => {
    const rows = latencyWaterfall(makeLog({ first_byte_ms: 1500, total_ms: 1000 }))
    expect(rows[0].pct).toBe(100)
  })
})
