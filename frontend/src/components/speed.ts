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
// 分母怎么取，按「有没有交付观测」分两条路：
//
//   1. 有观测（2026-09-30 起后端在流式读循环里记 body_reads / last_byte_ms）：
//      正文分多次到达（body_reads ≥ 2）时，last_byte_ms − first_byte_ms 就是正文
//      真正用了多久送达 —— 两端都以本次上游尝试为基准，不含排队与重试退避，
//      比「总耗时 − 首字」更接近生成时段。只读到一次（body_reads = 1）说明上游
//      把整段正文一次发出，窗口里只有尾包传输，直接判为量不到。
//      这段窗口可不可信，看**算出来的速度是否物理上可能**（上限 1000 tok/s）：
//      分块数已经证明正文是分多次交付的，此时再用固定毫秒门槛只会误伤
//      「短而快」的回答（2026-09-30 实测：6 行跨 143~199ms、分块数 10~40 的
//      逐帧返回被 200ms 门槛砍掉，它们算出来 296~510 tok/s，都正常）。
//   2. 没有观测（加列之前的历史行）：退回「总耗时 − 首字」。这个口径偏大
//      （含排队与首字等待），也分辨不出「整段返回」，所以额外要过**绝对下限 200ms**。
//
// 为什么历史行的下限是绝对毫秒（200ms）而不是原来的「总耗时 5%」：比例判据会把
// 「首字等待占九成、但确实逐帧返回」的请求判成塌缩 —— 站主 2026-09-30 截图里
// 那四行就是（窗口 530~790ms，占各自总耗时 3.3%~6.0%，同一列两种分母）。
// 绝对下限在 2026-09-29 的数据上也验证过：「窗口 < 200ms」与「窗口 < 总耗时 5%」
// 两种规则给出的 p99 / 最大值完全相同（370 / 1069 tok/s），而前者多认下 102 行
// （窗口 200ms~5% 那些），它们算出来的速度都落在正常区间里。
// 反向也成立：上游整段返回时，几 KB 的正文传输只要几十毫秒（那批塌缩行的窗口
// 中位数 16ms、最小 4ms），不可能撑到 200ms。
import type { RequestLog } from '@/api/types'

/** 历史行（没有交付观测）可用的窗口下限（毫秒）：比这更短的窗口量到的是尾包传输。 */
const MIN_WINDOW_MS = 200

/**
 * 有交付观测时的物理上限（tok/s）：没有上游能在 1ms 里生成一个词元。
 *
 * 2026-09-30 实测（生产库 24 小时、561 条有观测的流式行）：p50 276、p90 369、
 * p99 480；超过 1000 的只有 3 条 —— 两条是上游把正文压到最后一起发的（23/36 个
 * 词元、跨 18/29ms），一条是词元数 58761 而窗口只有 12.7s 的用量异常行。
 * 三种都是「按窗口与词元数算出来物理上不可能」，宁可不给数。
 *
 * 上限取 1000 而不是更紧的 500：给未知的快模型留余量，且它只用来挡「不可能」，
 * 不用来判断「快得不寻常」（那是站主自己看数据的活）。
 */
const MAX_PLAUSIBLE_TOK_PER_SEC = 1000

export interface SpeedReading {
  /** 词元/秒。未取整 —— 显示层一律取整（站主 2026-09-21 要求） */
  tokPerSec: number
  /** 实际用作分母的毫秒数：悬停说明直接摆它，口径才不会只活在注释里 */
  denomMs: number
}

/** 速度算不出的原因，用来给「—」配一句解释（文案在组件里）。 */
export type SpeedMissReason =
  | 'noOutput'
  | 'noTime'
  | 'noStream'
  | 'oneShot'
  | 'burst'
  | 'implausible'

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
    // 有交付观测：分母用实测的送达跨度，可信度按「算出来的速度是否物理上可能」判，
    // 不再用固定毫秒门槛 —— 门槛会把「短而快」的回答一并砍掉（2026-09-30 实测：
    // 有 6 行跨 143~199ms、分块数 10~40 的行被 200ms 门槛误判，它们其实是逐帧返回的，
    // 算出来 296~510 tok/s，都落在正常区间）。
    if (reads < 2) return { reading: null, miss: 'oneShot' }
    const span = (row.last_byte_ms || 0) - first
    if (span > 0) {
      const tokPerSec = out / (span / 1000)
      if (tokPerSec <= MAX_PLAUSIBLE_TOK_PER_SEC) {
        return { reading: { tokPerSec, denomMs: span }, miss: null }
      }
      // 两种不可信：窗口只有几毫秒（上游把正文压到最后一起发）与词元数对不上窗口。
      // 判据一样，但理由文案分开写 —— 前者说窗口，后者说速度上限。
      return { reading: null, miss: span < MIN_WINDOW_MS ? 'burst' : 'implausible' }
    }
    return { reading: null, miss: 'burst' }
  }

  // 没有观测（历史行）：只能靠「总耗时 − 首字」这个近似，它含排队与首字等待、
  // 也可能整体塌缩，所以额外要过绝对下限（有观测的那条路不需要它）。
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
