// 复制原位变形的状态机（2026-09-27 立测）：五个槽位的对勾共享这一份时序，
// 「重标会重置计时」与「卸载清定时器」是肉眼最容易漏的两条 ——
// 前者坏了表现为连点复制时对勾提前消失，后者坏了表现为组件卸载后
// 还有一个挂起的定时器在写已死的 ref（不崩，但属于幽灵写入）。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, h } from 'vue'
import { useCopyFeedback } from './useCopyFeedback'

/** 在真实的组件 setup 里执行组合式函数，返回结果与卸载句柄 ——
 *  onUnmounted 这类生命周期钩子只有在组件实例里才有意义 */
function withSetup<T>(composable: () => T): { result: T; unmount: () => void } {
  let result!: T
  const app = createApp({
    setup() {
      result = composable()
      return () => h('div')
    }
  })
  const root = document.createElement('div')
  app.mount(root)
  return { result, unmount: () => app.unmount() }
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('useCopyFeedback', () => {
  it('标记后 copiedKey 置位，超时（默认 900ms）自动回落', () => {
    const { result } = withSetup(() => useCopyFeedback())
    const { copiedKey, markCopied } = result

    expect(copiedKey.value).toBeNull()
    markCopied('key-7')
    expect(copiedKey.value).toBe('key-7')

    vi.advanceTimersByTime(899)
    expect(copiedKey.value).toBe('key-7')
    vi.advanceTimersByTime(1)
    expect(copiedKey.value).toBeNull()
  })

  it('自定义超时生效（详情抽屉的 err 槽位用更长档）', () => {
    const { result } = withSetup(() => useCopyFeedback(1500))
    result.markCopied('trace')
    vi.advanceTimersByTime(1400)
    expect(result.copiedKey.value).toBe('trace')
    vi.advanceTimersByTime(100)
    expect(result.copiedKey.value).toBeNull()
  })

  it('连点重新标记会重置计时（对勾不该在连点中途提前消失）', () => {
    const { result } = withSetup(() => useCopyFeedback())
    result.markCopied('baseurl')
    vi.advanceTimersByTime(800)
    result.markCopied('baseurl') // 距第一次只剩 100ms，计时必须从头算
    vi.advanceTimersByTime(800)
    expect(result.copiedKey.value).toBe('baseurl')
    vi.advanceTimersByTime(100)
    expect(result.copiedKey.value).toBeNull()
  })

  it('换槽位标记直接切换目标（互不残留旧定时器）', () => {
    const { result } = withSetup(() => useCopyFeedback())
    result.markCopied('key-1')
    result.markCopied('created')
    expect(result.copiedKey.value).toBe('created')
    vi.advanceTimersByTime(900)
    expect(result.copiedKey.value).toBeNull()
  })

  it('组件卸载时清掉挂起的定时器（不再有幽灵写入）', () => {
    const { result, unmount } = withSetup(() => useCopyFeedback())
    result.markCopied('trace')
    unmount()
    // 定时器已被 cancel：时间走到超时之后 copiedKey 也不会被改写
    vi.advanceTimersByTime(5000)
    expect(result.copiedKey.value).toBe('trace')
  })
})
