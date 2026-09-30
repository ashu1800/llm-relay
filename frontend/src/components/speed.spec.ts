// 速度口径的回归（2026-09-29 立，2026-09-30 改成「一列一个口径」）。
//
// 抽 speed.ts 的直接原因就是「首字窗口塌缩」把速度算成几千 tok/s —— 所以用例
// 不造干净数字，一组组直接抄生产库真实行（2026-09-25~29，id 见 docs/superpowers/
// specs/2026-09-29-speed-metric-robustness-design.md 的表）。
//
// 2026-09-30 的改动：塌缩行不再退回总耗时，而是显示 —。原因与取舍见
// speed.ts 顶部注释与设计文档第十节 —— 这里把「哪种情形显示 —、哪种情形有数」
// 逐条钉住。
import { describe, expect, it } from 'vitest'
import { speedMissReason, speedOf, speedText } from './speed'
import type { RequestLog } from '@/api/types'

/** 只填本模块读的四个字段：其余字段与口径无关，缺了也不该影响判据 */
function makeLog(p: Partial<RequestLog>): RequestLog {
  return { stream: true, completion_tokens: 0, first_byte_ms: 0, total_ms: 0, ...p } as RequestLog
}

describe('首字窗口塌缩：整段正文压到最后一刻返回 → 量不到，显示 —', () => {
  // 这一组的共同形态：first_byte_ms ≈ total_ms（首字几乎在收尾时刻才到）。
  // 修前（2026-09-29 之前）用「总耗时 − 首字」当分母，分母只剩几毫秒，
  // 速度被算成 4~5 万；2026-09-29 那版退回总耗时（显示 46~81 tok/s）；
  // 现在按站主 2026-09-30 定的口径显示 —，不给一个含首字等待的吞吐。
  it.each([
    ['id 1427 智谱Pro/glm-5.3-flash', 366, 7395, 7388],
    ['id 1715 智谱Pro/glm-5.3-flash', 677, 12743, 12730],
    ['id 1428 智谱Pro/glm-5.3-flash', 303, 6627, 6621],
    ['id 1564 智谱Pro/glm-5.3-flash', 601, 7397, 7385],
    ['id 1571 智谱Pro/glm-5.3-flash', 864, 18084, 18005]
  ])('%s：不显示数字，悬停给「上游整段返回」', (_name, out, total, first) => {
    const row = makeLog({ completion_tokens: out, total_ms: total, first_byte_ms: first })
    expect(speedOf(row)).toBeNull()
    expect(speedText(row)).toBe('')
    expect(speedMissReason(row)).toBe('burst')
  })

  it('首字与收尾记在同一毫秒（窗口 = 0）同样是「量不到」', () => {
    const row = makeLog({ completion_tokens: 366, total_ms: 7395, first_byte_ms: 7395 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('burst')
  })

  it('窗口为负（重试链路里首字晚于落库的总耗时）也按量不到处理', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 5000, first_byte_ms: 6000 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('burst')
  })

  it('站主看到的那类行：若按「总耗时 − 首字」算就是 52286 tok/s（假值的来源）', () => {
    const row = makeLog({ completion_tokens: 366, total_ms: 7395, first_byte_ms: 7388 })
    expect(366 / ((7395 - 7388) / 1000)).toBeCloseTo(52286, 0)
  })
})

describe('逐帧返回的流式请求：唯一的分母是首字之后的生成时段', () => {
  it('首字 1.00s、总共 5.00s、输出 100 词元 → 25 tok/s（4.00s 生成时段）', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 5000, first_byte_ms: 1000 })
    const r = speedOf(row)!
    expect(r.denomMs).toBe(4000)
    expect(speedText(row)).toBe('25')
    expect(speedMissReason(row)).toBeNull()
  })

  it('后端没量到首字（first_byte_ms = 0）时，窗口就是总耗时，仍算得出', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 5000, first_byte_ms: 0 })
    expect(speedOf(row)!.denomMs).toBe(5000)
    expect(speedText(row)).toBe('20')
  })

  it('窗口恰好等于总耗时的 1/20：算得出（边界留在逐帧一侧）', () => {
    const row = makeLog({ completion_tokens: 20, total_ms: 2000, first_byte_ms: 1900 })
    expect(speedOf(row)!.denomMs).toBe(100)
    expect(speedText(row)).toBe('200')
  })

  it('窗口比 1/20 少 1ms 就判为塌缩（毫秒精度，边界不含糊）', () => {
    const row = makeLog({ completion_tokens: 20, total_ms: 2000, first_byte_ms: 1901 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('burst')
  })

  it('站主 2026-09-30 反馈的那四行：同渠道同模型，只有窗口够宽的那行有数', () => {
    // WorkBuddy海外 / deepseek-v4.1-flash，抄自站主截图（时间取整到 10ms）
    const rows = [
      { out: 142, total: 14180, first: 13520 }, // 窗口 4.7% → 量不到
      { out: 144, total: 8900, first: 8370 }, // 窗口 6.0% → 有数
      { out: 84, total: 23700, first: 22910 }, // 窗口 3.3% → 量不到
      { out: 93, total: 14250, first: 13620 } // 窗口 4.4% → 量不到
    ]
    expect(rows.map((r) => speedText(makeLog({ completion_tokens: r.out, total_ms: r.total, first_byte_ms: r.first })))).toEqual([
      '',
      '272',
      '',
      ''
    ])
  })
})

describe('非流式与算不出的情形', () => {
  it('非流式：生成时段量不到，显示 —（原因与非流式对应）', () => {
    const row = makeLog({ stream: false, completion_tokens: 366, total_ms: 7395, first_byte_ms: 7395 })
    expect(speedOf(row)).toBeNull()
    expect(speedText(row)).toBe('')
    expect(speedMissReason(row)).toBe('noStream')
  })

  it('非流式的窗口再宽也不算 —— 那个窗口里是正文传输，不是生成', () => {
    const row = makeLog({ stream: false, completion_tokens: 500, total_ms: 5000, first_byte_ms: 500 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('noStream')
  })

  it('没有输出词元 / 没量到总耗时：显示 —，原因各自对应', () => {
    const noOut = makeLog({ completion_tokens: 0, total_ms: 7395, first_byte_ms: 10 })
    expect(speedOf(noOut)).toBeNull()
    expect(speedMissReason(noOut)).toBe('noOutput')

    const noTime = makeLog({ completion_tokens: 366, total_ms: 0, first_byte_ms: 10 })
    expect(speedOf(noTime)).toBeNull()
    expect(speedText(noTime)).toBe('')
    expect(speedMissReason(noTime)).toBe('noTime')
  })

  it('已知边界：部分缓冲（窗口占比 64%）判不出来，仍会给出偏高的值', () => {
    // id 1219（GOAT/deepseek-v4.1-flash，7344 词元 / 10.715s，首字 3.845s）：
    // 上游大概是分几次大块 flush 的，前端只有首字与总耗时两个数，分不出
    // 「分块缓冲」与「真的这么快」。要根治得让后端在流式读循环里记尾包时刻
    // 或分块数并落库（见设计文档第八节第 1 条）。
    const row = makeLog({ completion_tokens: 7344, total_ms: 10715, first_byte_ms: 3845 })
    expect(speedText(row)).toBe('1069')
    expect(speedMissReason(row)).toBeNull()
  })
})
