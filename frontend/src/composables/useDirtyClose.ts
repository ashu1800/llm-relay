// 表单弹窗的「防误关」：遮罩不再能关掉弹窗，且关闭前若表单有改动会先问一句。
//
// ---- 为什么需要它（2026-10-10 交互打磨）----
//
// 全站七个弹窗此前都用 antd 的默认行为：maskClosable = true。渠道表单有十来段
// 字段（名称 / 分组 / 权重 / 币种 / 协议 / 地址 / 密钥 / 代理 / 图标 / 白名单 …），
// 填到一半手滑点到遮罩上，弹窗直接关掉、内容一个字节都不留 —— 而且没有任何
// 提示，用户往往要等下一次打开才发现「刚才填的没了」。
//
// 这里做两件事，缺一不可：
//   1. 遮罩点击不再关闭（调用方加 :mask-closable="false"）—— 遮罩是最容易
//      误触的那一个关闭路径，而它恰恰是**没有任何确认成本**的那个；
//   2. 有改动时，X 按钮 / Esc 关闭要先确认（guardCancel）。
//
// ---- 为什么是「先按住再问」----
//
// antd 的 Modal 没有 beforeClose 钩子：v-model:open 会先被写成 false，
// 随后才 emit('cancel')。所以 guardCancel 里先把 open 写回 true，再弹确认 ——
// 这两次写发生在**同一个同步执行**里，Vue 的渲染副作用排在微任务队列上，
// 看到的只有最终值 true，因此不会闪一帧关闭态（这一条由
// .shots/verify-dirty-close.mjs 逐帧采样钉住）。
//
// 确认后走 closeNow()：复位基线、关闭弹窗、把表单恢复成干净值。
import { onBeforeUnmount, ref, type Ref } from 'vue'
import { modal } from '@/utils/feedback'

export function useDirtyClose(opts: {
  /** 弹窗的 open 状态（v-model:open 绑定的那个 ref） */
  open: Ref<boolean>
  /** 取表单当前值的快照，只要能比较相等即可（JSON.stringify(form) 就够） */
  snapshot: () => string
  /** 确认放弃（或本来就没改动）之后：把表单恢复成干净状态 */
  onDiscard: () => void
  /** 确认弹窗的标题 */
  title?: string
}) {
  // null 表示「还没有基线」（弹窗没打开过）—— 此时一律当作不脏，
  // 避免组件初始化阶段的一次误判把用户拦在弹窗外
  const baseline = ref<string | null>(null)

  /** 打开弹窗、数据载入完成之后调用：此刻的值就是「没改过」的基准 */
  function markClean() {
    baseline.value = opts.snapshot()
  }

  function isDirty() {
    return baseline.value !== null && opts.snapshot() !== baseline.value
  }

  function closeNow() {
    baseline.value = null
    opts.open.value = false
    opts.onDiscard()
  }

  /**
   * 绑定到 a-modal / a-drawer 的 @cancel / @close。
   * 没改动就直接关，有改动先确认。
   */
  function guardCancel() {
    if (!isDirty()) {
      closeNow()
      return
    }
    // 按住：见文件顶部「为什么是先按住再问」
    opts.open.value = true
    modal.confirm({
      title: opts.title ?? '放弃未保存的改动？',
      content: '这个弹窗里的改动还没有保存，关闭后不会保留。',
      okText: '放弃改动',
      cancelText: '继续编辑',
      okButtonProps: { danger: true },
      centered: true,
      onOk() {
        closeNow()
      }
      // onCancel 什么都不做：open 已经是 true，用户留在原处继续编辑
    })
  }

  /**
   * 刷新 / 关标签页也拦住（2026-10-11）。
   *
   * 上面那些只覆盖**弹窗内**的关闭路径。用户填了十个字段之后直接按 F5 或
   * 关掉标签页，浏览器不问、页面也不问 —— 内容一个字节都不留。
   * 那是同一个损失的另一半，而它的触发概率并不低（改完想刷新确认一下、
   * 或者顺手关掉一个「已经不需要」的标签页）。
   *
   * 两条浏览器限制要说清：
   *   · 文案不能自定义，只能让浏览器弹它自己那句「离开此网站？」；
   *   · Chrome 要求页面**有过用户交互**才真的弹，所以「打开看一眼就走」
   *     不会被拦（那也正是我们想要的行为）。
   *
   * 判定直接用 isDirty() 而不是维护一个额外的 ref：它本来就是「现在和基线比」，
   * 而 beforeunload 在真的要离开时只触发一次 —— 那一刻算一次就够了，
   * 不必（也不该）在每次输入时都算一遍。
   */
  function onBeforeUnload(e: BeforeUnloadEvent) {
    if (!isDirty()) return
    e.preventDefault()
    // returnValue 是旧 API，但 Chrome 仍要求写它才弹确认框
    e.returnValue = ''
  }

  window.addEventListener('beforeunload', onBeforeUnload)
  onBeforeUnmount(() => window.removeEventListener('beforeunload', onBeforeUnload))

  return { markClean, guardCancel }
}
