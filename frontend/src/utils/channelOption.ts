import type { Channel, ChannelGroup } from '@/api/types'

/**
 * 渠道下拉的选项：名字后面按需补上所属分组，并带上渲染图标需要的信息。
 *
 * 分组名为什么是「按需」：渠道名没有唯一约束，跨分组挑渠道时两个「D1」分不清，
 * 所以要一并显示分组名；而已经筛了某个分组时，候选里每条渠道的分组都一样 ——
 * 一列「· DeepSeek」既不提供信息，又把渠道名挤到显示不全。
 *
 * label 保持**纯文本**（图标由 components/ChannelOption.vue 通过 option / optionLabel
 * 两个插槽渲染）：label 一旦换成 VNode，antd 就给不出无障碍名称了 ——
 * 它只把字符串 label 写进隐藏 listbox 的 aria-label，VNode 标签的选项在
 * 读屏软件里只剩一个数字 id。
 *
 * name / icon 是额外字段，供那个组件渲染用；antd 会把整个选项对象透传给插槽。
 */
export function channelOption(c: Channel, groups: ChannelGroup[], withGroup: boolean) {
  const g = groups.find((x) => x.id === c.group_id)
  return {
    value: String(c.id),
    label: withGroup && g ? c.name + ' · ' + g.name : c.name,
    name: c.name,
    icon: c.icon
  }
}
