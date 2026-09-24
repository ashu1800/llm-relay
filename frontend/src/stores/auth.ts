import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'

// 登录认证状态（无账号模型）。
//
// 全站只有一把「管理密钥」，登录成功后会话在 HttpOnly Cookie 里，
// 前端不持有、也无法读取任何凭据 —— 这里只维护「当前是否已登录」的
// 镜像状态，真相始终在服务端（/auth/status 与每个接口的 401）。
//
// 三个消费方：
//   - router 守卫：未登录跳登录页（fetchStatus 的结果）；
//   - api/client 的 401 处理：会话过期时 markUnauthenticated + 跳登录；
//   - useLive：未登录时不再空转重连 WebSocket。
export const useAuthStore = defineStore('auth', () => {
  const enabled = ref(false)
  const authenticated = ref(false)
  // statusLoaded 为 false 时守卫会去请求一次 /auth/status；
  // 失败也置为 true —— 后端没起来时每个路由都重试一遍只会拖慢导航，
  // 真正的会话失效由各接口的 401 统一兜底。
  const statusLoaded = ref(false)

  /** 查询一次登录状态（守卫首次导航时调用）。 */
  async function fetchStatus() {
    try {
      const res = await api.get<{ enabled: boolean; authenticated: boolean }>('/auth/status')
      enabled.value = res.enabled
      authenticated.value = res.authenticated
    } finally {
      statusLoaded.value = true
    }
  }

  /** 用管理密钥登录。成功后会话由后端写进 HttpOnly Cookie。 */
  async function login(key: string) {
    await api.post('/auth/login', { key })
    enabled.value = true
    authenticated.value = true
  }

  /** 退出登录：后端清 Cookie，本地清镜像状态。 */
  async function logout() {
    try {
      await api.post('/auth/logout', {})
    } finally {
      authenticated.value = false
    }
  }

  /** 会话失效（接口 401）时由 client.ts 调用，只清本地状态不改服务端。 */
  function markUnauthenticated() {
    authenticated.value = false
  }

  return { enabled, authenticated, statusLoaded, fetchStatus, login, logout, markUnauthenticated }
})
