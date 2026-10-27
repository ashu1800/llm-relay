// 登录认证的管理接口（向导 / 切换 / 改密 / 换绑）。
//
// 路径全在 /api/admin/auth 下，但权限来源不同：
//   - setup/*：认证方式未初始化时可达（要求「向导会话」= 用引导密钥
//     换来的有效会话）；
//   - switch/*、password/*、totp/*：要求有效会话，且部分动作要求
//     当前方式的凭据一并提交（后端的 requireMethod 中间件把关）。
//
// 所有成功的凭据变更都会重签会话 Cookie（后端完成），前端只需刷新
// 认证镜像即可，不需要重新登录。
import { api } from './client'
import type { AuthMethod } from '@/components/authSetup'

interface TOTPSetupResult {
  /** otpauth:// URI，交给二维码组件渲染 */
  otpauth_uri: string
  issuer: string
}

interface CredChangeResult {
  ok: boolean
  message: string
  method: AuthMethod
  expires_at: string
}

export const consoleAuthApi = {
  // ---- 设置向导（认证方式未初始化时）----

  /** 生成待绑定的 2FA 密钥（可重复调用，以最后一次为准） */
  setupTotpInit: () => api.post<TOTPSetupResult>('/auth/setup/totp/init', {}),

  /** 用认证器上的码确认绑定，完成后 2FA 即生效、引导密钥退役 */
  setupTotpConfirm: (code: string) => api.post<CredChangeResult>('/auth/setup/totp/confirm', { code }),

  /** 设置管理密钥（手动填写，不再自动生成），完成后密钥登录生效 */
  setupKey: (key: string) => api.post<CredChangeResult>('/auth/setup/key', { key }),

  // ---- 认证方式切换 ----

  /** 密钥 → 2FA：生成二维码（已是登录态，无需再验密钥） */
  switchToTotpInit: () => api.post<TOTPSetupResult>('/auth/switch/to-totp/init', {}),

  /** 密钥 → 2FA：验新码完成切换，旧密钥即刻失效 */
  switchToTotpConfirm: (code: string) =>
    api.post<CredChangeResult>('/auth/switch/to-totp/confirm', { code }),

  /** 2FA → 密钥：当前动态码 + 新密钥一次提交 */
  switchToKey: (currentCode: string, newKey: string) =>
    api.post<CredChangeResult>('/auth/switch/to-key', {
      current_code: currentCode,
      new_key: newKey
    }),

  // ---- 密钥改密 ----

  changeKey: (currentKey: string, newKey: string) =>
    api.post<CredChangeResult>('/auth/password/change', {
      current_key: currentKey,
      new_key: newKey
    }),

  // ---- 2FA 换绑 ----

  /** 换绑第一步：验当前码，返回新密钥的二维码 */
  rebindTOTPInit: (currentCode: string) =>
    api.post<TOTPSetupResult>('/auth/totp/rebind/init', { current_code: currentCode }),

  /** 换绑第二步：验新码完成换绑，旧设备即刻失效 */
  rebindTOTPConfirm: (code: string) =>
    api.post<CredChangeResult>('/auth/totp/rebind/confirm', { code })
}
