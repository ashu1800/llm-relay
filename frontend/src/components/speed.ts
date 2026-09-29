// 输出速度的纯函数（2026-09-29 从 RequestLogPanel.vue 抽出）。
//
// 抽出来的动机：分母口径的判据埋在组件里只能靠肉眼对着列表看，而它恰恰出过
// 「几千 tok/s」的假值事故 —— 生产库 2026-09-29 的 2265 行流式日志里，323 行的
// 首字窗口塌缩、速度被算成几百到五万，全库 p99 从 370 涨到 12100、最大 52286。
// 根因与证据见 docs/superpowers/specs/2026-09-29-speed-metric-robustness-design.md。
//
// 速度 = 输出词元 ÷ 生成时长，而这个「生成时长」只有流式请求才量得到：
//   · 非流式 —— 响应头与整段正文一起到达（后端非流式分支的 first_byte_ms 记的
//     就是响应头到达时刻，那时生成已经完成），只能用总耗时近似，kind='total'；
//   · 流式且逐帧返回 —— 首字之后才是生成时段，用「总耗时 − 首字」，kind='firstByte'；
//   · 流式但首字窗口塌缩 —— 上游把整段正文压到最后一刻才发（响应头与首个正文
//     字节几乎同一时刻到达），这个窗口量到的是尾包传输而不是生成时长，
//     kind='burst'，退回总耗时近似。
//
// 三种口径对应三种悬停说明（组件里按 kind 选词）、「流」胶囊是「分母不是总耗时」
// 的可视标记 —— 口径不同的分叉必须让用户看得见，是这套规则的前提。
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

/** 分母口径：首字后生成时段 / 总耗时（非流式）/ 总耗时（流式但整段到达） */
export type SpeedKind = 'firstByte' | 'total' | 'burst'

export interface SpeedReading {
  /** 词元/秒。未取整 —— 显示层一律取整（站主 2026-09-21 要求） */
  tokPerSec: number
  /** 实际用作分母的毫秒数：悬停说明直接摆它，口径才不会只活在注释里 */
  denomMs: number
  kind: SpeedKind
}

/**
 * 一次请求的输出速度读数；null 表示算不出（没有输出词元、没量到耗时）。
 * null 在列表里显示「—」，不猜数 —— 估出来的速度比没有速度更误导。
 */
export function speedOf(row: RequestLog): SpeedReading | null {
  const out = row.completion_tokens
  if (!out || out <= 0) return null
  const total = row.total_ms || 0
  if (total <= 0) return null
  if (row.stream) {
    const win = total - (row.first_byte_ms || 0)
    // 窗口非正（首字与收尾记在同一毫秒；重试链路里首字甚至可能晚于落库的总耗时）
    // 也按塌缩处理：这时窗口里没有任何可用的生成时段，用总耗时反而更接近真相
    if (win > 0 && win >= total * MIN_WINDOW_SHARE) {
      return { tokPerSec: out / (win / 1000), denomMs: win, kind: 'firstByte' }
    }
    return { tokPerSec: out / (total / 1000), denomMs: total, kind: 'burst' }
  }
  return { tokPerSec: out / (total / 1000), denomMs: total, kind: 'total' }
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
