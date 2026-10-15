// 显示解析层的优先级矩阵与 key 口径。这些坏掉的表现都是「列上显示错」
// 而不是报错（映射不生效、图标空白、CSV 与界面两个名字），所以逐条钉住。
import { describe, expect, it } from 'vitest'
import { clientKey, isRemoteIcon, resolveClientIcon, resolveClientLabel } from './clientDisplay'
import type { ClientAlias } from '@/api/types'

function aliasMap(rows: Partial<ClientAlias>[]): Map<string, ClientAlias> {
  return new Map(
    rows.map((r) => [
      r.key ?? '',
      { id: 1, key: r.key ?? '', name: r.name ?? '', icon: r.icon ?? '', created_at: '', updated_at: '', ...r } as ClientAlias
    ])
  )
}

describe('clientKey（三处同口径的前端版）', () => {
  it('id 非空取 id，否则取 name token，全空为空串', () => {
    expect(clientKey('claude-code', 'Claude Code')).toBe('claude-code')
    expect(clientKey('', 'deepseek-harness')).toBe('deepseek-harness')
    expect(clientKey('', '')).toBe('')
  })
})

describe('resolveClientLabel（显示名优先级）', () => {
  it('映射名压过内置叫法与快照名 —— 这是对照表存在的意义', () => {
    const m = aliasMap([{ key: 'deepseek-harness', name: 'DSH' }])
    expect(resolveClientLabel('', 'deepseek-harness', m)).toBe('DSH')
  })

  it('映射也能改写内置客户端的叫法（key 命中 id）', () => {
    const m = aliasMap([{ key: 'claude-code', name: 'CC' }])
    expect(resolveClientLabel('claude-code', 'Claude Code', m)).toBe('CC')
  })

  it('待设置的映射行（name 空）不参与，回落内置叫法', () => {
    const m = aliasMap([{ key: 'claude-code', name: '' }])
    expect(resolveClientLabel('claude-code', 'Claude Code', m)).toBe('Claude Code')
  })

  it('没有映射时：内置叫法 > 快照名 > 空串', () => {
    const m = aliasMap([])
    expect(resolveClientLabel('curl', 'curl', m)).toBe('curl')
    expect(resolveClientLabel('claude-code', 'Claude Code', m)).toBe('Claude Code')
    expect(resolveClientLabel('', 'my-tool', m)).toBe('my-tool')
    expect(resolveClientLabel('', '', m)).toBe('')
  })
})

describe('resolveClientIcon（图标形态识别）', () => {
  it('映射 icon 为 data URI / URL → img', () => {
    const m = aliasMap([{ key: 'k', icon: 'data:image/png;base64,xxx' }])
    expect(resolveClientIcon('', 'k', m)).toEqual({ kind: 'img', src: 'data:image/png;base64,xxx' })
    const m2 = aliasMap([{ key: 'k', icon: 'https://a.com/x.png' }])
    expect(resolveClientIcon('', 'k', m2)).toEqual({ kind: 'img', src: 'https://a.com/x.png' })
  })

  it('一两个字符 → 文字/emoji（与渠道图标同一约定）', () => {
    const m = aliasMap([{ key: 'k', icon: '🤖' }])
    expect(resolveClientIcon('', 'k', m)).toEqual({ kind: 'text', ch: '🤖' })
  })

  it('内置文件名 → mask；拼错的文件名回落默认链路而不是空白 mask', () => {
    const m = aliasMap([{ key: 'deepseek-harness', icon: 'deepseek.svg' }])
    expect(resolveClientIcon('', 'deepseek-harness', m)).toEqual({ kind: 'builtin', file: 'deepseek.svg' })
    const bad = aliasMap([{ key: 'k', icon: 'no-such-file.svg' }])
    expect(resolveClientIcon('', 'k', bad)).toEqual({ kind: 'builtin', file: 'default.svg' })
  })

  it('映射 icon 空（待设置）回落内置品牌图标', () => {
    const m = aliasMap([{ key: 'claude-code', icon: '' }])
    expect(resolveClientIcon('claude-code', 'Claude Code', m)).toEqual({ kind: 'builtin', file: 'claude-code.svg' })
  })

  it('无映射时维持原有两级：CLIENT_META → default', () => {
    const m = aliasMap([])
    expect(resolveClientIcon('zcode', 'ZCode', m)).toEqual({ kind: 'builtin', file: 'default.svg' })
    expect(resolveClientIcon('codex', 'Codex CLI', m)).toEqual({ kind: 'builtin', file: 'codex.svg' })
    expect(resolveClientIcon('', '', m)).toEqual({ kind: 'builtin', file: 'default.svg' })
  })
})

// 判「这条图标还是外链、没缓存」的唯一口径：设置面板的未缓存标记与
// 「保存」是否保持可点（= 再点一次就是重试抓取）都读它，
// 判宽了会把 data URI 也标成外链，判窄了则永远标不出来。
describe('isRemoteIcon（图片外链判定）', () => {
  it('http(s) 地址算外链，大小写与首尾空白不敏感', () => {
    expect(isRemoteIcon('https://a.com/x.png')).toBe(true)
    expect(isRemoteIcon('http://a.com/x.png')).toBe(true)
    expect(isRemoteIcon('  HTTPS://a.com/x.png ')).toBe(true)
  })

  it('data URI / 内置文件名 / 字符 / 空串都不算', () => {
    expect(isRemoteIcon('data:image/png;base64,xxx')).toBe(false)
    expect(isRemoteIcon('deepseek.svg')).toBe(false)
    expect(isRemoteIcon('🤖')).toBe(false)
    expect(isRemoteIcon('')).toBe(false)
    expect(isRemoteIcon('   ')).toBe(false)
  })
})
