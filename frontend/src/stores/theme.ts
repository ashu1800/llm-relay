import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

type ThemeMode = 'light' | 'dark'

const STORAGE_KEY = 'llm-relay-theme'

// 主题切换只做一件事：在 <html> 上设置 data-theme 属性。
//
// 所有颜色令牌的定义都在 styles/theme.css 里一处维护
// （:root 是浅色，:root[data-theme='dark'] 是深色）。
//
// 这里以前还在 JS 里存了一份同样的色值，并在运行时用
// document.documentElement.style.setProperty 逐个写进去。那是两个问题：
//
//   1. 双份定义会漂移。同一份色值改了一处、忘了另一处，表现就是
//      「改了 CSS 不生效」——内联样式的优先级高于样式表，实际生效的
//      永远是 JS 那份，而排查时看 CSS 怎么都看不出问题。
//   2. 用 JS 重新声明变量等于放弃了 CSS 的层叠能力：深色主题本该只是
//      覆盖几个变量（下面的 dark 块就是这么写的），却被迫把每个变量
//      都在两套 JS 对象里抄一遍。
//
// 现在深色主题新增一个变量只需要在 CSS 的 dark 块里写一行，
// 不会再出现「加在 CSS 里但不生效」的情况。
export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStoredMode())
  const isDark = ref(mode.value === 'dark')

  function apply(next: ThemeMode) {
    document.documentElement.setAttribute('data-theme', next)
    isDark.value = next === 'dark'
    // 浏览器外壳（地址栏/状态栏）跟着主题换色：不更新的话深色主题下
    // 一圈米色非常突兀。meta 在 index.html 里带主色初值，这里只做跟随
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) {
      meta.setAttribute('content', next === 'dark' ? '#202020' : '#c87864')
    }
  }

  function toggle() {
    mode.value = mode.value === 'dark' ? 'light' : 'dark'
  }

  watch(
    mode,
    (v) => {
      try {
        localStorage.setItem(STORAGE_KEY, v)
      } catch {
        // 隐私模式下 localStorage 会抛异常。主题偏好存不下不影响使用，
        // 但不能让这个异常打断 apply —— 否则页面主题会停在切换前的状态。
      }
      apply(v)
    },
    { immediate: true }
  )

  return { mode, isDark, apply, toggle }
})

// readStoredMode 读取并校验本地存储里的主题。
//
// 必须校验：localStorage 里的值可能是任意历史遗留内容（例如更早版本
// 存过 'auto'），而它只在登录页/主布局初始化时读一次 —— 取到非法值会让
// apply() 把 data-theme 设成一个没有对应样式块的值，页面直接退回无主题状态。
function readStoredMode(): ThemeMode {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === 'dark' || raw === 'light') {
      return raw
    }
  } catch {
    // 忽略：下面回落到默认值
  }
  // 没有显式偏好时跟随系统。这是用户对「深色模式」最普遍的预期，
  // 而且不引入第三个状态 —— 一旦用户手动切过，就以他自己的选择为准。
  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) {
    return 'dark'
  }
  return 'light'
}
