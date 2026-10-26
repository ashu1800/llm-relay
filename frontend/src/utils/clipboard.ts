// 写剪贴板：优先用 clipboard API，失败时退回临时 textarea + execCommand。
//
// 两条退路缺一不可：
//  1. 用 http 且不是 localhost 访问时 clipboard API 直接不可用（抛错）；
//  2. 浏览器可能把 writeText 挂起等用户授权 —— 那是**既不成功也不失败**的状态，
//     实测点击后界面毫无反应。所以给它 800ms 的上限，超时就走退路。
//
// 从 KeysView 提取为公共工具：日志详情复制 Trace ID 与复制密钥是同一个诉求
// （把一段必须精确的字符串送进剪贴板），降级路径也应该只有一份。
export async function writeClipboard(text: string): Promise<boolean> {
  try {
    const ok = await Promise.race([
      navigator.clipboard.writeText(text).then(() => true),
      new Promise<boolean>((resolve) => setTimeout(() => resolve(false), 800))
    ])
    if (ok) return true
  } catch {
    // 落到下面的退路
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  try {
    return document.execCommand('copy')
  } finally {
    document.body.removeChild(ta)
  }
}

// 读剪贴板：优先用 clipboard API，取不到时如实返回失败，由调用方决定退路。
//
// ---- 为什么它和 writeClipboard 不是一个待遇 ----
//
// writeText 在「页面被用户手势触发」的前提下基本都能用；readText 不是。
// 读剪贴板等于读用户的整个剪贴板，浏览器把它当成敏感能力：
//   - 必须在**安全上下文**（https 或 localhost）里；
//   - 必须在用户手势里调用，且多数浏览器会弹一次授权提示；
//   - Firefox 默认把 dom.events.asyncClipboard.readText 设为 false，
//     直接抛 NotAllowedError；
//   - Safari 在非直接手势路径上会拒绝。
//
// 而且**没有可靠的降级路径**：execCommand('paste') 在主流浏览器里被禁用
// 于网页（只有扩展能调用），临时 textarea + execCommand 那一套只能写不能读。
//
// 所以这里不做「假装能读」的尝试（比如抢焦点到隐藏 textarea 再调
// execCommand('paste')）—— 那在 Chrome 里会静默失败，写出来只会让调用方
// 以为读取成功、拿到空串。失败就 return null，让调用方去问用户要。
//
// 返回值用 string | null 而不是 ''：空剪贴板和读取失败是两件事，
// 调用方对二者的处置不同（前者提示「剪贴板是空的」，后者提示「读不到」）。
export async function readClipboard(): Promise<string | null> {
  try {
    // 与 writeClipboard 同一条超时思路：writeText 可能挂在「等用户授权」上，
    // readText 同样会 —— 那是既不成功也不失败的状态，界面看起来像点了没反应。
    // 2 秒比写入的 800ms 宽：授权弹窗是真的要用户点一下的，
    // 800ms 会把「用户正在看提示」误判成失败。
    const text = await Promise.race([
      navigator.clipboard.readText(),
      new Promise<null>((resolve) => setTimeout(() => resolve(null), 2000))
    ])
    return typeof text === 'string' ? text : null
  } catch {
    // 不可用（http、无权限、Firefox 默认关闭、用户拒绝）
    return null
  }
}
