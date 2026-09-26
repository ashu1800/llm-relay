import { onUnmounted, ref } from 'vue'

/**
 * 复制成功的「原位变形」反馈状态（2026-09-27 感官升级）。
 *
 * 复制成功后不再只弹一条顶部 message —— 那是打断视线的全局通知，
 * 而复制是发生在**这一个胶囊/按钮**上的事。调用方在成功分支里
 * markCopied(槽位键)，模板据 copiedKey 把复制图标换成对勾，
 * timeout 后自动复原。同一槽位连点会重新计时（复制多把密钥时
 * 每一把各自亮，互不抢占）。
 *
 * 失败路径不走这里：失败仍用 message.warning —— 「没复制上」必须
 * 比成功更显眼，不能悄悄复原成没发生过的样子。
 */
export function useCopyFeedback(timeout = 900) {
  const copiedKey = ref<string | null>(null)
  let timer: number | null = null

  function markCopied(key: string) {
    copiedKey.value = key
    if (timer !== null) window.clearTimeout(timer)
    timer = window.setTimeout(() => {
      copiedKey.value = null
    }, timeout)
  }

  onUnmounted(() => {
    if (timer !== null) window.clearTimeout(timer)
  })

  return { copiedKey, markCopied }
}
