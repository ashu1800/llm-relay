// 速度口径的回归（2026-09-29 立，2026-09-30 改成「一列一个口径」+ 按观测判）。
//
// 抽 speed.ts 的直接原因就是「首字窗口塌缩」把速度算成几千 tok/s —— 所以用例
// 不造干净数字，一组组直接抄生产库真实行（2026-09-25~29，id 见 docs/superpowers/
// specs/2026-09-29-speed-metric-robustness-design.md 的表）。
//
// 2026-09-30 的两处改动，都在这里逐条钉住：
//   1. 塌缩行不再退回总耗时，而是显示 —（一列一个口径）；
//   2. 判据从「窗口 < 总耗时 5%」改成绝对下限 200ms，并且优先用后端新落库的
//      交付观测（body_reads / last_byte_ms）算分母。
import { describe, expect, it } from 'vitest'
import { speedMissReason, speedOf, speedText } from './speed'
import type { RequestLog } from '@/api/types'

/** 只填本模块读的字段：其余字段与口径无关，缺了也不该影响判据 */
function makeLog(p: Partial<RequestLog>): RequestLog {
  return {
    stream: true,
    completion_tokens: 0,
    first_byte_ms: 0,
    total_ms: 0,
    body_reads: 0,
    last_byte_ms: 0,
    ...p
  } as RequestLog
}

describe('有交付观测：按正文分块数与送达跨度判（2026-09-30 起落库）', () => {
  it('逐帧返回（47 个分块）：分母是实测的送达跨度，不是总耗时', () => {
    // 首块 120ms、末块 3120ms、总共 14000ms（首字等待 3 秒后还在等收尾）
    const row = makeLog({ completion_tokens: 600, total_ms: 14000, first_byte_ms: 120, last_byte_ms: 3120, body_reads: 47 })
    const r = speedOf(row)!
    expect(r.denomMs).toBe(3000)
    expect(speedText(row)).toBe('200')
    expect(speedMissReason(row)).toBeNull()
  })

  it('上游把整段正文一次发出（只读到 1 个分块）：量不到，显示 —', () => {
    // 这正是站主 2026-09-30 截图里那类行的形态：首字 ≈ 总耗时
    const row = makeLog({ completion_tokens: 142, total_ms: 14180, first_byte_ms: 13520, last_byte_ms: 13520, body_reads: 1 })
    expect(speedOf(row)).toBeNull()
    expect(speedText(row)).toBe('')
    expect(speedMissReason(row)).toBe('oneShot')
  })

  it('分块多次但跨度不足 200ms：量到的是尾包传输，显示 —', () => {
    const row = makeLog({ completion_tokens: 366, total_ms: 7395, first_byte_ms: 7388, last_byte_ms: 7392, body_reads: 3 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('burst')
  })

  it('跨度恰好 200ms：算得出（边界留在可用一侧）', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 9000, first_byte_ms: 8000, last_byte_ms: 8200, body_reads: 5 })
    expect(speedOf(row)!.denomMs).toBe(200)
    expect(speedText(row)).toBe('500')
  })

  it('跨度 199ms：算不出', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 9000, first_byte_ms: 8000, last_byte_ms: 8199, body_reads: 5 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('burst')
  })

  it('观测与总耗时不一致时以观测为准：分母不含末块之后的收尾开销', () => {
    // 同样 600 词元：按观测跨度 3s 算 200 tok/s；按「总耗时 − 首字」算只有 43
    const observed = makeLog({ completion_tokens: 600, total_ms: 14000, first_byte_ms: 120, last_byte_ms: 3120, body_reads: 47 })
    const legacy = makeLog({ completion_tokens: 600, total_ms: 14000, first_byte_ms: 120 })
    expect(speedText(observed)).toBe('200')
    expect(speedText(legacy)).toBe('43')
  })
})

describe('没有交付观测的历史行：退回「总耗时 − 首字」+ 绝对下限 200ms', () => {
  it('站主 2026-09-30 截图那四行：窗口都够宽，四行都算得出（口径一致）', () => {
    // WorkBuddy海外 / deepseek-v4.1-flash，抄自站主截图（时间取整到 10ms）。
    // 旧版判据（窗口 < 总耗时 5%）会把其中三行判成塌缩、退回总耗时，
    // 于是同一列出现 10 / 274 / 4 / 7 这样没法互相比较的一组数。
    const rows = [
      { out: 142, total: 14180, first: 13520 },
      { out: 144, total: 8900, first: 8370 },
      { out: 84, total: 23700, first: 22910 },
      { out: 93, total: 14250, first: 13620 }
    ]
    expect(rows.map((r) => speedText(makeLog({ completion_tokens: r.out, total_ms: r.total, first_byte_ms: r.first })))).toEqual([
      '215',
      '272',
      '106',
      '148'
    ])
  })

  it('首字窗口真的塌缩（几毫秒）：量不到，显示 —', () => {
    // id 1427 智谱Pro/glm-5.3-flash：366 词元 / 7395ms / 首字 7388ms → 窗口 7ms。
    // 修前用这个窗口做分母算出 52286 tok/s，2026-09-29 那版退回总耗时显示 49，
    // 现在按「量不到」显示 —。
    const row = makeLog({ completion_tokens: 366, total_ms: 7395, first_byte_ms: 7388 })
    expect(speedOf(row)).toBeNull()
    expect(speedText(row)).toBe('')
    expect(speedMissReason(row)).toBe('burst')
    expect(366 / ((7395 - 7388) / 1000)).toBeCloseTo(52286, 0)
  })

  it('首字与收尾记在同一毫秒（窗口 = 0）也是量不到', () => {
    const row = makeLog({ completion_tokens: 366, total_ms: 7395, first_byte_ms: 7395 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('burst')
  })

  it('窗口为负（重试链路里首字晚于落库的总耗时）也按量不到处理', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 5000, first_byte_ms: 6000 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('burst')
  })

  it('首字 1.00s、总共 5.00s、输出 100 词元 → 25 tok/s（4.00s 生成时段）', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 5000, first_byte_ms: 1000 })
    expect(speedOf(row)!.denomMs).toBe(4000)
    expect(speedText(row)).toBe('25')
  })

  it('后端没量到首字（first_byte_ms = 0）时，窗口就是总耗时，仍算得出', () => {
    const row = makeLog({ completion_tokens: 100, total_ms: 5000, first_byte_ms: 0 })
    expect(speedOf(row)!.denomMs).toBe(5000)
    expect(speedText(row)).toBe('20')
  })

  it('窗口恰好 200ms 算得出、199ms 算不出（绝对下限，与总耗时无关）', () => {
    const atFloor = makeLog({ completion_tokens: 20, total_ms: 20000, first_byte_ms: 19800 })
    expect(speedOf(atFloor)!.denomMs).toBe(200)
    expect(speedText(atFloor)).toBe('100')

    const below = makeLog({ completion_tokens: 20, total_ms: 20000, first_byte_ms: 19801 })
    expect(speedOf(below)).toBeNull()
    expect(speedMissReason(below)).toBe('burst')
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

  it('已知边界：上游分几次大块 flush（观测跨度仍然偏小）会给出偏高的值', () => {
    // id 1219（GOAT/deepseek-v4.1-flash，7344 词元 / 10.715s，首字 3.845s）：
    // 前端只有时间戳，分不出「分块缓冲」与「真的这么快」。有观测之后这类行的
    // 分母换成实测跨度（比「总耗时 − 首字」小），值只会更高 —— 要彻底解决得让
    // 上游探针看它到底怎么发的（设计文档第八节第 1 条）。
    const row = makeLog({ completion_tokens: 7344, total_ms: 10715, first_byte_ms: 3845 })
    expect(speedText(row)).toBe('1069')
    expect(speedMissReason(row)).toBeNull()
  })
})
