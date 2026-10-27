// 主题存储的读取校验、老档位迁移与选择联动（2026-09-27 立，2026-09-30 随多主题重写）。
//
// readStoredTheme 不校验的话，localStorage 里任何历史遗留值都会让 data-theme
// 被设成一个没有对应样式块的锚点，页面整页退回「无主题」状态 ——
// theme.ts 的注释里写着「必须在登录页/主布局初始化时读一次，取到非法值会让
// apply() 把页面直接退回无主题状态」。这里把「非法值回落」「老键迁移」
// 「快捷切换记忆」「同 id 是空操作」这几条行为钉住。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useThemeStore } from './theme'
import { THEMES, themeById } from '@/utils/themes'

const THEME_KEY = 'llm-relay-theme'
const STORAGE_MODE_KEY = 'llm-relay-theme-mode'

let meta: HTMLMetaElement

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  meta = document.createElement('meta')
  meta.setAttribute('name', 'theme-color')
  document.head.appendChild(meta)
  setActivePinia(createPinia())
})

afterEach(() => {
  meta.remove()
  vi.restoreAllMocks()
  delete (document as unknown as Record<string, unknown>).startViewTransition
})

describe('存储读取校验', () => {
  it('没有存储值时默认为 auto 跟随系统', () => {
    const store = useThemeStore()
    expect(store.mode).toBe('auto')
    expect(store.themeId).toBe('light')
    expect(store.tone).toBe('light')
  })

  it('存储的 dark 模式被正常读取', () => {
    localStorage.setItem(STORAGE_MODE_KEY, 'dark')
    const store = useThemeStore()
    expect(store.mode).toBe('dark')
    expect(store.themeId).toBe('dark')
    expect(store.tone).toBe('dark')
    expect(store.current.name).toBe('深灰')
  })

  it('非法的主题值回落为 auto', () => {
    for (const bad of ['solarize', '', 'DARK', 'light ']) {
      localStorage.clear()
      localStorage.setItem(STORAGE_MODE_KEY, bad)
      setActivePinia(createPinia())
      const store = useThemeStore()
      expect(store.mode).toBe('auto')
      expect(store.themeId).toBe('light')
      expect(store.tone).toBe('light')
    }
  })
})

describe('切换与落盘', () => {
  it('setTheme / setMode 写属性、落盘，并按注册表设置浏览器外壳色', async () => {
    const store = useThemeStore()
    store.setMode('dark')
    await nextTick()

    expect(store.mode).toBe('dark')
    expect(store.themeId).toBe('dark')
    expect(store.tone).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(localStorage.getItem(STORAGE_MODE_KEY)).toBe('dark')
    expect(meta.getAttribute('content')).toBe(themeById('dark').shell)
  })

  it('暖白与深灰两套主题的外壳色保持规范', async () => {
    const store = useThemeStore()
    const seen: Record<string, string | null> = {}
    for (const id of ['light', 'dark']) {
      store.setTheme(id)
      await nextTick()
      seen[id] = meta.getAttribute('content')
    }
    expect(seen).toEqual({ light: '#c87864', dark: '#202020' })
  })

  it('非法 id 被忽略，不会把 data-theme 写成没有样式块的值', async () => {
    const store = useThemeStore()
    store.setTheme('solarize')
    await nextTick()
    expect(store.themeId).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('同 mode 再选一次是空操作（不重复触发 View Transition）', () => {
    const store = useThemeStore()
    const startViewTransition = vi.fn((cb: () => void) => {
      cb()
      return { ready: Promise.resolve() }
    })
    ;(document as unknown as { startViewTransition: unknown }).startViewTransition = startViewTransition
    ;(document.documentElement as unknown as { animate: unknown }).animate = vi.fn()

    store.setMode(store.mode, { x: 10, y: 10 })
    expect(startViewTransition).not.toHaveBeenCalled()

    store.setMode('dark', { x: 10, y: 10 })
    expect(startViewTransition).toHaveBeenCalledTimes(1)
    expect(store.mode).toBe('dark')
    expect(store.themeId).toBe('dark')
  })

  it('注册表里暖白与深灰都能选中且各自落到自己的 data-theme 上', async () => {
    const store = useThemeStore()
    for (const t of THEMES) {
      store.setTheme(t.id)
      await nextTick()
      expect(document.documentElement.getAttribute('data-theme')).toBe(t.id)
      expect(store.current.id).toBe(t.id)
      expect(store.tone).toBe(t.tone)
    }
  })
})
