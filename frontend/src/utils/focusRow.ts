// 行级操作（启停 / 删除）之后，把焦点还给「同一行、同一个动作」的按钮。
//
// ---- 为什么需要它 ----
//
// 渠道 / 密钥 / 代理三页的动作成功后都是**整表重拉**（ChannelsView 的删除与启停、
// KeysView 的启停与删除、ProxiesView 的删除）。antd 的 focusTriggerAfterClose
// 本想把焦点还给触发按钮，但那个按钮已经随旧行一起销毁了 —— 焦点于是掉回 body：
// 键盘用户要重新从页面顶部 Tab 一长串才能回到表格（列表越长代价越大），
// 读屏用户还额外失去了「我现在在哪一行」的上下文。
//
// ---- 怎么找回「同一行」----
//
// 行的身份是 antd 写在 <tr> 上的 data-row-key（各页都是记录的 id）。
// 动作按钮用 aria-label 的前缀认（形如「停用密钥 X」「编辑渠道 Y」）——
// 按 aria-label 而不是按列位置或图标：重排之后列位置会变，而 aria-label
// 是这些按钮本来就有的、稳定的动作名（图标按钮没有可见文字，它是唯一的名字）。
//
// 找不到那一行（多半就是刚被删掉的那一行）时，焦点落到表格容器上 ——
// 容器带 tabindex="-1"，焦点因此仍在「这一块」里，用户接着 Tab 就是表格内
// 的下一个控件，而不是从页面顶部重来。

/**
 * 只转义属性选择器里真正会出问题的两个字符。
 *
 * rowKey 是后端给的数字 id，但它是外部数据 —— 不假设它一定干净。
 * 不用 CSS.escape 是因为它在 happy-dom 里不一定存在，而这里要的转义规则
 * 比它简单得多（我们只把它放进双引号里）。
 */
export function escapeAttr(v: string): string {
  return v.replace(/\\/g, '\\\\').replace(/"/g, '\\"')
}

/** 「第 rowKey 行」的选择器 */
export function rowSelector(rowKey: string | number): string {
  return `tr[data-row-key="${escapeAttr(String(rowKey))}"]`
}

/** 「动作名以 labelPrefix 开头」的按钮选择器（前缀匹配，不是全等） */
export function actionSelector(labelPrefix: string): string {
  return `button[aria-label^="${escapeAttr(labelPrefix)}"]`
}

/**
 * 在 scope 里找回目标按钮并聚焦。
 *
 * @returns 是否找到了那一行的按钮（false 表示焦点落到了 scope 兜底）
 */
export function focusRowAction(
  scope: HTMLElement | null | undefined,
  rowKey: string | number,
  labelPrefix: string
): boolean {
  if (!scope) return false
  const btn = scope.querySelector<HTMLElement>(
    rowSelector(rowKey) + ' ' + actionSelector(labelPrefix)
  )
  if (btn) {
    btn.focus()
    return true
  }
  // 行不在了：焦点交给表格容器（它带 tabindex="-1"）。
  // 这不是「失败」—— 用户刚删掉一行，焦点回到列表这一块本身就是正确的落点。
  scope.focus()
  return false
}
