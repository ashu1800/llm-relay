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
// 三种「量不到」，判据依次是：
//   · 没有输出词元（失败请求、或上游没给 usage）→ 分子都没有，谈不上速度；
//   · 没量到总耗时（total_ms = 0，落库异常）→ 分母也没有；
//   · 非流式请求 —— 响应头与整段正文一起到达，first_byte_ms 记的是响应头时刻，
//     那时生成已经完成，窗口里只有正文传输；
//   · 流式但首字窗口塌缩 —— 上游把整段正文压到最后一刻才发（响应头与首个正文
//     字节几乎同一时刻到达），窗口量到的是尾包传输而不是生成时段。
//
// 后两种都是「上游没有逐帧返回」，差别只在客户端要的是不是流式；中继控制不了
// 上游的交付方式，能做的是不给一个分母含等待时间的数 —— 那正是站主 2026-09-30
// 反馈「这列看着别扭」的原因（旧版对塌缩行显示的是「词元 ÷ 整次请求耗时」，
// 14 秒里有 13.5 秒在等首字，读数自然只有 10 tok/s）。
import type { RequestLog } from '@/api/types'

/**
 * 首字窗口塌缩的判据：首字之后剩下的时间不足总耗时的 1/20。
 *
 * 这不是「上游慢」，是「上游没有逐帧返回」：正文（连同响应头）在请求末尾整块
 * 到达时，`total_ms − first_byte_ms` 退化成几毫秒的尾包传输时间，而分子仍是
 * 整段输出的词元数 —— 366 词元 ÷ 7ms = 52286 tok/s 就是这么来的。
 *
 * 阈值取比例而不是某个绝对毫秒数，是因为这个形态的特征就是「窗口相对整次请求
 * 塌缩了」；生产数据上「窗口 < 总耗时 5%」（命中 323 行）与「窗口 < 200ms」
 * （命中 221 行）两种规则给出的 p99 / 最大值完全相同（370 / 1069 tok/s），
 * 结论不依赖阈值取在哪一刀。
 */
const MIN_WINDOW_SHARE = 1 / 20

export interface SpeedReading {
  /** 词元/秒。未取整 —— 显示层一律取整（站主 2026-09-21 要求） */
  tokPerSec: number
  /** 实际用作分母的毫秒数：悬停说明直接摆它，口径才不会只活在注释里 */
  denomMs: number
}

/** 速度算不出的原因，用来给「—」配一句解释（文案在组件里）。 */
export type SpeedMissReason = 'noOutput' | 'noTime' | 'noStream' | 'burst'

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
  if (!row.stream) return { reading: null, miss: 'noStream' }

  const win = total - (row.first_byte_ms || 0)
  // 窗口非正（首字与收尾记在同一毫秒；重试链路里首字甚至可能晚于落库的总耗时）
  // 也按塌缩处理：那时窗口里没有任何可用的生成时段
  if (win > 0 && win >= total * MIN_WINDOW_SHARE) {
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
