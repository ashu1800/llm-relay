import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import {
  DARK_IDS,
  DEFAULT_DARK,
  DEFAULT_LIGHT,
  LIGHT_IDS,
  isThemeId,
  themeById,
  type ThemeTone
} from '@/utils/themes'
import { readStoredChoice, writeStoredChoice } from '@/utils/persistedChoice'

const STORAGE_KEY = 'llm-relay-theme'
// 上一次用过的浅色/深色主题：弹层顶部那两枚「浅色 / 深色」快捷按钮切到它们。
// 没有这两个键的话，快捷按钮只能跳回默认主题 —— 一个常驻在 Nord 里的人
// 想临时看一眼浅色再切回来，就得在两套主题之间来回找。
const LAST_LIGHT_KEY = 'theme-light-last'
const LAST_DARK_KEY = 'theme-dark-last'

// 「深色质感档位」的老键（2026-09-27 引入，2026-09-30 随多主题取消）。
// 现在只读一次做迁移，读完就删 —— 留着它只会让人以为还存在第三个轴。
const LEGACY_DARK_STYLE_KEY = 'llm-relay-darkstyle'

// 主题切换只做一件事：在 <html> 上设置 data-theme 属性，值是主题 id。
//
// 所有颜色令牌的定义都在 styles/theme.css 里一处维护：
//   :root                     → light（浅色默认主题本尊）
//   :root[data-theme='<id>']  → 其余 11 套，每块把随底色而变的令牌写全
// 哪些 id 存在由 utils/themes.ts 的注册表说了算，这里是唯一写这个属性的地方。
//
// 这里以前（多主题之前）在 JS 里还存了一份同样的色值，并用
// document.documentElement.style.setProperty 逐个写进去。那是两个问题：
//
//   1. 双份定义会漂移。同一份色值改了一处、忘了另一处，表现就是
//      「改了 CSS 不生效」——内联样式的优先级高于样式表，实际生效的
//      永远是 JS 那份，而排查时看 CSS 怎么都看不出问题。
//   2. 用 JS 重新声明变量等于放弃了 CSS 的层叠能力：主题本该只是覆盖
//      若干变量，却被迫把每个变量在两套 JS 对象里抄一遍。
//
// 2026-09-30 扩到 12 套之后这条口径更重要了：新增一套主题只需要在 CSS 里
// 加一个块、在注册表里加一项，JS 侧一行都不用改。
export const useThemeStore = defineStore('theme', () => {
  const themeId = ref<string>(readStoredTheme())

  // 上一次用过的两族主题（快捷按钮的目标）。与主题本身同一个容错口径：
  // 存储里是脏数据就回落默认，绝不把不认识的值当成主题 id 用。
  const lastLight = ref(readStoredChoice(LAST_LIGHT_KEY, LIGHT_IDS, DEFAULT_LIGHT))
  const lastDark = ref(readStoredChoice(LAST_DARK_KEY, DARK_IDS, DEFAULT_DARK))

  const tone = computed<ThemeTone>(() => themeById(themeId.value).tone)
  const current = computed(() => themeById(themeId.value))

  function apply(id: string) {
    document.documentElement.setAttribute('data-theme', id)
    // 浏览器外壳（地址栏/状态栏）跟着主题换色：不更新的话深色主题下
    // 一圈米色非常突兀。值来自注册表（浅色族取该主题主色、深色族取页面底色），
    // 而注册表那一列由 check-contracts.mjs 对着 CSS 逐字比对 —— 不会出现
    // 「注册表里写了一个 CSS 里没有的颜色」。
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) meta.setAttribute('content', themeById(id).shell)
  }

  /**
   * 选一套主题。
   *
   * `point` 是这次切换的「起点」（鼠标点击位置）：给了它并且浏览器支持
   * View Transitions 时，新主题从点击处圆形扩散铺满全屏；没给（键盘操作）
   * 或浏览器不支持时退回普通切换 —— 功能完全一样，只是没有动画。
   *
   * 同 id 再选一次是空操作：弹层里连点同一张卡不该再放一次动画，
   * 也不该多写一次 localStorage。这条同时还是「鼠标与键盘两条路径不会互相打架」
   * 的实现依据 —— radio 的 click 先于 change 触发，先到的那个生效、后到的落在空处。
   */
  function setTheme(id: string, point?: { x: number; y: number }) {
    if (!isThemeId(id) || id === themeId.value) return
    if (point) withBurst(point.x, point.y, () => (themeId.value = id))
    else themeId.value = id
  }

  /** 快捷切换：切到该族上一次用过的主题（没记录过就是该族的默认）。 */
  function setTone(next: ThemeTone) {
    setTheme(next === 'dark' ? lastDark.value : lastLight.value)
  }

  /**
   * 带圆形扩散地执行一次变更（View Transitions API）。
   *
   * 从点击位置把新主题「扩散」出去 —— 十几行代码换来一次观感拔群的切换，
   * 让一个本来纯功能性的按钮变成全站最顺手的小玩具。
   * 浏览器不支持（Firefox 旧版等）或系统开了「减少动态效果」时直接执行，
   * 不产生任何过渡伪元素。
   */
  function withBurst(x: number, y: number, mutate: () => void) {
    const doc = document as Document & {
      startViewTransition?: (cb: () => void) => { ready: Promise<void> }
    }
    const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    if (!doc.startViewTransition || reduced) {
      mutate()
      return
    }
    // 扩散半径取「点击点到最远角」：圆要盖满整个视口才收尾
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
      .catch(() => {
        // ready 被打断（用户快速连点）不算错：浏览器对新旧快照有兜底的交叉淡变
      })
  }

  watch(
    themeId,
    (v) => {
      try {
        localStorage.setItem(STORAGE_KEY, v)
      } catch {
        // 隐私模式下 localStorage 会抛异常。主题偏好存不下不影响使用，
        // 但不能让这个异常打断 apply —— 否则页面主题会停在切换前的状态。
      }
      apply(v)
      // 记住「这一族上次用的是哪套」，快捷按钮才有意义
      applyLastOfTone(v)
    },
    { immediate: true }
  )

  // 两个快捷记忆单独持久化：换主题不该把它们冲掉
  watch(lastLight, (v) => writeStoredChoice(LAST_LIGHT_KEY, v))
  watch(lastDark, (v) => writeStoredChoice(LAST_DARK_KEY, v))

  function applyLastOfTone(id: string) {
    if (themeById(id).tone === 'dark') lastDark.value = id
    else lastLight.value = id
  }

  return {
    themeId,
    tone,
    current,
    lastLight,
    lastDark,
    apply,
    setTheme,
    setTone,
    withBurst
  }
})

// readStoredTheme 读取并校验本地存储里的主题。
//
// 必须校验：localStorage 里的值可能是任意历史遗留内容（例如更早版本
// 存过 'auto'、手改的脏数据、装了新版本又被降级回去时留下的新 id），
// 而它只在登录页/主布局初始化时读一次 —— 取到非法值会让 apply() 把
// data-theme 设成一个没有对应样式块的锚点，页面直接退回无主题状态。
//
// 读取顺序（每一步都有具体来由，不要重排）：
//   1. 老档位键 + 主题键都读出来（老键顺手删掉：这个键以后再没有人写，
//      留着只会让人以为还有第三个轴）。
//   2. 老档位迁移成主题 oled —— 但**只在「上次停在深色」时**：
//      多主题之前档位与明暗是两个独立的键，「深空纯黑」只影响深色底，
//      所以用户完全可能选了 oled 之后再点灯泡切到浅色，此时档位键仍是 oled、
//      而主题键是 light。那种情况下他最后看到的是浅色，迁移成 oled 等于
//      在升级时替他换了一次主题。判据是主题键为 'dark' 或压根没有主题键。
//      这一段必须与 index.html 预涂脚本里的同一段判断保持逐字一致，
//      否则会出现「首帧一套、store 起来后另一套」的跳变。
//   3. 主题键。合法 id 才算数。
//   4. 跟随系统。这是用户对「深色模式」最普遍的预期，而且不引入第三个状态 ——
//      一旦用户手动切过，就以他自己的选择为准。
function readStoredTheme(): string {
  let legacy: string | null = null
  let stored: string | null = null
  try {
    legacy = localStorage.getItem(LEGACY_DARK_STYLE_KEY)
    if (legacy !== null) localStorage.removeItem(LEGACY_DARK_STYLE_KEY)
    stored = localStorage.getItem(STORAGE_KEY)
  } catch {
    // 隐私模式下读写都会抛：迁移做不了不影响下面继续判断
  }
  if (legacy === 'oled' && (stored === null || stored === 'dark')) return 'oled'
  if (isThemeId(stored)) return stored

  if (typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) {
    return DEFAULT_DARK
  }
  return DEFAULT_LIGHT
}
