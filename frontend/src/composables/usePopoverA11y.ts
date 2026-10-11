// 弹层的键盘可达性（2026-10-11）：打开时焦点进得去、Esc 关得掉、关闭后焦点还回来。
//
// ---- 为什么需要它 ----
//
// 全站有三个 antd 弹层（看板主题选择、侧栏版本面板、工具栏列设置），它们
// 都有同一个毛病，而且三处各缺一角：
//
//   · 焦点进不去：antd 把弹层内容 teleport 到 body 外层，触发按钮的焦点不会
//     跟着进去 —— 实测点开主题弹层后按 Tab，落点是弹层**外面**的「刷新」按钮。
//     纯键盘用户于是永远走不进弹层内容（版本面板更糟：它的内容在 DOM 末尾，
//     不主动聚焦的话 Tab 要绕整页才到）。
//   · Esc 关不掉：全仓除了移动端抽屉（MainLayout.onKeydown）之外没有任何
//     Esc 处理，而 antd 的 Popover 默认不响应 Esc。按了没反应，只能再点一次
//     触发按钮 —— 用户会以为弹层卡住了。
//   · 关闭后焦点掉到 body：弹层内容被移除时，里面的焦点节点一起消失，焦点
//     回到 body；下一次 Tab 从页面最顶上重新开始，用户丢失了位置。
//     （MainLayout 的 closeNav 早就是正确样板：关闭后把焦点还给汉堡按钮。）
//
// 三处都需要同样的三件事，所以抽成一个 composable 而不是抄三遍 ——
// 抄三遍的结果必然是「一处忘了还给焦点、一处没有 Esc」。
//
// ---- 为什么用 panel ref 而不是 document.querySelector ----
//
// 弹层内容是 teleport 出去的，但仍然可以用模板 ref 拿到根节点（Vue 会给
// teleport 的内容绑定 ref）。用 ref 而不是全局选择器，是因为页面上可能同时
// 存在别的 `.ant-popover`（antd 关闭后不销毁 DOM，只是隐藏）——
// 选择器会拿到第一个，也就是那个早就关掉的。
import { nextTick, onUnmounted, watch, type Ref } from 'vue'

/** 弹层内第一个可聚焦控件。与 useFormValidate 里的定位口径一致。 */
const FOCUSABLE =
  'button:not([disabled]), [href], input:not([type="hidden"]):not([disabled]),' +
  ' select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/**
 * 「能聚焦的东西」：既可以是 DOM 元素，也可以是 antd 的组件实例
 * （Button 用 expose 提供了 focus / blur，模板 ref 拿到的是实例而不是元素）。
 * 只要求有 focus，不要求是 HTMLElement —— 否则调用方得为每个 a-button 包一层 span。
 */
export type Focusable = { focus: () => void }

export function usePopoverA11y(opts: {
  /** 弹层开合状态（v-model:open 绑定的那个 ref） */
  open: Ref<boolean>
  /** 触发按钮的 ref：关闭后焦点要还给它 */
  trigger: Ref<Focusable | null>
  /** 弹层内容根节点的 ref（挂在 <template #content> 里的元素上） */
  panel: Ref<HTMLElement | null>
  /**
   * 打开后优先聚焦的选择器（在 panel 里查）。
   * 不传、查不到、或查到的东西**不可聚焦**时，退化为「第一个可聚焦控件」。
   */
  preferred?: string
}) {
  function onKeydown(e: KeyboardEvent) {
    if (e.key !== 'Escape') return
    // 只关自己这一层：事件目标必须落在本弹层里。
    // 不加这个判断的话，页面上同时开着两层弹层时按一次 Esc 会把两层一起关掉。
    const panel = opts.panel.value
    if (!panel) return
    if (e.target instanceof Node && panel.contains(e.target)) {
      opts.open.value = false
    }
  }

  watch(opts.open, (open) => {
    if (open) {
      document.addEventListener('keydown', onKeydown)
      // 内容是被 teleport 出来的：打开这一拍 DOM 里可能还没有它，
      // 所以轮询几拍而不是查一次（与 DashboardView 原来那段逻辑同一套）。
      // 12 × 50ms = 600ms 上限，够 antd 挂载；查不到就放弃（聚焦是增强，
      // 不该因为它让弹层打不开）。
      let tries = 0
      const focusIn = () => {
        const panel = opts.panel.value
        const preferred = opts.preferred
          ? panel?.querySelector<HTMLElement>(opts.preferred)
          : null
        const el = preferred ?? panel?.querySelector<HTMLElement>(FOCUSABLE) ?? null
        if (el) {
          el.focus()
          if (document.activeElement === el) return
          // preferred 是**偏好**，不是硬要求：它可能指向一个聚焦不了的东西
          // （disabled、或 0×0 且浏览器拒绝聚焦）。这时退到「第一个可聚焦控件」。
          //
          // 这不是防御性代码，是实测踩出来的：列设置传的 preferred 指向第一个
          // 勾选框，而日志表的第一列（请求时间）是**锁定列**、勾选框是 disabled ——
          // 焦点于是永远停在 body，连 Esc 都失效（事件目标不在弹层里，
          // onKeydown 的包含判断不成立），弹层只能靠再点一次触发按钮关掉。
          if (preferred) {
            const fallback = panel?.querySelector<HTMLElement>(FOCUSABLE) ?? null
            if (fallback && fallback !== preferred) {
              fallback.focus()
              if (document.activeElement === fallback) return
            }
          }
        }
        if (++tries < 12) setTimeout(focusIn, 50)
      }
      void nextTick(focusIn)
      return
    }

    document.removeEventListener('keydown', onKeydown)
    // 关闭后把焦点还给触发按钮 —— 但只在「焦点本来就在弹层里（或已经掉到
    // body）」时还。用户点了弹层外面别的东西而关闭时，焦点正落在那个东西上，
    // 这时抢回触发按钮等于把用户刚点的目标顶掉。
    const active = document.activeElement
    const orphaned =
      !active || active === document.body || (opts.panel.value?.contains(active) ?? false)
    if (orphaned) opts.trigger.value?.focus()
  })

  onUnmounted(() => document.removeEventListener('keydown', onKeydown))
}
