import { describe, expect, it } from 'vitest'
import { EFFORT_LEVELS, effortOptions, normalizeEfforts } from './efforts'

// 前端这份归一必须与后端 convert.NormalizeEfforts 同口径（见 efforts.ts 顶部）。
// 两边不一致时的表现是：库里存着 off、打开编辑器改别的档位保存后它没了 ——
// 因为 antd 的 select 把选项外的值当空 tag 丢掉，而这里正是它唯一的兜底。
describe('normalizeEfforts', () => {
  it('空输入回空数组', () => {
    expect(normalizeEfforts()).toEqual([])
    expect(normalizeEfforts(null)).toEqual([])
    expect(normalizeEfforts([])).toEqual([])
    expect(normalizeEfforts(['', '   '])).toEqual([])
  })

  it('按强度序重排，不按输入顺序', () => {
    expect(normalizeEfforts(['max', 'low', 'medium'])).toEqual(['low', 'medium', 'max'])
  })

  it('大小写与空白归一', () => {
    expect(normalizeEfforts([' HIGH ', 'Low'])).toEqual(['low', 'high'])
  })

  it('去重', () => {
    expect(normalizeEfforts(['low', 'low', 'LOW'])).toEqual(['low'])
  })

  it('none 归成 off（与后端的 NormalizeEffort 同义）', () => {
    expect(normalizeEfforts(['none'])).toEqual(['off'])
  })

  it('未知档位保留并殿后', () => {
    expect(normalizeEfforts(['ultra', 'low'])).toEqual(['low', 'ultra'])
    expect(normalizeEfforts(['ultra', 'ultra'])).toEqual(['ultra'])
  })
})

describe('effortOptions', () => {
  it('没声明时就是八档全出', () => {
    expect(effortOptions().map((o) => o.value)).toEqual([...EFFORT_LEVELS])
  })

  it('声明里有词表外的条目时补在末尾，不丢', () => {
    const values = effortOptions(['ultra', 'low']).map((o) => o.value)
    expect(values).toEqual([...EFFORT_LEVELS, 'ultra'])
  })
})
