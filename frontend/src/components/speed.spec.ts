// 速度分母口径的回归（2026-09-29 立）。抽 speed.ts 的直接原因就是
// 「首字窗口塌缩」把速度算成几千 tok/s —— 所以用例不造干净数字，
// 一组组直接抄生产库真实行（2026-09-25~29，id 见 docs/superpowers/specs/
// 2026-09-29-speed-metric-robustness-design.md 的表）。
import { describe, expect, it } from 'vitest'
import { speedOf, speedText } from './speed'
import type { RequestLog } from '@/api/types'

/** 只填本模块读的四个字段：其余字段与口径无关，缺了也不该影响判据 */
function makeLog(p: Partial<RequestLog>): RequestLog {
  return { stream: true, completion_tokens: 0, first_byte_ms: 0, total_ms: 0, ...p } as RequestLog
}

describe('首字窗口塌缩：整段正文压到最后一刻返回', () => {
  // 这一组的共同形态：first_byte_ms ≈ total_ms（首字几乎在收尾时刻才到），
  // 修前用「总耗时 − 首字」当分母，分母只剩几毫秒，速度被算成 4~5 万
  it.each([
    ['id 1427 智谱Pro/glm-5.3-flash', 366, 7395, 7388, '49'],
    ['id 1715 智谱Pro/glm-5.3-flash', 677, 12743, 12730, '53'],
    ['id 1428 智谱Pro/glm-5.3-flash', 303, 6627, 6621, '46'],
    ['id 1564 智谱Pro/glm-5.3-flash', 601, 7397, 7385, '81'],
    ['id 1571 智谱Pro/glm-5.3-flash', 864, 18084, 18005, '48']
  ])('%s：改用总耗时口径，显示 %s tok/s', (_name, out, total, first, want) => {
    const row = makeLog({ completion_tokens: out, total_ms: total, first_byte_ms: first })
    const r = speedOf(row)!
    expect(r.kind).toBe('burst')
    expect(r.denomMs).toBe(total)
    expect(speedText(row)).toBe(want)
  })

  it('首字与收尾记在同一毫秒（窗口 = 0）也走总耗时口径，不返回空串', () => {
    const row = makeLog({ completion_tokens: 366, total_ms: 7395, first_byte_ms: 7395 })
    expect(speedOf(row)!.kind).toBe('burst')
    expect(speedText(row)).toBe('49')
  })

  it('修前的口径确实是 5 万级：同一行按「总耗时 − 首字」算出来是 52286', () => {
    // 这个数不是凑的：它就是站主看到「离谱的几千 token/s」时的那类行
    const row = makeLog({ completion_tokens: 366, total_ms: 7395, first_byte_ms: 7388 })
    expect(366 / ((7395 - 7388) / 1000)).toBeCloseTo(52286, 0)
  })
})

describe('逐帧返回的流式请求：分母仍是首字之后的生成时段', () => {
  it('首字 1.00s、总共 5.00s、输出 100 词元 → 25 tok/s（4.00s 生成时段）', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 5000, first_byte_ms: 1000 })
    const r = speedOf(row)!
    expect(r.kind).toBe('firstByte')
    expect(r.denomMs).toBe(4000)
    expect(speedText(row)).toBe('25')
  })

  it('后端没量到首字（first_byte_ms = 0）时，窗口就是总耗时，仍走首字口径', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 5000, first_byte_ms: 0 })
    const r = speedOf(row)!
    expect(r.kind).toBe('firstByte')
    expect(r.denomMs).toBe(5000)
    expect(speedText(row)).toBe('20')
  })

  it('窗口恰好等于总耗时的 1/20：不收（边界留在逐帧一侧）', () => {
    const row = makeLog({ completion_tokens: 20, total_ms: 2000, first_byte_ms: 1900 })
    const r = speedOf(row)!
    expect(r.kind).toBe('firstByte')
    expect(r.denomMs).toBe(100)
    expect(speedText(row)).toBe('200')
  })

  it('窗口比 1/20 少 1ms 就判为塌缩（毫秒精度，边界不含糊）', () => {
    const row = makeLog({ completion_tokens: 20, total_ms: 2000, first_byte_ms: 1901 })
    const r = speedOf(row)!
    expect(r.kind).toBe('burst')
    expect(r.denomMs).toBe(2000)
    expect(speedText(row)).toBe('10')
  })
})

describe('非流式与算不出的情形', () => {
  it('非流式用总耗时（响应头与整段正文一起到达，生成时段量不到）', () => {
    const row = makeLog({ stream: false, completion_tokens: 366, total_ms: 7395, first_byte_ms: 7395 })
    const r = speedOf(row)!
    expect(r.kind).toBe('total')
    expect(r.denomMs).toBe(7395)
    expect(speedText(row)).toBe('49')
  })

  it('没有输出词元、或没量到总耗时：返回 null，显示层渲染 —（不猜数）', () => {
    expect(speedOf(makeLog({ completion_tokens: 0, total_ms: 7395, first_byte_ms: 10 }))).toBeNull()
    expect(speedOf(makeLog({ completion_tokens: 366, total_ms: 0, first_byte_ms: 10 }))).toBeNull()
    expect(speedText(makeLog({ completion_tokens: 366, total_ms: 0, first_byte_ms: 10 }))).toBe('')
  })

  it('已知边界：部分缓冲（窗口占比 64%）判不出来，仍可能给出偏高的值', () => {
    // id 1219（GOAT/deepseek-v4.1-flash，7344 词元 / 10.715s，首字 3.845s）：
    // 上游大概是分几次大块 flush 的，前端只有首字与总耗时两个数，分不出
    // 「分块缓冲」与「真的这么快」。修的是塌缩那一类，这一类留在已知边界里
    // （要根治得让后端记尾包时刻/分块数，见设计文档「未做的事」）。
    const row = makeLog({ completion_tokens: 7344, total_ms: 10715, first_byte_ms: 3845 })
    expect(speedOf(row)!.kind).toBe('firstByte')
    expect(speedText(row)).toBe('1069')
  })
})
