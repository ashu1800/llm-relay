// 带宽读数的纯函数：格式化、曲线采样、状态判定（2026-10-23）。
//
// 抽成纯函数的理由与 speed.ts / latency.ts 一致：这里有三处容易写错的判断 ——
//
//   1. 「空闲」与「采集坏了」必须分开。空闲站点上整夜都是 0 B/s，
//      把读不到也显示成 0，采集坏了就永远发现不了；
//   2. 速率的单位选择会影响读数的可读性（1.2 KB/s vs 0.0012 MB/s）；
//   3. 曲线的纵轴上限要能自适应，否则一次备份同步会把整条线压成一条贴底的直线。

/** 服务端推来的单拍读数（字段与 Go 的 netstat.Stats 一一对应）。 */
export interface BandwidthPoint {
  at: string
  interface: string
  rx_bytes_per_sec: number
  tx_bytes_per_sec: number
  rx_total_bytes: number
  tx_total_bytes: number
  available: boolean
  error?: string
}

/** 一帧 bandwidth 推送（对应 Go 的 netstat.Frame）。history 只在建连时出现一次。 */
export interface BandwidthFrame {
  latest: BandwidthPoint
  history?: BandwidthPoint[]
}

/** 曲线最多保留的点数，与服务端带宽环形缓冲一致（60 拍 × 1 秒）。
 *  一致性由 `npm run check` 的「带宽缓冲前后端一致」一项锁定 ——
 *  两处都是「最近一分钟」这个口径，改了一边另一边会失配而不报错。 */
export const BANDWIDTH_POINTS = 60

/**
 * 把新的一帧并进曲线。
 *
 * 规则：
 *   · 带 history 的帧（建连补帧）**替换**整条曲线，而不是追加 ——
 *     追加的话每次重连都会在曲线上留下一段重复的点；
 *   · 只有 latest 的帧追加一个点，并裁到 BANDWIDTH_POINTS；
 *   · 不可用的拍（available=false）**照样入列**，只是数值按 0 画 ——
 *     把它从曲线上挖掉会在图上留一个洞，而那个洞看起来像「断线」，
 *     实际上只是这一刻没读到。真正该表达的是「这一段没有可信读数」，
 *     由状态文案负责说，曲线保持连续更像事实（真的没流量时也就是 0）。
 */
export function mergeBandwidth(prev: BandwidthPoint[], frame: BandwidthFrame): BandwidthPoint[] {
  if (frame.history?.length) {
    return frame.history.slice(-BANDWIDTH_POINTS)
  }
  return [...prev, frame.latest].slice(-BANDWIDTH_POINTS)
}

/** 一拍的总流量（收 + 发），曲线与纵轴都看它。 */
export function pointTotal(p: BandwidthPoint): number {
  if (!p.available) return 0
  return Math.max(0, p.rx_bytes_per_sec) + Math.max(0, p.tx_bytes_per_sec)
}

/**
 * 按 1024 进位缩放到合适的单位。
 *
 * 阈值用 1024 的幂而不是 1000：与操作系统、`top`、`iftop` 的显示口径一致。
 * 运维习惯上「1 KB/s」指的是 1024 B/s —— 用 1000 会与这些工具的数字对不上，
 * 而这正是排障时最容易被当成「两边数据不一样」的地方。
 *
 * 小数位的取舍：小于 10 时给一位（1.4 KB/s），大于等于 10 时取整
 * （14 KB/s）。一位小数在两位数以上只是视觉噪声，而它每帧都在变，
 * 跳动的末位反而让人读不出量级。
 *
 * 速率与累计流量共用这一份实现，只有单位串不同 —— 分开写两份的话，
 * 进位规则一旦要调整，总有一处会被漏掉。
 */
function scaled(v: number, units: string[]): string {
  if (!Number.isFinite(v) || v <= 0) return '0 ' + units[0]
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  // 不足 1 单位的零头四舍五入到 0 时仍然显示 0 —— 这是对的：
  // 「0.4 B/s」对一个按秒刷新的读数没有意义
  return i === 0 ? `${Math.round(v)} ${units[0]}` : `${v < 10 ? v.toFixed(1) : v.toFixed(0)} ${units[i]}`
}

/** 速率：B/s → KB/s → MB/s → GB/s。 */
export function formatRate(bytesPerSec: number): string {
  return scaled(bytesPerSec, ['B/s', 'KB/s', 'MB/s', 'GB/s', 'TB/s'])
}

/** 累计流量：B → KB → MB → GB（与速率同一套进位）。 */
export function formatBytes(bytes: number): string {
  return scaled(bytes, ['B', 'KB', 'MB', 'GB', 'TB'])
}

/**
 * 读数的状态：正常 / 空闲 / 不可用。
 *
 * 拆出 `idle` 的理由是界面提示：空闲要说「没有流量」，不可用要说
 * 「采集不到（原因）」—— 两者的处置完全不同（前者什么都不用做，
 * 后者要去看网卡名、权限、平台支持）。
 *
 * 只收 latest：它恒等于曲线的最后一拍（mergeBandwidth 两种分支都保留尾元素），
 * 所以「有没有拍到」这件事已经由 latest 是否为 null 表达，不必再传点数。
 */
export type BandwidthState = 'live' | 'idle' | 'unavailable'

export function bandwidthState(latest: BandwidthPoint | null): BandwidthState {
  if (!latest) return 'unavailable'
  if (!latest.available) return 'unavailable'
  return pointTotal(latest) > 0 ? 'live' : 'idle'
}

/**
 * 曲线的纵轴上限。
 *
 * 取窗口内的最大值而不是固定刻度：带宽的量级在「几十 B/s 的心跳」与
 * 「几十 MB/s 的备份」之间跨越六个数量级，固定上限要么把空闲画成一条贴底
 * 的直线、要么把峰值削平。
 *
 * 下限 1 KB/s 是为了空闲时不要因为 1 B/s 的抖动把纵轴放到极小，
 * 让整条曲线看起来像在剧烈震荡 —— 那会误导成「有流量」。
 *
 * 再乘 1.15 留一点顶部余量：正好等于最大值的线会贴着框顶，读不出那是不是峰值。
 */
export function curveCeiling(points: BandwidthPoint[]): number {
  let max = 0
  for (const p of points) {
    const t = pointTotal(p)
    if (t > max) max = t
  }
  return Math.max(1024, max * 1.15)
}

/**
 * 把一串读数映射成 SVG 折线坐标（y 轴向上为 0，与 SVG 的坐标系相反）。
 *
 * 点数不足以画线（0 或 1 个点）时返回空串，让调用方渲染占位而不是一条
 * 「贴边的一条线」—— 后者看起来像「一直满速」。
 */
export function polylinePoints(
  points: BandwidthPoint[],
  width: number,
  height: number,
  ceiling: number
): string {
  if (points.length < 2 || width <= 0 || height <= 0 || ceiling <= 0) return ''
  const step = width / (points.length - 1)
  return points
    .map((p, i) => {
      const ratio = Math.min(1, pointTotal(p) / ceiling)
      // 最小值留 0.5px：为 0 的拍贴在底边上，与「没有这一拍」在视觉上会混
      const y = height - Math.max(0.5, ratio * height)
      return `${(i * step).toFixed(2)},${y.toFixed(2)}`
    })
    .join(' ')
}
