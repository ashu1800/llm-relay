// 渠道下拉选项的行为钉住（utils/channelOption.ts）：
//   · 选项形状：value 是 id 字符串、label 纯渠道名（无障碍约束，见被测文件）、
//     name / icon 透传给 ChannelOption 渲染
// 分组归堆（groupedChannelOptions）已随分组功能一起移除（2026-10-26），
// 下拉回到平铺 —— 这份测试只锁单个选项的形状契约。
import { describe, expect, it } from 'vitest'
import { channelOption } from './channelOption'
import type { Channel } from '@/api/types'

// 实体字段很多，而这里只用到被测函数读的那几个 —— 其余的与行为无关，
// 用断言跳过整张表的构造（真缺了字段，被测函数本身也会先挂）
const ch = (id: number, name: string, icon = '') =>
  ({ id, name, icon }) as unknown as Channel

describe('channelOption 单个选项', () => {
  it('选项形状：value 为 id 字符串，label 纯渠道名，name / icon 透传', () => {
    // label 必须保持纯文本（读屏依赖，见实现注释）
    expect(channelOption(ch(7, 'D1', '🚀'))).toEqual({
      value: '7',
      label: 'D1',
      name: 'D1',
      icon: '🚀'
    })
  })

  it('icon 缺省时透传空串（渲染组件按名字派生底色）', () => {
    expect(channelOption(ch(9, 'D2')).icon).toBe('')
  })

  it('同名的两条渠道各自成项，value 用 id 区分（渠道名没有唯一约束）', () => {
    const a = channelOption(ch(1, 'D1'))
    const b = channelOption(ch(2, 'D1'))
    expect(a.value).toBe('1')
    expect(b.value).toBe('2')
    expect(a.label).toBe(b.label)
  })
})
