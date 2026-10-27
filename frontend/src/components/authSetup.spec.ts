import { describe, expect, it } from 'vitest'
import {
  authActions,
  confirmKeyError,
  isCompleteCode,
  keyStrengthError,
  loginPane,
  methodLabel,
  sanitizeCode,
  wizardStage,
  type AuthStatus
} from './authSetup'

function status(partial: Partial<AuthStatus>): AuthStatus {
  return { enabled: true, authenticated: false, method: 'key', needs_setup: false, ...partial }
}

describe('loginPane', () => {
  it('未完成初始化优先走向导（即使未登录）', () => {
    expect(loginPane(status({ needs_setup: true, method: 'unset' }))).toBe('wizard')
  })
  it('已初始化：totp 给动态码、key 给密钥', () => {
    expect(loginPane(status({ method: 'totp' }))).toBe('totp')
    expect(loginPane(status({ method: 'key' }))).toBe('key')
  })
  it('鉴权关闭的旧部署不显示表单', () => {
    expect(loginPane(status({ enabled: false, method: 'unset' }))).toBe('disabled')
  })
  it('状态未加载时按 disabled 处理（避免闪现错误的表单）', () => {
    expect(loginPane(null)).toBe('disabled')
  })
})

describe('wizardStage', () => {
  it('未登录时停在引导密钥阶段', () => {
    expect(wizardStage(false, 'totp')).toBe('bootstrap')
    expect(wizardStage(false, null)).toBe('bootstrap')
  })
  it('登录后先选择方式，再进入对应绑定流程', () => {
    expect(wizardStage(true, null)).toBe('choose')
    expect(wizardStage(true, 'totp')).toBe('totp')
    expect(wizardStage(true, 'key')).toBe('key')
  })
})

describe('动态码判据', () => {
  it('只认 6 位数字', () => {
    expect(isCompleteCode('123456')).toBe(true)
    expect(isCompleteCode(' 123456 ')).toBe(true)
    expect(isCompleteCode('12345')).toBe(false)
    expect(isCompleteCode('1234567')).toBe(false)
    expect(isCompleteCode('12a456')).toBe(false)
    expect(isCompleteCode('')).toBe(false)
  })
  it('输入清洗：去非数字、截断 6 位', () => {
    expect(sanitizeCode('12 34 56')).toBe('123456')
    expect(sanitizeCode('abc123def456ghi789')).toBe('123456')
    expect(sanitizeCode('12345')).toBe('12345')
  })

  it('粘贴带空格的完整码能还原（加回 maxlength 会破坏这条）', () => {
    // 回归防线：输入框上一旦有人加回 maxlength="6"，浏览器会先把
    // "123 456" 截成 "123 45"，sanitizeCode 再怎么清洗也只剩 5 位 ——
    // 用户从认证器复制的码永远验不过且界面不报错。
    // 单测拿不到 DOM 的 maxlength 行为，所以这里钉住「原文完整时能还原」，
    // 那个属性本身由 check-contracts.mjs 静态禁掉。
    expect(sanitizeCode('123 456')).toBe('123456')
    expect(sanitizeCode(' 123456 ')).toBe('123456')
    expect(sanitizeCode('123-456')).toBe('123456')
  })
})

describe('密钥强度', () => {
  it('少于 16 位拒绝，占位值拒绝', () => {
    expect(keyStrengthError('')).toBe('密钥不能为空')
    expect(keyStrengthError('   ')).toBe('密钥不能为空')
    expect(keyStrengthError('short-key')).toContain('16')
    expect(keyStrengthError('CHANGE_ME')).toContain('CHANGE_ME')
    expect(keyStrengthError('0123456789abcdef')).toBe('')
  })
  it('两次输入必须一致', () => {
    expect(confirmKeyError('0123456789abcdef', '')).toBe('请再输入一次新密钥')
    expect(confirmKeyError('0123456789abcdef', '0123456789abcdee')).toContain('不一致')
    expect(confirmKeyError('0123456789abcdef', '0123456789abcdef')).toBe('')
  })
})

describe('认证方式与操作矩阵', () => {
  it('密钥模式可改密、可切 2FA，不能换绑/切回', () => {
    const a = authActions('key')
    expect(a).toEqual({ canChangeKey: true, canSwitchToTotp: true, canRebindTotp: false, canSwitchToKey: false })
  })
  it('2FA 模式可换绑、可切密钥，不能改密/再切 2FA', () => {
    const a = authActions('totp')
    expect(a).toEqual({ canChangeKey: false, canSwitchToTotp: false, canRebindTotp: true, canSwitchToKey: true })
  })
  it('未初始化没有任何操作入口（必须回登录页走向导）', () => {
    const a = authActions('unset')
    expect(Object.values(a).every((v) => v === false)).toBe(true)
  })
  it('方式名可读', () => {
    expect(methodLabel('totp')).toContain('2FA')
    expect(methodLabel('key')).toContain('密钥')
    expect(methodLabel('unset')).toContain('未')
  })
})
