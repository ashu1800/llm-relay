import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

type ThemeMode = 'light' | 'dark'

/**
 * 深色的质感档位（2026-09-27）：classic 是原有深灰，oled 是「深空纯黑」
 * 表面叠加层（见 theme.css 的 oled 块）。它不是第三套主题 —— 文字、
 * 语义色、光标全部沿用深色主题，只换表面四件套，所以独立存一个键、
 * 以 <html data-darkstyle> 属性表达，浅色下那层选择器不命中、零副作用。
 */
type DarkStyle = 'classic' | 'oled'

const STORAGE_KEY = 'llm-relay-theme'
const DARK_STYLE_KEY = 'llm-relay-darkstyle'

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
  const darkStyle = ref<DarkStyle>(readStoredDarkStyle())

  function apply(next: ThemeMode) {
    document.documentElement.setAttribute('data-theme', next)
    // 质感档位常驻：浅色下 oled 那层选择器不命中，写了也没有副作用；
    // 而常驻写法让「浅色 ↔ 深色(oled)」来回切换时不需要在这里补属性
    document.documentElement.setAttribute('data-darkstyle', darkStyle.value)
    isDark.value = next === 'dark'
    // 浏览器外壳（地址栏/状态栏）跟着主题换色：不更新的话深色主题下
    // 一圈米色非常突兀。meta 在 index.html 里带主色初值，这里只做跟随；
    // OLED 档的深色外壳要跟到纯黑，否则地址栏一圈 #202020 灰得扎眼
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) {
      const darkShell = darkStyle.value === 'oled' ? '#0a0a0a' : '#202020'
      meta.setAttribute('content', next === 'dark' ? darkShell : '#c87864')
    }
  }

  function toggle() {
    mode.value = mode.value === 'dark' ? 'light' : 'dark'
  }

  /**
   * 选深色质感档位。浅色/深色本身仍从看板右上角的灯泡切换；
   * 这里选的是「深色长什么样」—— 所以选定后若当前是浅色，
   * 直接切到深色让选择立刻看得见（选了却看不见等于没选上）。
   */
  function setDarkStyle(s: DarkStyle) {
    darkStyle.value = s
    if (mode.value !== 'dark') toggle()
    else apply('dark')
  }

  /**
   * 带圆形扩散的主题切换（View Transitions API）。
   *
   * 从点击位置把新主题「扩散」出去 —— 十几行代码换来一次观感拔群的切换，
   * 让一个本来纯功能性的按钮变成全站最顺手的小玩具。
   * 浏览器不支持（Firefox 旧版等）或系统开了「减少动态效果」时，
   * 退回普通切换：功能完全不受影响，只是没有动画。
   */
  function toggleWithBurst(x: number, y: number) {
    const doc = document as Document & {
      startViewTransition?: (cb: () => void) => { ready: Promise<void> }
    }
    const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    if (!doc.startViewTransition || reduced) {
      toggle()
      return
    }
    // 扩散半径取「点击点到最远角」：圆要盖满整个视口才收尾
    const radius = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y))
    const transition = doc.startViewTransition(() => toggle())
    transition.ready
      .then(() => {
        document.documentElement.animate(
          {
            clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${radius}px at ${x}px ${y}px)`]
          },
          { duration: 450, easing: 'ease-in-out', pseudoElement: '::view-transition-new(root)' }
        )
      })
      .catch(() => {
        // ready 被打断（用户快速连点）不算错：浏览器对新旧快照有兜底的交叉淡变
      })
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

  // 质感档位单独持久化：换主题（浅↔深）不该把它一起冲掉
  watch(darkStyle, (v) => {
    try {
      localStorage.setItem(DARK_STYLE_KEY, v)
    } catch {
      // 同上：存不下只影响「下次还记不记得」
    }
  })

  return { mode, isDark, darkStyle, apply, toggle, toggleWithBurst, setDarkStyle }
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

// readStoredDarkStyle 与 readStoredMode 同一套校验逻辑：
// 非法值（手改的脏数据）回落 classic，绝不让一个没有对应样式块的档位
// 写到 <html> 上 —— 那会让叠加层永远不命中、选择等于失效。
function readStoredDarkStyle(): DarkStyle {
  try {
    const raw = localStorage.getItem(DARK_STYLE_KEY)
    if (raw === 'oled' || raw === 'classic') return raw
  } catch {
    // 忽略：回落默认档
  }
  return 'classic'
}
