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

  /** 模型商在别处被改名后强制刷新 */
  async function reload() {
    loaded.value = false
    await ensure()
  }

  return { items, loaded, loading, ensure, byId, reload }
})
