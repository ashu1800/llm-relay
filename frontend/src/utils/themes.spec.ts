// 主题注册表的自洽性（2026-09-30 随 12 套主题一起立）。
//
// 注册表是「哪些主题存在」的唯一真源（CSS 块、预涂清单、光标资源都跟着它走）；
// 它自己写错的话，check-contracts.mjs 那几条比对会全线报错，但报出来的现象
// 很难直接指回原因。这里把注册表自己的不变量钉住，让失败信息一眼可读。
import { describe, expect, it } from 'vitest'
import {
  DARK_IDS,
  DEFAULT_DARK,
  DEFAULT_LIGHT,
  LIGHT_IDS,
  THEMES,
  THEME_IDS,
  isThemeId,
  themeById
} from './themes'

describe('注册表不变量', () => {
  it('id 唯一', () => {
    expect(new Set(THEME_IDS).size).toBe(THEMES.length)
  })

  it('两种明暗族都存在，且默认主题落在各自族里', () => {
    expect(LIGHT_IDS.length).toBeGreaterThan(0)
    expect(DARK_IDS.length).toBeGreaterThan(0)
    expect(LIGHT_IDS).toContain(DEFAULT_LIGHT)
    expect(DARK_IDS).toContain(DEFAULT_DARK)
  })

  it('每项的名字、上游、说明、出处都非空', () => {
    for (const t of THEMES) {
      expect(t.id).toMatch(/^[a-z][a-z0-9-]*$/)
      expect(t.name.length).toBeGreaterThan(0)
      expect(t.upstream.length).toBeGreaterThan(0)
      expect(t.desc.length).toBeGreaterThan(0)
      expect(t.sourceUrl).toMatch(/^https?:\/\//)
    }
  })

  it('外壳色是 6 位十六进制（契约脚本会拿它跟 CSS 逐字比）', () => {
    for (const t of THEMES) {
      expect(t.shell).toMatch(/^#[0-9a-f]{6}$/)
    }
  })

  it('cursorSource 只指向存在的主题，且不指向自己', () => {
    for (const t of THEMES) {
      if (!t.cursorSource) continue
      expect(THEME_IDS).toContain(t.cursorSource)
      expect(t.cursorSource).not.toBe(t.id)
    }
  })

  it('明暗分组与 tone 字段一致', () => {
    for (const t of THEMES) {
      expect(t.tone === 'light' ? LIGHT_IDS : DARK_IDS).toContain(t.id)
    }
  })
})

describe('查询与校验', () => {
  it('isThemeId 只认注册表里的字符串', () => {
    expect(isThemeId('nord')).toBe(true)
    expect(isThemeId('auto')).toBe(false)
    expect(isThemeId('')).toBe(false)
    expect(isThemeId(null)).toBe(false)
    expect(isThemeId(undefined)).toBe(false)
    expect(isThemeId(42)).toBe(false)
  })

  it('themeById 查不到时回落第一项而不是抛异常（主题取不到不该白屏）', () => {
    expect(themeById('dracula').id).toBe('dracula')
    expect(themeById('nope').id).toBe(THEMES[0].id)
  })
})
