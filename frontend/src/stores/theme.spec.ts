// 主题存储的读取校验与档位联动（2026-09-27 立测）。
//
// readStoredMode / readStoredDarkStyle 不校验的话，localStorage 里任何
// 历史遗留值都会让 data-theme / data-darkstyle 被设成没有对应样式块的值，
// 页面整页退回无主题状态 —— theme.ts 的注释里写着「必须在登录页/主布局
// 初始化时读一次，取到非法值会让 apply() 把页面直接退回无主题状态」。
// 这里把「非法值回落」与「OLED 档联动切深色」两条行为钉住。
import { beforeEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useThemeStore } from './theme'

const THEME_KEY = 'llm-relay-theme'
const STYLE_KEY = 'llm-relay-darkstyle'

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.removeAttribute('data-darkstyle')
  setActivePinia(createPinia())
})

describe('存储读取校验', () => {
  it('没有存储值时跟随系统（happy-dom 下 prefers-color-color 不命中 → 浅色）', () => {
    const store = useThemeStore()
    expect(store.mode).toBe('light')
    expect(store.darkStyle).toBe('classic')
  })

  it('合法的主题值被读取（浅色偏好优先于系统）', () => {
    localStorage.setItem(THEME_KEY, 'light')
    const store = useThemeStore()
    expect(store.mode).toBe('light')
  })

  it('非法的主题值（历史遗留）回落而不是带着坏值进 apply', () => {
    localStorage.setItem(THEME_KEY, 'auto') // 更早版本存过的值
    const store = useThemeStore()
    expect(store.mode).toBe('light')
    expect(['light', 'dark']).toContain(store.mode)
  })

  it('非法的深色档位值回落 classic', () => {
    localStorage.setItem(STYLE_KEY, 'gray')
    const store = useThemeStore()
    expect(store.darkStyle).toBe('classic')
  })

  it('合法的 OLED 档位值被读取', () => {
    localStorage.setItem(STYLE_KEY, 'oled')
    const store = useThemeStore()
    expect(store.darkStyle).toBe('oled')
  })
})

describe('档位与模式联动', () => {
  it('setDarkStyle 在浅色下自动切到深色（选了却看不见等于没选上）', async () => {
    const store = useThemeStore()
    expect(store.mode).toBe('light')

    store.setDarkStyle('oled')
    await nextTick()

    expect(store.mode).toBe('dark')
    expect(store.isDark).toBe(true)
    // html 属性是 CSS 两个叠加层（classic / oled）的选择器锚点
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.documentElement.getAttribute('data-darkstyle')).toBe('oled')
    // 偏好持久化（换主题不该把档位冲掉）
    expect(localStorage.getItem(STYLE_KEY)).toBe('oled')
    expect(localStorage.getItem(THEME_KEY)).toBe('dark')
  })

  it('深色下切档只换档位、不换模式', async () => {
    localStorage.setItem(THEME_KEY, 'dark')
    const store = useThemeStore()
    expect(store.mode).toBe('dark')

    store.setDarkStyle('oled')
    await nextTick()
    expect(store.mode).toBe('dark')
    expect(document.documentElement.getAttribute('data-darkstyle')).toBe('oled')
  })

  it('浅色下档位属性也常驻（oled 那层选择器在浅色下不命中，零副作用）', () => {
    const store = useThemeStore()
    expect(store.mode).toBe('light')
    expect(document.documentElement.getAttribute('data-darkstyle')).toBe('classic')
  })

  it('meta theme-color 跟随模式与档位（OLED 的浏览器外壳要纯黑）', async () => {
    const meta = document.createElement('meta')
    meta.setAttribute('name', 'theme-color')
    document.head.appendChild(meta)

    try {
      const store = useThemeStore()
      expect(meta.getAttribute('content')).toBe('#c87864')

      store.setDarkStyle('oled')
      await nextTick()
      expect(meta.getAttribute('content')).toBe('#0a0a0a')

      store.setDarkStyle('classic')
      await nextTick()
      expect(meta.getAttribute('content')).toBe('#202020')
    } finally {
      meta.remove()
    }
  })
})
