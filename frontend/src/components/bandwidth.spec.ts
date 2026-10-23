// 带宽读数口径的回归（2026-10-23）。
//
// 三件事各自都在界面上「安静地错」，所以逐条钉住：
//   · 「空闲」与「采集坏了」被混成同一个 0；
//   · 重连时把补位历史当新点追加，曲线上多出一段重复；
//   · 纵轴固定刻度，把空闲画成震荡、把峰值削平。
import { describe, expect, it } from 'vitest'
import {
  BANDWIDTH_POINTS,
  bandwidthState,
  curveCeiling,
  formatBytes,
  formatRate,
  mergeBandwidth,
  pointTotal,
  polylinePoints,
  type BandwidthPoint
} from './bandwidth'

function pt(p: Partial<BandwidthPoint> = {}): BandwidthPoint {
  return {
    at: '2026-10-23T00:00:00Z',
    interface: 'eth0',
    rx_bytes_per_sec: 0,
    tx_bytes_per_sec: 0,
    rx_total_bytes: 0,
    tx_total_bytes: 0,
    available: true,
    ...p
  }
}

describe('速率格式化', () => {
  it('按 1024 进位，与 top / iftop 的口径一致', () => {
    expect(formatRate(0)).toBe('0 B/s')
    expect(formatRate(512)).toBe('512 B/s')
    expect(formatRate(1024)).toBe('1.0 KB/s')
    expect(formatRate(1536)).toBe('1.5 KB/s')
    expect(formatRate(1024 * 1024)).toBe('1.0 MB/s')
    expect(formatRate(1024 * 1024 * 1024)).toBe('1.0 GB/s')
  })

  it('两位数以上不再给小数（末位每帧都在跳）', () => {
    expect(formatRate(9.94 * 1024)).toBe('9.9 KB/s')
    expect(formatRate(10.4 * 1024)).toBe('10 KB/s')
    expect(formatRate(120 * 1024)).toBe('120 KB/s')
  })

  it('非有限值与非正数一律当 0', () => {
    expect(formatRate(-1)).toBe('0 B/s')
    expect(formatRate(NaN)).toBe('0 B/s')
    expect(formatRate(Infinity)).toBe('0 B/s')
  })

  it('累计流量与速率同一套进位', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(2048)).toBe('2.0 KB')
    expect(formatBytes(5 * 1024 * 1024 * 1024)).toBe('5.0 GB')
  })
})

describe('曲线合并', () => {
  it('带 history 的帧替换整条曲线，而不是追加', () => {
    const prev = [pt({ rx_bytes_per_sec: 1 }), pt({ rx_bytes_per_sec: 2 })]
    const history = [pt({ rx_bytes_per_sec: 10 }), pt({ rx_bytes_per_sec: 20 }), pt({ rx_bytes_per_sec: 30 })]
    const merged = mergeBandwidth(prev, { latest: history[history.length - 1], history })
    expect(merged.map((p) => p.rx_bytes_per_sec)).toEqual([10, 20, 30])
  })

  it('只有 latest 的帧追加一个点', () => {
    const prev = [pt({ rx_bytes_per_sec: 1 })]
    const merged = mergeBandwidth(prev, { latest: pt({ rx_bytes_per_sec: 7 }) })
    expect(merged.map((p) => p.rx_bytes_per_sec)).toEqual([1, 7])
  })

  it('超出上限时丢掉最旧的点', () => {
    const prev = Array.from({ length: BANDWIDTH_POINTS }, (_, i) => pt({ rx_bytes_per_sec: i }))
    const merged = mergeBandwidth(prev, { latest: pt({ rx_bytes_per_sec: 999 }) })
    expect(merged).toHaveLength(BANDWIDTH_POINTS)
    expect(merged[0].rx_bytes_per_sec).toBe(1)
    expect(merged[merged.length - 1].rx_bytes_per_sec).toBe(999)
  })

  it('补位历史本身过长时也裁到上限', () => {
    const history = Array.from({ length: BANDWIDTH_POINTS + 20 }, (_, i) => pt({ rx_bytes_per_sec: i }))
    const merged = mergeBandwidth([], { latest: history[history.length - 1], history })
    expect(merged).toHaveLength(BANDWIDTH_POINTS)
    // 留下的一定是最近的：最后一拍保留
    expect(merged[merged.length - 1].rx_bytes_per_sec).toBe(BANDWIDTH_POINTS + 19)
  })
})

describe('状态判定', () => {
  it('有流量是 live', () => {
    expect(bandwidthState(pt({ rx_bytes_per_sec: 100 }))).toBe('live')
  })

  it('真的没流量是 idle —— 与「读不到」必须分开', () => {
    expect(bandwidthState(pt())).toBe('idle')
  })

  it('available=false 是 unavailable，哪怕速率是 0', () => {
    expect(bandwidthState(pt({ available: false, error: '平台不支持' }))).toBe('unavailable')
  })

  it('还没有任何一拍时也是 unavailable（不能显示成空闲）', () => {
    expect(bandwidthState(null)).toBe('unavailable')
  })

  it('收发都算进总量', () => {
    expect(pointTotal(pt({ rx_bytes_per_sec: 100, tx_bytes_per_sec: 50 }))).toBe(150)
  })

  it('不可用的拍按 0 计入曲线（保持曲线连续）', () => {
    expect(pointTotal(pt({ available: false, rx_bytes_per_sec: 999 }))).toBe(0)
  })
})

describe('纵轴与坐标', () => {
  it('上限取自窗口峰值，并留顶部余量', () => {
    const ceil = curveCeiling([pt({ rx_bytes_per_sec: 1000 * 1024 })])
    expect(ceil).toBeGreaterThan(1000 * 1024)
    expect(ceil).toBeCloseTo(1000 * 1024 * 1.15, 0)
  })

  it('空闲时下限 1 KB/s，避免抖动画成震荡', () => {
    expect(curveCeiling([pt(), pt({ rx_bytes_per_sec: 3 })])).toBe(1024)
    expect(curveCeiling([])).toBe(1024)
  })

  it('点数不足两个时不给折线（否则会画出一条假的贴边线）', () => {
    expect(polylinePoints([], 100, 20, 1024)).toBe('')
    expect(polylinePoints([pt()], 100, 20, 1024)).toBe('')
  })

  it('坐标覆盖整个宽度，峰值贴顶、零值贴底', () => {
    const pts = [pt(), pt({ rx_bytes_per_sec: 1024 })]
    const line = polylinePoints(pts, 100, 20, 1024)
    const coords = line.split(' ').map((s) => s.split(',').map(Number))
    expect(coords).toHaveLength(2)
    expect(coords[0][0]).toBe(0)
    expect(coords[1][0]).toBe(100)
    // 第一拍为 0：贴在底边（留 0.5px），第二拍到顶
    expect(coords[0][1]).toBeCloseTo(19.5, 1)
    expect(coords[1][1]).toBeCloseTo(0, 1)
  })

  it('超过上限的拍被夹在顶部', () => {
    const pts = [pt(), pt({ rx_bytes_per_sec: 99999 })]
    const line = polylinePoints(pts, 100, 20, 1024)
    const last = line.split(' ')[1].split(',').map(Number)
    expect(last[1]).toBe(0)
  })
})
