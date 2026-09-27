// 渠道下拉「按分组归堆」的行为钉住（utils/channelOption.ts）：
//   · 渠道按 group_id 归到对应组头下，组头顺序跟随分组表（后端按 id 升序返回）
//   · 组内保持渠道表原顺序
//   · 空分组不出桶；group_id 指向已删除分组的渠道兜底进「已删除的分组」且在最后
//   · 选项形状：value 是 id 字符串、label 纯渠道名（无障碍约束，见被测文件）、
//     name / icon 透传给 ChannelOption 渲染
// 这些都是下拉里一眼可见的行为：组头丢了、顺序错了或是渠道被悄悄吞掉，
// 用户每次筛渠道都会撞上 —— 没有测试的话就是「改一次样式再也回不去」的回归。
import { describe, expect, it } from 'vitest'
import { channelOption, groupedChannelOptions } from './channelOption'
import type { Channel, ChannelGroup } from '@/api/types'

// 实体字段很多，而这里只用到被测函数读的那几个 —— 其余的与行为无关，
// 用断言跳过整张表的构造（真缺了字段，被测函数本身也会先挂）
const ch = (id: number, name: string, group_id: number, icon = '') =>
  ({ id, name, group_id, icon }) as unknown as Channel
const grp = (id: number, name: string) => ({ id, name }) as unknown as ChannelGroup

describe('groupedChannelOptions 按分组归堆', () => {
  it('渠道按 group_id 归到对应组头下，组头顺序跟随分组表', () => {
    // 分组表顺序故意与 id 相反：钉的是「跟随数组」而不是「按 id 排」
    const out = groupedChannelOptions(
      [ch(101, 'D1', 2), ch(102, 'D2', 1), ch(103, 'D3', 2)],
      [grp(2, '乙组'), grp(1, '甲组')]
    )
    expect(out.map((s) => s.label)).toEqual(['乙组', '甲组'])
    expect(out[0].options.map((o) => o.value)).toEqual(['101', '103'])
    expect(out[1].options.map((o) => o.value)).toEqual(['102'])
  })

  it('组内保持渠道表原顺序', () => {
    const out = groupedChannelOptions(
      [ch(103, 'D3', 1), ch(101, 'D1', 1)],
      [grp(1, '甲组')]
    )
    expect(out[0].options.map((o) => o.label)).toEqual(['D3', 'D1'])
  })

  it('没有渠道的分组不出桶', () => {
    const out = groupedChannelOptions([ch(101, 'D1', 1)], [
      grp(1, '甲组'),
      grp(2, '空组')
    ])
    expect(out.map((s) => s.label)).toEqual(['甲组'])
  })

  it('分组已删除的渠道兜底进「已删除的分组」，且排在最后', () => {
    const out = groupedChannelOptions([ch(104, '孤儿', 9), ch(101, 'D1', 1)], [
      grp(1, '甲组')
    ])
    expect(out.map((s) => s.label)).toEqual(['甲组', '已删除的分组'])
    expect(out[1].options.map((o) => o.value)).toEqual(['104'])
  })

  it('选项形状：value 为 id 字符串，name / icon 透传给渲染组件', () => {
    const out = groupedChannelOptions(
      [ch(101, 'D1', 1, '🚀')],
      [grp(1, '甲组')]
    )
    expect(out[0].options[0]).toEqual({ value: '101', label: 'D1', name: 'D1', icon: '🚀' })
  })
})

describe('channelOption 单个选项', () => {
  it('label 是纯渠道名，不带分组后缀', () => {
    // 分组信息由组头承担；label 必须保持纯文本（读屏依赖，见实现注释）
    expect(channelOption(ch(7, 'D1', 1))).toEqual({
      value: '7',
      label: 'D1',
      name: 'D1',
      icon: ''
    })
  })
})
