// 客户端映射表的两条自洽断言。
//
// 文件是否真的存在、id 集合与后端规则表是否一致，由
// frontend/scripts/check-contracts.mjs 把关（它读得到 backend 源码与
// public 目录；vitest 跑在前端目录里，后端源码不一定在）。
// 这里守住的是更轻但同样会静默出错的两条：file 都带 .svg 后缀、
// id 都是 kebab-case —— 后者是给规则表与映射表「同名同形」立的口径：
// 两边各写一遍的 id 一旦风格漂移（openaiNode vs openai-node），
// 表现为图标静默落到 default，而不是报错。
import { describe, expect, it } from 'vitest'
import { CLIENT_META, clientIconFile } from './clientMeta'

describe('CLIENT_META', () => {
  it('每个 file 都以 .svg 结尾', () => {
    for (const [id, meta] of Object.entries(CLIENT_META)) {
      expect(meta.file, `id ${id} 的 file`).toMatch(/\.svg$/)
    }
  })

  it('每个 id 都是 kebab-case（与后端规则表的 id 同形）', () => {
    for (const id of Object.keys(CLIENT_META)) {
      expect(id, `id ${id}`).toMatch(/^[a-z0-9]+(-[a-z0-9]+)*$/)
    }
  })

  it('每个 name 非空（空名字会在列里渲染成一枚孤零零的图标）', () => {
    for (const [id, meta] of Object.entries(CLIENT_META)) {
      expect(meta.name.trim(), `id ${id} 的 name`).not.toBe('')
    }
  })
})

describe('clientIconFile', () => {
  it('已知 id 返回映射的文件', () => {
    expect(clientIconFile('claude-code')).toBe('claude-code.svg')
  })

  it('未知 id / 空值一律回落到默认图标', () => {
    // 三种输入形态都要覆盖：'unknown' 是「后端加了规则、前端还没跟」，
    // '' 与 null 是历史行与「请求没带 UA」—— 它们都渲染同一枚默认图标
    expect(clientIconFile('some-new-cli')).toBe('default.svg')
    expect(clientIconFile('')).toBe('default.svg')
    expect(clientIconFile(null)).toBe('default.svg')
    expect(clientIconFile(undefined)).toBe('default.svg')
  })
})
