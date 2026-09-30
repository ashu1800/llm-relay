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
const LEGACY_STYLE_KEY = 'llm-relay-darkstyle'
const LAST_LIGHT_KEY = 'llm-relay-theme-light-last'
const LAST_DARK_KEY = 'llm-relay-theme-dark-last'

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
  it('没有存储值时跟随系统（happy-dom 下 prefers-color-scheme 不命中 → 浅色）', () => {
    const store = useThemeStore()
    expect(store.themeId).toBe('light')
    expect(store.tone).toBe('light')
  })

  it('新主题的合法 id 被读取', () => {
    localStorage.setItem(THEME_KEY, 'nord')
    const store = useThemeStore()
    expect(store.themeId).toBe('nord')
    expect(store.tone).toBe('dark')
    expect(store.current.upstream).toBe('Nord')
  })

  it('非法的主题值（历史遗留/手改脏数据）回落而不是带着坏值进 apply', () => {
    for (const bad of ['auto', 'solarize', '', 'DARK', 'light ']) {
      localStorage.clear()
      localStorage.setItem(THEME_KEY, bad)
      setActivePinia(createPinia())
      const store = useThemeStore()
      expect(store.themeId).toBe('light')
      expect(store.tone).toBe('light')
    }
  })

  it('老档位键：主题=dark + 档位=oled 迁移成主题 oled（且老键被清掉）', () => {
    localStorage.setItem(THEME_KEY, 'dark')
    localStorage.setItem(LEGACY_STYLE_KEY, 'oled')
    const store = useThemeStore()
    // 顺序很关键：先认老键再认主题键，否则老用户会被当成一个普通深灰用户
    expect(store.themeId).toBe('oled')
    expect(store.current.name).toBe('深空纯黑')
    expect(localStorage.getItem(LEGACY_STYLE_KEY)).toBeNull()
  })

  it('老档位键：没有主题键时也迁移（老版本可能没写过主题键）', () => {
    localStorage.setItem(LEGACY_STYLE_KEY, 'oled')
    const store = useThemeStore()
    expect(store.themeId).toBe('oled')
    expect(localStorage.getItem(LEGACY_STYLE_KEY)).toBeNull()
  })

  it('老档位键：主题=light + 档位=oled 保持浅色（档位只对深色底生效）', () => {
    // 老版本里档位与明暗是两个独立的键：选了 oled 之后还能点灯泡切到浅色，
    // 此时档位键仍是 oled。迁移时把人拽回深色等于替他换了一次主题。
    localStorage.setItem(THEME_KEY, 'light')
    localStorage.setItem(LEGACY_STYLE_KEY, 'oled')
    const store = useThemeStore()
    expect(store.themeId).toBe('light')
    expect(store.tone).toBe('light')
    expect(localStorage.getItem(LEGACY_STYLE_KEY)).toBeNull()
  })

  it('老档位键是 classic 时不影响主题键', () => {
    localStorage.setItem(THEME_KEY, 'gruvbox-dark')
    localStorage.setItem(LEGACY_STYLE_KEY, 'classic')
    const store = useThemeStore()
    expect(store.themeId).toBe('gruvbox-dark')
    expect(localStorage.getItem(LEGACY_STYLE_KEY)).toBeNull()
  })

  it('老档位键里的脏值被忽略', () => {
    localStorage.setItem(THEME_KEY, 'mocha')
    localStorage.setItem(LEGACY_STYLE_KEY, 'gray')
    const store = useThemeStore()
    expect(store.themeId).toBe('mocha')
  })

  it('快捷切换的记忆是脏值时回落该族默认', () => {
    localStorage.setItem(LAST_LIGHT_KEY, 'not-a-theme')
    localStorage.setItem(LAST_DARK_KEY, 'latte') // 浅色主题写进了深色键
    const store = useThemeStore()
    expect(store.lastLight).toBe('light')
    expect(store.lastDark).toBe('dark')
  })
})

describe('切换与落盘', () => {
  it('setTheme 写属性、落盘，并按注册表设置浏览器外壳色', async () => {
    const store = useThemeStore()
    store.setTheme('mocha')
    await nextTick()

    expect(store.themeId).toBe('mocha')
    expect(store.tone).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('mocha')
    expect(localStorage.getItem(THEME_KEY)).toBe('mocha')
    expect(meta.getAttribute('content')).toBe(themeById('mocha').shell)
  })

  it('三套老主题的外壳色逐字不变（改主题系统不该顺手改掉地址栏）', async () => {
    const store = useThemeStore()
    const seen: Record<string, string | null> = {}
    for (const id of ['light', 'dark', 'oled']) {
      store.setTheme(id)
      await nextTick()
      seen[id] = meta.getAttribute('content')
    }
    expect(seen).toEqual({ light: '#c87864', dark: '#202020', oled: '#0a0a0a' })
  })

  it('同族换主题也算换主题（antd 令牌依赖的是主题 id，不是明暗）', async () => {
    localStorage.setItem(THEME_KEY, 'nord')
    const store = useThemeStore()
    expect(store.tone).toBe('dark')
    store.setTheme('dracula')
    await nextTick()
    expect(store.themeId).toBe('dracula')
    // 明暗没变 —— 界面若只盯 tone 就会停在 Nord 的配色上（这条是回归钉子）
    expect(store.tone).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dracula')
  })

  it('非法 id 被忽略，不会把 data-theme 写成没有样式块的值', async () => {
    const store = useThemeStore()
    store.setTheme('solarize')
    await nextTick()
    expect(store.themeId).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('同 id 再选一次是空操作（不启动 View Transition，也不重复落盘）', () => {
    const store = useThemeStore()
    const startViewTransition = vi.fn((cb: () => void) => {
      cb()
      return { ready: Promise.resolve() }
    })
    ;(document as unknown as { startViewTransition: unknown }).startViewTransition = startViewTransition
    ;(document.documentElement as unknown as { animate: unknown }).animate = vi.fn()

    store.setTheme(store.themeId, { x: 10, y: 10 })
    expect(startViewTransition).not.toHaveBeenCalled()

    store.setTheme('nord', { x: 10, y: 10 })
    expect(startViewTransition).toHaveBeenCalledTimes(1)
    expect(store.themeId).toBe('nord')
  })

  it('快捷切换切到该族上次用过的主题，并把新选择记下来', async () => {
    const store = useThemeStore()

    // 用户在浅色族里挑了「日光浅」，然后跑去深色族
    store.setTheme('solarized-light')
    await nextTick()
    expect(localStorage.getItem(LAST_LIGHT_KEY)).toBe('solarized-light')

    store.setTone('dark')
    await nextTick()
    expect(store.themeId).toBe('dark')

    store.setTheme('gruvbox-dark')
    await nextTick()
    expect(store.lastDark).toBe('gruvbox-dark')
    expect(localStorage.getItem(LAST_DARK_KEY)).toBe('gruvbox-dark')

    // 再切回浅色：应该回到「日光浅」，而不是被重置成默认浅色 ——
    // 这正是快捷按钮存在的理由（临时看一眼另一边，不丢自己的主题）
    store.setTone('light')
    await nextTick()
    expect(store.themeId).toBe('solarized-light')

    store.setTone('dark')
    await nextTick()
    expect(store.themeId).toBe('gruvbox-dark')
  })

  it('快捷记忆跨会话有效（刷新后还在）', () => {
    localStorage.setItem(THEME_KEY, 'nord')
    localStorage.setItem(LAST_DARK_KEY, 'tokyo-night')
    setActivePinia(createPinia())
    const store = useThemeStore()
    expect(store.themeId).toBe('nord')
    // 当前主题属于深色族，所以它同时是「深色族上次用过的」
    expect(store.lastDark).toBe('nord')
  })

  it('浏览器不支持 View Transitions 时退回普通切换', async () => {
    const store = useThemeStore()
    expect((document as unknown as Record<string, unknown>).startViewTransition).toBeUndefined()
    store.setTheme('latte', { x: 10, y: 10 })
    expect(store.themeId).toBe('latte')
    await nextTick()
    expect(document.documentElement.getAttribute('data-theme')).toBe('latte')
  })

  it('注册表里每套主题都能选中且各自落到自己的 data-theme 上', async () => {
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
