import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import type { AuthMethod, AuthStatus } from '@/components/authSetup'

// 登录认证状态（无账号模型，认证方式二选一）。
//
// 真相始终在服务端（/auth/status 与每个接口的 401）；这里只维护镜像：
//   - router 守卫：未登录跳登录页（fetchStatus 的结果）；
//   - api/client 的 401 处理：会话过期时 markUnauthenticated + 跳登录；
//   - useLive：未登录时不再空转重连 WebSocket。
//
// method / needsSetup 决定登录页形态：unset=先引导登录再走向导，
// key=密钥输入，totp=6 位动态码。
export const useAuthStore = defineStore('auth', () => {
  const enabled = ref(false)
  const authenticated = ref(false)
  const method = ref<AuthMethod>('unset')
  const needsSetup = ref(false)
  // statusLoaded 为 false 时守卫会去请求一次 /auth/status；
  // 失败也置为 true —— 后端没起来时每个路由都重试一遍只会拖慢导航，
  // 真正的会话失效由各接口的 401 统一兜底。
  const statusLoaded = ref(false)

  function applyStatus(res: AuthStatus) {
    enabled.value = res.enabled
    authenticated.value = res.authenticated
    method.value = res.method
    needsSetup.value = res.needs_setup
  }

  /** 查询一次登录状态（守卫首次导航时调用）。 */
  async function fetchStatus() {
    try {
      applyStatus(await api.get<AuthStatus>('/auth/status'))
    } finally {
      statusLoaded.value = true
    }
  }

  /** 用管理密钥登录（unset 模式下就是引导密钥）。 */
  async function loginKey(key: string) {
    const res = await api.post<{ method: AuthMethod }>('/auth/login', { key })
    enabled.value = true
    authenticated.value = true
    method.value = res.method
    needsSetup.value = res.method === 'unset'
  }

  /** 用 6 位动态码登录（2FA 模式）。 */
  async function loginTotp(code: string) {
    const res = await api.post<{ method: AuthMethod }>('/auth/login', { code })
    enabled.value = true
    authenticated.value = true
    method.value = res.method
    needsSetup.value = false
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

  /** 认证方式变更（向导/切换/改密/换绑成功后调用，刷新镜像）。 */
  function setMethod(m: AuthMethod) {
    method.value = m
    needsSetup.value = m === 'unset'
    authenticated.value = true
  }

  return {
    enabled,
    authenticated,
    method,
    needsSetup,
    statusLoaded,
    fetchStatus,
    loginKey,
    loginTotp,
    logout,
    markUnauthenticated,
    setMethod
  }
})
