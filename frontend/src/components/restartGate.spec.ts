// 重启后刷新时机的回归（2026-09-30 立，站主反馈「点完立即刷新导致 502」）。
//
// 这一条很容易被"简化"掉：探活 200 就刷新看起来天经地义，而线上表现是
// 一个 502 页面 —— 因为 200 可能来自**还没退出的旧进程**。用例把「旧进程
// 还活着」这一格单独钉住，免得日后有人把判据改回去。
import { describe, expect, it } from 'vitest'
import { RESTART_COUNTDOWN_MS, canReloadAfterRestart } from './restartGate'

describe('重启后能不能刷新页面', () => {
  it('新进程起来了（uptime 变小）→ 可以刷新', () => {
    expect(canReloadAfterRestart(3 * 24 * 3600 * 1000, 1200)).toBe(true)
  })

  it('旧进程还活着（uptime 更大）→ 不能刷新 —— 这正是 502 的那一格', () => {
    // 点完重启后紧接着探活：进程还没退出，healthz 照样 200
    expect(canReloadAfterRestart(5000, 5300)).toBe(false)
  })

  it('uptime 相同（同一毫秒读两次）→ 不能刷新', () => {
    expect(canReloadAfterRestart(5000, 5000)).toBe(false)
  })

  it('服务还没回来（探不到）→ 不能刷新', () => {
    expect(canReloadAfterRestart(5000, null)).toBe(false)
  })

  it('没读到重启前的 uptime → 靠倒计时兜底，探到即刷新', () => {
    expect(canReloadAfterRestart(null, 1200)).toBe(true)
    expect(canReloadAfterRestart(null, null)).toBe(false)
  })

  it('倒计时是 8 秒（站主 2026-09-30 定的），且必须大于 systemd 的 RestartSec=3', () => {
    expect(RESTART_COUNTDOWN_MS).toBe(8000)
    expect(RESTART_COUNTDOWN_MS).toBeGreaterThan(3000)
  })
})
