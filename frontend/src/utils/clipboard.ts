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
