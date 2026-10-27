import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import {
  DEFAULT_DARK,
  DEFAULT_LIGHT,
  isThemeId,
  themeById,
  type ThemeMode,
  type ThemeTone
} from '@/utils/themes'

const STORAGE_KEY = 'llm-relay-theme'
const STORAGE_MODE_KEY = 'llm-relay-theme-mode'

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStoredMode())
  const systemDark = ref(
    typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches
  )

  if (typeof window !== 'undefined' && window.matchMedia) {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    mq.addEventListener('change', (e) => {
      systemDark.value = e.matches
    })
  }

  const themeId = computed<string>(() => {
    if (mode.value === 'auto') {
      return systemDark.value ? DEFAULT_DARK : DEFAULT_LIGHT
    }
    return mode.value
  })

  const tone = computed<ThemeTone>(() => themeById(themeId.value).tone)
  const current = computed(() => themeById(themeId.value))

  function apply(id: string) {
    document.documentElement.setAttribute('data-theme', id)
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) meta.setAttribute('content', themeById(id).shell)
  }

  function setMode(next: ThemeMode, point?: { x: number; y: number }) {
    if (next === mode.value) return
    if (point) withBurst(point.x, point.y, () => (mode.value = next))
    else mode.value = next
  }

  function setTheme(id: string, point?: { x: number; y: number }) {
    if (id === 'auto') {
      setMode('auto', point)
      return
    }
    if (!isThemeId(id)) return
    setMode(id as ThemeMode, point)
  }

  function setTone(next: ThemeTone) {
    setMode(next === 'dark' ? 'dark' : 'light')
  }

  function withBurst(x: number, y: number, mutate: () => void) {
    const doc = document as Document & {
      startViewTransition?: (cb: () => void) => { ready: Promise<void> }
    }
    const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    if (!doc.startViewTransition || reduced) {
      mutate()
      return
    }
    const radius = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y))
    const transition = doc.startViewTransition(mutate)
    transition.ready
      .then(() => {
        document.documentElement.animate(
          {
            clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${radius}px at ${x}px ${y}px)`]
          },
          { duration: 450, easing: 'ease-in-out', pseudoElement: '::view-transition-new(root)' }
        )
      })
      .catch(() => {})
  }

  watch(
    mode,
    (m) => {
      try {
        localStorage.setItem(STORAGE_MODE_KEY, m)
        localStorage.setItem(STORAGE_KEY, m)
      } catch {}
    },
    { immediate: true }
  )

  watch(
    themeId,
    (id) => {
      apply(id)
    },
    { immediate: true }
  )

  return {
    mode,
    themeId,
    tone,
    current,
    lastLight: ref(DEFAULT_LIGHT),
    lastDark: ref(DEFAULT_DARK),
    apply,
    setMode,
    setTheme,
    setTone,
    withBurst
  }
})

function readStoredMode(): ThemeMode {
  try {
    const m = localStorage.getItem(STORAGE_MODE_KEY)
    if (m === 'light' || m === 'dark' || m === 'auto') return m
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored === 'dark') return 'dark'
    if (stored === 'light') return 'light'
    if (stored === 'auto') return 'auto'
  } catch {}
  return 'auto'
}
