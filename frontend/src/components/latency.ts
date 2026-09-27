// 耗时家族的纯函数（2026-09-27 从 RequestLogPanel.vue 抽出）：列表竖条、
// 详情抽屉的数值行、耗时瀑布三处共用同一套档位与格式化 —— 三份调用点
// 决定了这套规则必须只有一份定义。抽出后：
//   · 契约检查里「阈值钉死」的几条（10s/30s、20s/60s、toFixed(2)）锚定本文件；
//   · 单元测试（latency.spec.ts）在这里铺，档位边界不需要再靠肉眼对截图。
import type { RequestLog } from '@/api/types'

/* 耗时文本。秒**一律补齐两位小数**（8.70s / 14.00s，而不是 8.7s / 14.0s）：
   列表是竖着扫的，位数一致时小数点在同一列上，扫一列数字不用重新找基准。
   不足 1 秒仍按毫秒显示（170ms）—— 抖成「0.17s」不如毫秒直观，
   而且库里最小的耗时是 2ms，两位小数会把它抹成「0.00s」，
   等于把「很快」显示成「没有耗时」。 */
export function fmtMs(v: number) {
  if (!v) return '-'
  return v >= 1000 ? (v / 1000).toFixed(2) + 's' : v + 'ms'
}

// 耗时分级（2026-09-16 站主重定阈值，两行各用一套）：
//   首字：≤10s 绿、10-30s 橙、>30s 红
//   耗时：≤20s 绿、20-60s 橙、>60s 红
// 首字更严，因为它才是「用户感觉卡不卡」的那一下；总耗时把上游生成的时间也算进去，
// 长回答本来就要几十秒，用同一把尺子会把正常请求染红。
//
// 边界取 <=10000 / <=30000 这种「闭区间、下一档从 10001 起」的写法，
// 不在两档之间留缝：中间的毫秒必须落进某一档，否则会出现「不着色」的空档。
//
// fmtMs 对 0 与空值都返回 '-'，那种情况不着色，避免把「没有数据」显示成「很快」。
export function latencyClass(ms: number | null | undefined, kind: 'first' | 'total' = 'total') {
  if (!ms) return 'lat-none'
  const [fast, mid] = kind === 'first' ? [10000, 30000] : [20000, 60000]
  if (ms <= fast) return 'lat-fast'
  if (ms <= mid) return 'lat-mid'
  return 'lat-slow'
}

/** 悬停说明这一档的判据：颜色本身不该是唯一的信息来源 */
export function latencyTitle(ms: number | null | undefined, kind: 'first' | 'total' = 'total') {
  if (!ms) return '没有记录到耗时'
  if (kind === 'first') {
    if (ms <= 10000) return '10 秒内'
    if (ms <= 30000) return '10-30 秒'
    return '超过 30 秒'
  }
  if (ms <= 20000) return '20 秒内'
  if (ms <= 60000) return '20-60 秒'
  return '超过 60 秒'
}

/** 耗时瀑布的一行：label 进左轨、pct 决定条长、cls 沿用耗时分级配色、text 是数值 */
export type WaterfallRow = { label: string; pct: number; cls: string; text: string }

/**
 * 详情抽屉的耗时瀑布（2026-09-27）：三个阶段各一条水平条，长度按占总耗时的比例。
 * 一眼看出「这次到底慢在哪一段」—— 首字条占满而握手条极短，慢的就是模型生成；
 * 反过来握手条占一半，问题在网络/代理。条色沿用列表里的耗时分级（同色同义）。
 *
 * 没有记录的阶段（0 / null）不画：瀑布回答的是「时间花在哪」，缺数据的段
 * 画一根 0 长度的条只会撑出空白行。
 */
export function latencyWaterfall(row: RequestLog): WaterfallRow[] {
  const total = row.total_ms || 0
  const rows: WaterfallRow[] = []
  const push = (label: string, ms: number | null | undefined, kind: 'first' | 'total') => {
    if (!ms || ms <= 0) return
    rows.push({
      label,
      // 首字理论上 ≤ 总耗时，但上游字段没有这个保证（重试链路的口径差），
      // 超了就封顶 100%，一根满条比一根溢出宽度的条诚实
      pct: total > 0 ? Math.min(100, (ms / total) * 100) : 0,
      cls: latencyClass(ms, kind),
      text: fmtMs(ms)
    })
  }
  push('上游握手', row.upstream_ms, 'first')
  push('首字', row.first_byte_ms, 'first')
  push('总共', row.total_ms, 'total')
  return rows
}
