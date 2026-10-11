// 连接状态判定的阈值逐条钉住（2026-10-11 加状态胶囊时立）。
//
// 这里唯一容易写错的就是阈值，而写错的表现很隐蔽：取小了，一条**完全健康**的
// 连接会在两次心跳之间反复闪成「无数据」；取大了，这一档永远看不到
// （45 秒的看门狗先把它变成 offline 了）。服务端心跳间隔 25 秒、看门狗 45 秒，
// 两条边界都在下面的用例里锚着。
import { describe, expect, it } from 'vitest'
import { liveStateOf, STALE_HINT_MS } from './useLive'

describe('liveStateOf 连接状态', () => {
  it('没连接一律是 offline，无论上次收到数据是多久以前', () => {
    expect(liveStateOf(false, 0, 1_000_000)).toBe('offline')
    expect(liveStateOf(false, 999_000, 1_000_000)).toBe('offline')
  })

  it('刚连上还没收到任何一帧时是 live（心跳最多 25 秒后到）', () => {
    expect(liveStateOf(true, 0, 1_000_000)).toBe('live')
  })

  it('刚收到帧是 live', () => {
    expect(liveStateOf(true, 1_000_000, 1_000_000)).toBe('live')
  })

  it('阈值必须大于服务端 25 秒的心跳间隔 —— 否则健康连接会在两次心跳之间闪成「无数据」', () => {
    expect(STALE_HINT_MS).toBeGreaterThan(25_000)
    // 26 秒（心跳间隔 + 1 秒）时仍是正常态，这是最关键的一条
    expect(liveStateOf(true, 1_000_000, 1_026_000)).toBe('live')
  })

  it('阈值必须小于 45 秒的看门狗 —— 否则这一档永远看不到', () => {
    expect(STALE_HINT_MS).toBeLessThan(45_000)
    // 阈值之后、看门狗之前：stale
    expect(liveStateOf(true, 1_000_000, 1_000_000 + STALE_HINT_MS + 1)).toBe('stale')
    // 41 秒：还没到看门狗判死，但已经该预警了
    expect(liveStateOf(true, 1_000_000, 1_041_000)).toBe('stale')
  })

  it('边界取「严格大于」：正好等于阈值时不算 stale', () => {
    expect(liveStateOf(true, 1_000_000, 1_000_000 + STALE_HINT_MS)).toBe('live')
  })
})
