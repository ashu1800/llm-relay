import type { Channel } from '@/api/types'

/**
 * 渠道下拉的单个选项：纯渠道名，不带任何后缀。
 *
 * 分组已移除（2026-10-26），原来的 groupedChannelOptions（按分组归堆、
 * 组头做标题）随之删除，下拉回到平铺列表。
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
