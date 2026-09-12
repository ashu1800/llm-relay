import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { Provider } from '@/api/types'

// 模型商列表在多个页面都要用（模型管理、模型定价、渠道管理），
// 集中缓存一次，避免每个页面各拉一遍。
export const useProviderStore = defineStore('providers', () => {
  const items = ref<Provider[]>([])
  const loaded = ref(false)
  const loading = ref(false)

  async function ensure() {
    if (loaded.value || loading.value) return
    loading.value = true
    try {
      const res = await api.get<{ items: Provider[] }>('/models/providers')
      items.value = res.items || []
      loaded.value = true
    } catch {
      // 拿不到就让调用方显示兜底样式，不阻塞页面
      items.value = []
    } finally {
      loading.value = false
    }
  }

  function byId(id: number): Provider | undefined {
    return items.value.find((p) => p.id === id)
  }

  // 按一段文字认模型商：分组名、模板名里出现模型商的名字或 code 就算命中
  // （用户按模型商建分组："DeepSeek" / "OpenAI"）。
  // 只用来给表单挑一个默认值；认不出来就返回 undefined，由调用方落到「未指定」——
  // 绝不硬凑一个，硬凑出来的徽标显示的和实际不符，正是要修的那个问题。
  function matchByText(text: string): Provider | undefined {
    const s = (text || '').trim().toLowerCase()
    if (!s) return undefined
    return items.value.find(
      (p) => s.includes(p.name.toLowerCase()) || s.includes(p.code.toLowerCase())
    )
  }

  /** 模型商在别处被改名后强制刷新 */
  async function reload() {
    loaded.value = false
    await ensure()
  }

  return { items, loaded, loading, ensure, byId, matchByText, reload }
})
