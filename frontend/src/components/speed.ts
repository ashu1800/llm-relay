// 输出速度的纯函数（2026-09-29 从 RequestLogPanel.vue 抽出）。
//
// 抽出来的动机：分母口径的判据埋在组件里只能靠肉眼对着列表看，而它恰恰出过
// 「几千 tok/s」的假值事故 —— 生产库 2026-09-29 的 2265 行流式日志里，323 行的
// 首字窗口塌缩、速度被算成几百到五万，全库 p99 从 370 涨到 12100、最大 52286。
// 根因与证据见 docs/superpowers/specs/2026-09-29-speed-metric-robustness-design.md。
//
// **速度只有一个含义（2026-09-30 站主定的口径）**：
//
//     速度 = 输出词元 ÷ 首字后生成时段
//
// 量不到生成时段就不显示数字（列表渲染 —），悬停说明讲清是哪种量不到 ——
// 「一列一个口径」比「换一个分母凑出一个数」重要：同一列里混着两种分母时
// （2026-09-29 那版是「窗口塌缩的行退回总耗时」），站主看到的是同一个渠道
// 同一分钟里 10 / 274 / 4 / 7 tok/s 这样一组没法互相比较的数。
//
// 分母怎么取，按「有没有交付观测」分两条路（两条路的阈值是同一个绝对下限）：
//
//   1. 有观测（2026-09-30 起后端在流式读循环里记 body_reads / last_byte_ms）：
//      正文分多次到达（body_reads ≥ 2）时，last_byte_ms − first_byte_ms 就是正文
//      真正用了多久送达 —— 两端都以本次上游尝试为基准，不含排队与重试退避，
//      比「总耗时 − 首字」更接近生成时段。只读到一次（body_reads = 1）说明上游
//      把整段正文一次发出，窗口里只有尾包传输，直接判为量不到。
//   2. 没有观测（加列之前的历史行）：退回「总耗时 − 首字」。这个口径偏大
//      （含排队与首字等待），但配上绝对下限之后不会给出假值。
//
// 为什么阈值是绝对毫秒（200ms）而不是原来的「总耗时 5%」：比例判据会把
// 「首字等待占九成、但确实逐帧返回」的请求判成塌缩 —— 站主 2026-09-30 截图里
// 那四行就是（窗口 530~790ms，占各自总耗时 3.3%~6.0%，同一列两种分母）。
// 绝对下限在 2026-09-29 的数据上也验证过：「窗口 < 200ms」与「窗口 < 总耗时 5%」
// 两种规则给出的 p99 / 最大值完全相同（370 / 1069 tok/s），而前者多认下 102 行
// （窗口 200ms~5% 那些），它们算出来的速度都落在正常区间里。
// 反向也成立：上游整段返回时，几 KB 的正文传输只要几十毫秒（那批塌缩行的窗口
// 中位数 16ms、最小 4ms），不可能撑到 200ms。
import type { RequestLog } from '@/api/types'

/** 可用的生成时段下限（毫秒）：比这更短的窗口量到的是尾包传输，不是生成时段。 */
const MIN_WINDOW_MS = 200

export interface SpeedReading {
  /** 词元/秒。未取整 —— 显示层一律取整（站主 2026-09-21 要求） */
  tokPerSec: number
  /** 实际用作分母的毫秒数：悬停说明直接摆它，口径才不会只活在注释里 */
  denomMs: number
}

/** 速度算不出的原因，用来给「—」配一句解释（文案在组件里）。 */
export type SpeedMissReason = 'noOutput' | 'noTime' | 'noStream' | 'oneShot' | 'burst'

interface SpeedEval {
  reading: SpeedReading | null
  miss: SpeedMissReason | null
}

/**
 * 判据只有这一处：`speedOf` 与 `speedMissReason` 都从这里取值，
 * 两者不会各自漂移（它们的调用方分别是数值与悬停说明，必须同进同退）。
 */
function evaluate(row: RequestLog): SpeedEval {
  const out = row.completion_tokens
  if (!out || out <= 0) return { reading: null, miss: 'noOutput' }
  const total = row.total_ms || 0
  if (total <= 0) return { reading: null, miss: 'noTime' }
  // 非流式：响应头与整段正文一起到达，first_byte_ms 记的是响应头时刻，
  // 那时生成已经完成，窗口里只有正文传输
  if (!row.stream) return { reading: null, miss: 'noStream' }

  const first = row.first_byte_ms || 0
  const reads = row.body_reads || 0
  if (reads > 0) {
    // 有交付观测：按事实判
    if (reads < 2) return { reading: null, miss: 'oneShot' }
    const span = (row.last_byte_ms || 0) - first
    if (span >= MIN_WINDOW_MS) {
      return { reading: { tokPerSec: out / (span / 1000), denomMs: span }, miss: null }
    }
    return { reading: null, miss: 'burst' }
  }

  // 没有观测（历史行）：退回「总耗时 − 首字」，同样要过绝对下限。
  // 窗口非正（首字与收尾记在同一毫秒）也落到这里判为量不到。
  const win = total - first
  if (win >= MIN_WINDOW_MS) {
    return { reading: { tokPerSec: out / (win / 1000), denomMs: win }, miss: null }
  }
  return { reading: null, miss: 'burst' }
}

/**
 * 一次请求的输出速度读数；null 表示量不到生成时段（显示层渲染 —）。
 * 不猜数：换一个分母（总耗时）凑出来的数分母里含着排队与首字等待，
 * 读数会比真实生成速度低一个数量级，比没有数更误导。
 */
export function speedOf(row: RequestLog): SpeedReading | null {
  return evaluate(row).reading
}

/**
 * 量不到时返回原因，量得到时返回 null。
 * 与 `speedOf` 共用同一处判据，不会出现「有数但没有理由」或反过来的组合。
 */
export function speedMissReason(row: RequestLog): SpeedMissReason | null {
  return evaluate(row).miss
}

/**
 * 显示值：算不出返回空串（列表显示 —），算得出则取整。
 * 取整的理由（站主 2026-09-21 要求）：速度只是个量级参考，小数位是假精度，
 * 取整后同列位数也天然一致（速度列还有「流」胶囊要对齐，见 ui-spec 第 18 条）。
 */
export function speedText(row: RequestLog): string {
  const r = speedOf(row)
  return r ? r.tokPerSec.toFixed(0) : ''
}
