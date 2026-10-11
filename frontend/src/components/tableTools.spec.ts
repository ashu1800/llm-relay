import { describe, expect, it } from 'vitest'
import { clampPage, matchQuery, pageSlice, showPager } from './tableTools'

describe('matchQuery', () => {
  it('空串命中一切（搜索框清空即回到全部）', () => {
    expect(matchQuery('anything', '')).toBe(true)
    expect(matchQuery('', '')).toBe(true)
    // 只有空白也算空：用户误按空格不该把整张表清空
    expect(matchQuery('anything', '   ')).toBe(true)
  })

  it('大小写不敏感', () => {
    expect(matchQuery('GPT-4o', 'gpt')).toBe(true)
    expect(matchQuery('gpt-4o', 'GPT-4O')).toBe(true)
  })

  it('多个词之间是 AND，且可以跨词序', () => {
    expect(matchQuery('key1 gpt-4o 生产', 'gpt 生产')).toBe(true)
    expect(matchQuery('key1 gpt-4o 生产', '生产 gpt')).toBe(true)
    expect(matchQuery('key1 gpt-4o 生产', 'gpt 测试')).toBe(false)
  })

  it('多余空白不影响（复制粘贴常常带前后空格）', () => {
    expect(matchQuery('渠道甲', '  渠道甲  ')).toBe(true)
  })

  it('空文本不命中非空查询', () => {
    expect(matchQuery('', 'gpt')).toBe(false)
  })
})

describe('clampPage', () => {
  it('页数够时原样返回', () => {
    expect(clampPage(3, 100, 20)).toBe(3)
  })

  it('数据变少后把越界的页码夹回最后一页', () => {
    // 这是搜索时最容易漏的一处：在第 5 页搜索只剩下 3 条，
    // 不夹的话表格是空的，用户以为「没搜到」
    expect(clampPage(5, 3, 20)).toBe(1)
    expect(clampPage(5, 45, 20)).toBe(3)
  })

  it('空结果集也返回第 1 页而不是 0', () => {
    expect(clampPage(1, 0, 20)).toBe(1)
    expect(clampPage(9, 0, 20)).toBe(1)
  })

  it('页码小于 1 时兜回 1', () => {
    expect(clampPage(0, 100, 20)).toBe(1)
    expect(clampPage(-3, 100, 20)).toBe(1)
  })

  it('每页条数为 0 或负数时不除零、也不给出 Infinity 页', () => {
    // 0 被兜成 1 条一页 → 100 行有 100 页，第 1 页仍然合法
    expect(clampPage(1, 100, 0)).toBe(1)
    expect(clampPage(1, 100, -5)).toBe(1)
    expect(Number.isFinite(clampPage(2, 100, 0))).toBe(true)
  })
})

describe('pageSlice', () => {
  const list = Array.from({ length: 25 }, (_, i) => i + 1)

  it('第 1 页取下标的 0..size', () => {
    expect(pageSlice(list, 1, 10)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10])
  })

  it('末页不足一整页时只给剩下的', () => {
    expect(pageSlice(list, 3, 10)).toEqual([21, 22, 23, 24, 25])
  })

  it('超出范围的页给空数组（由 clampPage 负责不让它发生）', () => {
    expect(pageSlice(list, 9, 10)).toEqual([])
  })

  it('原数组不被修改', () => {
    const copy = [...list]
    pageSlice(list, 2, 10)
    expect(list).toEqual(copy)
  })
})

describe('showPager', () => {
  it('只有一页时不显示', () => {
    expect(showPager(3, 20)).toBe(false)
    expect(showPager(20, 20)).toBe(false)
  })

  it('超过一页时显示', () => {
    expect(showPager(21, 20)).toBe(true)
  })

  it('空表不显示', () => {
    expect(showPager(0, 20)).toBe(false)
  })
})
