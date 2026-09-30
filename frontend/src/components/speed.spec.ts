// 速度口径的回归（2026-09-29 立，2026-09-30 两次收紧+放宽）。
//
// 抽 speed.ts 的直接原因就是「首字窗口塌缩」把速度算成几千 tok/s —— 所以用例
// 不造干净数字，一组组直接抄生产库真实行（2026-09-25~30，id 见 docs/superpowers/
// specs/2026-09-29-speed-metric-robustness-design.md 的表）。
//
// 2026-09-30 的三处改动，都在这里逐条钉住：
//   1. 塌缩行不再退回总耗时，而是显示 —（一列一个口径）；
//   2. 判据从「窗口 < 总耗时 5%」改成绝对下限 200ms，并且优先用后端新落库的
//      交付观测（body_reads / last_byte_ms）算分母；
//   3. 有观测时不再用固定毫秒门槛，改成「算出来的速度是否物理上可能」（≤1000 tok/s）
//      —— 固定门槛把「短而快」的回答一并砍掉了（站主 2026-09-30 追问的那批）。
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

  it('窗口短但逐帧返回、算出来速度正常 → 算得出（2026-09-30 放宽的那一类）', () => {
    // 生产库 id 6785：97 词元 / 跨度 190ms / 40 个分块 → 510 tok/s。
    // 旧判据（跨度 ≥ 200ms）把这一类一并砍掉，等于把「短而快」的回答判成量不到。
    const row = makeLog({ completion_tokens: 97, total_ms: 1726, first_byte_ms: 1531, last_byte_ms: 1721, body_reads: 40 })
    expect(speedOf(row)!.denomMs).toBe(190)
    expect(speedText(row)).toBe('511')
    expect(speedMissReason(row)).toBeNull()
  })

  it('同一天里被 200ms 门槛误伤的那批（143~199ms、10~40 个分块）全部算得出', () => {
    // 抄自生产库 2026-09-30 15:11~15:40 的 6 行，按跨度算分别是 510/296/440/457/446/397
    const rows = [
      { out: 97, first: 1531, last: 1721, reads: 40, total: 1726 },
      { out: 58, first: 4063, last: 4259, reads: 10, total: 4309 },
      { out: 63, first: 7105, last: 7248, reads: 23, total: 7409 },
      { out: 74, first: 8512, last: 8674, reads: 21, total: 8806 },
      { out: 87, first: 10828, last: 11023, reads: 24, total: 11135 },
      { out: 79, first: 12040, last: 12239, reads: 15, total: 12538 }
    ]
    const texts = rows.map((r) =>
      speedText(makeLog({ completion_tokens: r.out, total_ms: r.total, first_byte_ms: r.first, last_byte_ms: r.last, body_reads: r.reads }))
    )
    expect(texts).toEqual(['511', '296', '441', '457', '446', '397'])
  })

  it('上游把整段正文一次发出（只读到 1 个分块）：量不到，显示 —', () => {
    // 这正是站主 2026-09-30 截图里那类行的形态：首字 ≈ 总耗时
    const row = makeLog({ completion_tokens: 142, total_ms: 14180, first_byte_ms: 13520, last_byte_ms: 13520, body_reads: 1 })
    expect(speedOf(row)).toBeNull()
    expect(speedText(row)).toBe('')
    expect(speedMissReason(row)).toBe('oneShot')
  })

  it('只读到 3 个分块、正文 18ms 内到齐（站主问的 id 6913）：量不到，理由是窗口', () => {
    // 23 词元 ÷ 18ms = 1278 tok/s —— 物理上不可能；上游把响应头与正文一起压在最后发
    const row = makeLog({ completion_tokens: 23, total_ms: 4095, first_byte_ms: 4062, last_byte_ms: 4080, body_reads: 3 })
    expect(speedOf(row)).toBeNull()
    expect(speedText(row)).toBe('')
    expect(speedMissReason(row)).toBe('burst')
  })

  it('窗口够长但算出来超过物理上限（词元数与窗口对不上）：量不到，理由是上限', () => {
    // 生产库 id 6683：58761 词元 / 跨度 12.738s → 4613 tok/s。分块数 1541 说明窗口是真的，
    // 对不上的是词元数 —— 两种都不可信，但理由文案要说在点上（说上限，不说窗口）。
    const row = makeLog({ completion_tokens: 58761, total_ms: 13000, first_byte_ms: 200, last_byte_ms: 12938, body_reads: 1541 })
    expect(speedOf(row)).toBeNull()
    expect(speedMissReason(row)).toBe('implausible')
  })

  it('物理上限对「短而快」留了余量：1ms/词元（1000 tok/s）仍算得出', () => {
    const atLimit = makeLog({ completion_tokens: 100, total_ms: 9000, first_byte_ms: 8100, last_byte_ms: 8200, body_reads: 12 })
    expect(speedOf(atLimit)!.denomMs).toBe(100)
    expect(speedText(atLimit)).toBe('1000')

    const over = makeLog({ completion_tokens: 101, total_ms: 9000, first_byte_ms: 8100, last_byte_ms: 8200, body_reads: 12 })
    expect(speedOf(over)).toBeNull()
    expect(speedMissReason(over)).toBe('burst') // 窗口只有 100ms，理由说窗口
  })

  it('首字与末块落在同一毫秒（跨度 = 0）：量不到', () => {
    const row = makeLog({ completion_tokens: 23, total_ms: 7364, first_byte_ms: 7253, last_byte_ms: 7253, body_reads: 2 })
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

describe('没有交付观测的历史行：退回「总耗时 − 首字」+ 绝对下限 200ms', () => {  it('站主 2026-09-30 截图那四行：窗口都够宽，四行都算得出（口径一致）', () => {
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
    //
    // 注意这一行是**没有观测的历史行**，所以走的是「总耗时 − 首字 + 200ms 下限」，
    // 不带物理上限（1069 tok/s 才显示得出来）。有观测的行会过 1000 tok/s 上限 ——
    // 两条路的差别是有意的：上限要拿分块数当证据，历史行没有。
    const row = makeLog({ completion_tokens: 7344, total_ms: 10715, first_byte_ms: 3845 })
    expect(speedText(row)).toBe('1069')
    expect(speedMissReason(row)).toBeNull()
  })
})
