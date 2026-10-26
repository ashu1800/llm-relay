// 读剪贴板的返回语义钉住（utils/clipboard.ts）：
//   · 拿到字符串原样返回（含空串 —— 空剪贴板和读取失败是两件事）
//   · 权限被拒 / API 不存在 / 抛错 -> null
//   · 挂起（等授权）不能无限等，超时 -> null
//
// 第三条是这次最容易漏的：readText 在浏览器弹授权框时既不 resolve 也不
// reject，调用方会一直 await 着，界面看起来像「点了没反应」。超时退路
// 与 writeClipboard 是同一套思路，但阈值不同（2s vs 800ms）——
// 读要靠用户点一下授权框，800ms 会把正在看的提示误判成失败。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readClipboard } from './clipboard'

/** 换掉 navigator.clipboard，返回恢复函数 */
function stubClipboard(impl: { readText?: () => Promise<string> }): () => void {
  const original = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
  Object.defineProperty(navigator, 'clipboard', {
    value: impl,
    configurable: true,
    writable: true
  })
  return () => {
    if (original) Object.defineProperty(navigator, 'clipboard', original)
    else Reflect.deleteProperty(navigator as object, 'clipboard')
  }
}

afterEach(() => {
  vi.useRealTimers()
})

describe('readClipboard', () => {
  it('读到字符串时原样返回', async () => {
    const restore = stubClipboard({ readText: async () => '{"input_per_1m":"0.15"}' })
    try {
      await expect(readClipboard()).resolves.toBe('{"input_per_1m":"0.15"}')
    } finally {
      restore()
    }
  })

  it('空剪贴板返回空串，而不是 null', async () => {
    // 区分这两者是为了让调用方给出不同的提示：
    // 「剪贴板是空的」和「浏览器不让你读」是两种完全不同的下一步
    const restore = stubClipboard({ readText: async () => '' })
    try {
      await expect(readClipboard()).resolves.toBe('')
    } finally {
      restore()
    }
  })

  it('权限被拒（抛错）返回 null', async () => {
    const restore = stubClipboard({
      readText: async () => {
        throw new DOMException('denied', 'NotAllowedError')
      }
    })
    try {
      await expect(readClipboard()).resolves.toBeNull()
    } finally {
      restore()
    }
  })

  it('剪贴板 API 不存在时返回 null（http 访问的典型情形）', async () => {
    const restore = stubClipboard({})
    try {
      await expect(readClipboard()).resolves.toBeNull()
    } finally {
      restore()
    }
  })

  it('耗时超过上限时返回 null，不会挂住调用方', async () => {
    vi.useFakeTimers()
    // 永不 resolve：模拟「等用户点授权框」的状态
    const restore = stubClipboard({ readText: () => new Promise<string>(() => {}) })
    try {
      const pending = readClipboard()
      await vi.advanceTimersByTimeAsync(2000)
      await expect(pending).resolves.toBeNull()
    } finally {
      restore()
    }
  })
})
