// antd 令牌映射（2026-09-30 立）。
//
// 这个映射是从「App.vue 里手抄 13 个色值」改成「运行时读 CSS 变量」的那一层。
// 它本身是纯函数 + 注入读取器，所以能在 happy-dom 里直接测 ——
// 真实读取（readCssVar）依赖计算样式，不属于单测范围，
// 由 check-contracts.mjs 静态断言「映射的令牌在 theme.css 里都有定义」。
import { describe, expect, it, vi } from 'vitest'
import { ANTD_VAR_MAP, buildAntdTokens, isDarkTone } from './antdTheme'

describe('antd 令牌映射', () => {
  it('按映射表逐个读取，键与令牌名一一对应', () => {
    const read = vi.fn((name: string) => `value-of${name}`)
    const tokens = buildAntdTokens(read)
    expect(Object.keys(tokens).sort()).toEqual(Object.keys(ANTD_VAR_MAP).sort())
    for (const [key, varName] of Object.entries(ANTD_VAR_MAP)) {
      expect(tokens[key as keyof typeof tokens]).toBe(`value-of${varName}`)
    }
    expect(read).toHaveBeenCalledTimes(Object.keys(ANTD_VAR_MAP).length)
  })

  it('主色取的是实心块那一对（不是 --color-primary），两者必须成对', () => {
    // 这条是防回退：主色铺底 + 白字的对比度只有 --solid-primary-* 那一对达标
    expect(ANTD_VAR_MAP.colorPrimary).toBe('--solid-primary-bg')
    expect(ANTD_VAR_MAP.colorTextLightSolid).toBe('--solid-primary-fg')
  })

  it('文字灰阶三档都显式映射（不映射就退回 antd 默认的 1.84:1 占位文字）', () => {
    expect(ANTD_VAR_MAP.colorTextSecondary).toBe('--color-text-secondary')
    expect(ANTD_VAR_MAP.colorTextPlaceholder).toBe('--color-text-placeholder')
    expect(ANTD_VAR_MAP.colorTextTertiary).toBe('--color-text-tertiary')
  })

  it('读不到值（空串）时原样透传，不编造兜底色值', () => {
    const tokens = buildAntdTokens(() => '')
    expect(Object.values(tokens).every((v) => v === '')).toBe(true)
  })

  it('明暗族判定', () => {
    expect(isDarkTone('dark')).toBe(true)
    expect(isDarkTone('light')).toBe(false)
  })
})
