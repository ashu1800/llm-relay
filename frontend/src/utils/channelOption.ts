import type { Channel, ChannelGroup } from '@/api/types'

/**
 * 渠道下拉的单个选项：纯渠道名，不带任何分组后缀 —— 分组信息由
 * groupedChannelOptions 的组头承担（见那里的注释）。
 *
 * label 保持**纯文本**（图标由 components/ChannelOption.vue 通过 option / optionLabel
 * 两个插槽渲染）：label 一旦换成 VNode，antd 就给不出无障碍名称了 ——
 * 它只把字符串 label 写进隐藏 listbox 的 aria-label，VNode 标签的选项在
 * 读屏软件里只剩一个数字 id。
 *
 * name / icon 是额外字段，供那个组件渲染用；antd 会把整个选项对象透传给插槽。
 */
export function channelOption(c: Channel) {
  return {
    value: String(c.id),
    label: c.name,
    name: c.name,
    icon: c.icon
  }
}

/**
 * 渠道下拉的选项按分组归堆：分组名做组头（antd options 的 { label, options }
 * 结构，组头不可选），渠道列在自己分组下面。
 *
 * 为什么归堆、而不是在每个渠道名后面拼「· 分组名」：渠道名没有唯一约束，
 * 跨分组的两个「D1」确实要靠分组才分得清 —— 但平铺时这条信息摊在每一行
 * 选项上，一长就把渠道名本身挤得显示不全；组头一行说一次，每行选项的
 * 宽度就能完整留给渠道名。
 *
 * 组的顺序跟随分组表（后端按 id 升序返回，即建组顺序），组内保持渠道表
 * 原顺序。没有渠道的分组不出桶：下拉里一个空标题回答不了任何问题。
 * group_id 指向已删除分组的渠道兜底进「已删除的分组」桶 —— 分组可以在
 * 渠道还在的时候被删，下拉不该因此悄悄吞掉这些渠道。
 */
export function groupedChannelOptions(channels: Channel[], groups: ChannelGroup[]) {
  const sections = groups
    .map((g) => ({
      label: g.name,
      options: channels.filter((c) => c.group_id === g.id).map(channelOption)
    }))
    .filter((s) => s.options.length > 0)
  const orphans = channels.filter((c) => !groups.some((g) => g.id === c.group_id))
  if (orphans.length) {
    sections.push({ label: '已删除的分组', options: orphans.map(channelOption) })
  }
  return sections
}
