<script setup lang="ts">
// 渠道下拉的选项内容：图标 + 名字。
//
// 为什么是一个组件、而不是在视图里内联渲染：下拉项与选中值是两处渲染
// （antd 的 option / optionLabel 两个插槽），两处必须长得一样 ——
// 分开写迟早会出现「下拉里有图标、选完就没了」这种不一致。
//
// 样式写在组件里（scoped）：浮层是挂到 body 上的，视图里的 scoped 样式
// 到不了那里，而这个组件自己的样式跟着它渲染到哪都生效。
import ChannelIcon from '@/components/ChannelIcon.vue'

defineProps<{
  option: { label?: string | number; name?: string; icon?: string }
}>()
</script>

<template>
  <span class="chan-opt">
    <!-- 「全部渠道」没有渠道可显示图标，只出文字 -->
    <ChannelIcon v-if="option.name" :name="option.name" :icon="option.icon" :size="16" />
    <span class="chan-opt-text">{{ option.label }}</span>
  </span>
</template>

<style scoped>
/* 名字那层要 min-width: 0 + 省略号：flex 子项默认不肯缩到内容宽度以下，
   渠道名一长就会顶破下拉格子，而不是像普通选项那样出现「…」 */
.chan-opt {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
}
.chan-opt-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
