// 客户端显示映射（对照表）的共享状态。
//
// 模块级单例（与 logColumnVisibility 同一手法）：请求日志面板与设置页的
// 映射管理面板都要用这份映射 —— 面板里刚保存的别名，日志列立刻按它显示，
// 而不是等下一次进页面。加载去重：两个组件同时 onMounted 只发一次请求。
import { computed, ref } from 'vue'
import { clientAliasApi } from '@/api/clientAliases'
import type { ClientAlias } from '@/api/types'

const aliases = ref<ClientAlias[]>([])
const loading = ref(false)
let loadedOnce = false
let inflight: Promise<void> | null = null

async function fetchAll() {
  loading.value = true
  try {
    const res = await clientAliasApi.list()
    aliases.value = res.items ?? []
    loadedOnce = true
  } catch (err) {
    // 加载失败不打断宿主：日志列表照常显示（解析层自动回落内置/快照），
    // loadedOnce 不置位，下一次 load 还会重试
    console.warn('加载客户端显示映射失败', err)
  } finally {
    loading.value = false
    inflight = null
  }
}

export function useClientAliases() {
  /** 拉取映射表。已加载过且非强制时只复用内存副本；
   *  并发调用共享同一个在途请求（去重，不是竞态各自打一发）。 */
  async function load(force = false) {
    if (!force && loadedOnce) return
    if (inflight) return inflight
    inflight = fetchAll()
    return inflight
  }

  /** key → 行 的查找索引（解析层 clientDisplay 消费） */
  const byKey = computed(() => new Map(aliases.value.map((a) => [a.key, a])))

  /** 待设置行数（面板头部的计数徽标） */
  const pendingCount = computed(() => aliases.value.filter((a) => !a.name).length)

  return { aliases, byKey, pendingCount, loading, load }
}
