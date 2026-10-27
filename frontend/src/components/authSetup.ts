// 认证相关的界面判据（纯函数，规则 10：判定逻辑不写在组件里）。
//
// 这些函数决定「用户现在看到哪一屏」「这个输入能不能提交」「错误怎么
// 说人话」，全部可以脱离 DOM 测。组件只负责把它们接到模板上。

/** 认证方式，与后端 consoleauth.Method 同值域。 */
export type AuthMethod = 'unset' | 'totp' | 'key'

/** /auth/status 的响应形状（前端只认这几个字段）。 */
export interface AuthStatus {
  enabled: boolean
  authenticated: boolean
  method: AuthMethod
  needs_setup: boolean
  session_ttl_hours?: number
}

/** 登录页当前应展示的形态。 */
export type LoginPane = 'wizard' | 'key' | 'totp' | 'disabled'

/**
 * 决定登录页形态。
 *
 * 优先级：
 *   1. needs_setup → 向导（引导密钥登录成功后，同一页面接着让用户选方式）
 *   2. totp → 动态码输入
 *   3. key → 密钥输入
 *   4. 其余（鉴权关闭的旧部署）→ 不显示表单（守卫已放行，正常进不去这一屏）
 *
 * 注意 needs_setup 与 authenticated 的关系：setpending 状态下未登录时
 * 先显示引导密钥输入（属于 wizard 形态的第一阶段），登录成功后同一
 * 形态进入第二阶段（选方式 / 扫码）。两阶段共用一个 pane 是有意的 ——
 * 对用户来说这就是「第一次设置」这一件事。
 */
export function loginPane(s: AuthStatus | null): LoginPane {
  if (!s) return 'disabled'
  if (s.needs_setup) return 'wizard'
  if (!s.enabled) return 'disabled'
  return s.method === 'totp' ? 'totp' : 'key'
}

/** 向导内的阶段：输入引导密钥 → 选择方式 → 绑定/设置完成。 */
export type WizardStage = 'bootstrap' | 'choose' | 'totp' | 'key'

/** 决定向导阶段。hasBootstrap 由登录成功后的 authenticated 决定。 */
export function wizardStage(authenticated: boolean, chosen: 'totp' | 'key' | null): WizardStage {
  if (!authenticated) return 'bootstrap'
  if (chosen === 'totp') return 'totp'
  if (chosen === 'key') return 'key'
  return 'choose'
}

/** 动态码输入是否合法（6 位数字）。 */
export function isCompleteCode(code: string): boolean {
  return /^\d{6}$/.test(code.trim())
}

/**
 * 只保留数字并截断到 6 位 —— 用于输入框的实时清洗（粘贴带空格也能用）。
 *
 * 别在输入框上再加 `maxlength="6"`：浏览器在 input 事件**之前**就截断原始
 * 字符串，粘贴 "123 456" 会先变成 "123 45"，再清洗得到 "12345" ——
 * 第 6 位数字被丢掉，用户拿到一个永远验证不过的码且界面不报错。
 * 截断由本函数负责（末尾那个 slice），它看到的是完整原文。
 */
export function sanitizeCode(raw: string): string {
  return raw.replace(/\D/g, '').slice(0, 6)
}

/** 新密钥的强度问题；返回空串表示通过。 */
export function keyStrengthError(key: string): string {
  const trimmed = key.trim()
  if (!trimmed) return '密钥不能为空'
  // 占位值检查放在长度之前：CHANGE_ME 只有 9 位，先报「太短」会把
  // 「你还在用示例值」这个更要紧的信息盖住。
  if (key === 'CHANGE_ME') return '不能使用占位值 CHANGE_ME'
  if (key.length < 16) return '密钥至少 16 位（公网部署会被暴力破解）'
  return ''
}

/** 两次输入必须一致；返回空串表示通过。 */
export function confirmKeyError(key: string, confirm: string): string {
  if (!confirm) return '请再输入一次新密钥'
  if (key !== confirm) return '两次输入的密钥不一致'
  return ''
}

/** 认证方式的中文名。 */
export function methodLabel(m: AuthMethod): string {
  switch (m) {
    case 'totp':
      return '动态码（2FA）'
    case 'key':
      return '管理密钥'
    default:
      return '未设置'
  }
}

/**
 * 设置页「登录认证」面板当前可做的操作。
 *
 * unset 时不能在这里初始化：初始化必须从登录页走（需要引导密钥登录，
 * 且这是「第一次」的语义）。设置页只给指路文案。
 */
export interface AuthActions {
  canChangeKey: boolean
  canSwitchToTotp: boolean
  canRebindTotp: boolean
  canSwitchToKey: boolean
}

export function authActions(m: AuthMethod): AuthActions {
  return {
    canChangeKey: m === 'key',
    canSwitchToTotp: m === 'key',
    canRebindTotp: m === 'totp',
    canSwitchToKey: m === 'totp',
  }
}
