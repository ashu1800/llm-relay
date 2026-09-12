import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

type ThemeMode = 'light' | 'dark'

const STORAGE_KEY = 'llm-relay-theme'

// 主题令牌对齐参考站 llm.ohub.vip 的 light / dark 两套配置
const LIGHT = {
  primary: '#c87864',
  secondary: '#afbeaf',
  highlight: '#afbeaf',
  bg: '#f8f5ee',
  fg: '#ffffff',
  fgShadow: '0 0 0.5rem 0 rgba(0, 0, 0, 0.1)',
  text: '#303030',
  textSecondary: 'rgba(48, 48, 48, 0.65)',
  border: '#d9d9d9',
  scrollbarThumb: 'rgba(220, 220, 220)',
  scrollbarTrack: 'transparent'
}

const DARK = {
  primary: '#c87864',
  secondary: '#afbeaf',
  highlight: '#afbeaf',
  bg: '#202020',
  fg: '#303030',
  fgShadow: '0 0 0.5rem 0 rgba(0, 0, 0, 0.8)',
  text: '#c8c8c8',
  textSecondary: 'rgba(200, 200, 200, 0.65)',
  border: '#424242',
  scrollbarThumb: 'rgba(100, 100, 100)',
  scrollbarTrack: 'transparent'
}

const FONT_FAMILY = "'Harding', 'STSong', 'SimSun', 'Songti SC', '宋体', 'Hiragino Sans GB', 'STHeiti', 'WenQuanYi Micro Hei', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, serif"

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>((localStorage.getItem(STORAGE_KEY) as ThemeMode) || 'light')
  const isDark = ref(mode.value === 'dark')

  function apply(next: ThemeMode) {
    const t = next === 'dark' ? DARK : LIGHT
    const root = document.documentElement
    root.setAttribute('data-theme', next)
    root.style.setProperty('--font-family-base', FONT_FAMILY)
    root.style.setProperty('--color-primary', t.primary)
    root.style.setProperty('--color-secondary', t.secondary)
    root.style.setProperty('--color-highlight', t.highlight)
    root.style.setProperty('--color-bg', t.bg)
    root.style.setProperty('--color-fg', t.fg)
    root.style.setProperty('--color-fg-shadow', t.fgShadow)
    root.style.setProperty('--color-text', t.text)
    root.style.setProperty('--color-text-secondary', t.textSecondary)
    root.style.setProperty('--color-border', t.border)
    root.style.setProperty('--scrollbar-thumb', t.scrollbarThumb)
    root.style.setProperty('--scrollbar-track', t.scrollbarTrack)
    isDark.value = next === 'dark'
  }

  function toggle() {
    mode.value = mode.value === 'dark' ? 'light' : 'dark'
  }

  watch(mode, (v) => {
    localStorage.setItem(STORAGE_KEY, v)
    apply(v)
  }, { immediate: true })

  return { mode, isDark, apply, toggle }
})
