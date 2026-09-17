<script setup lang="ts">
// 页面工具栏：对齐参考站 dashboard-toolbar（高 42px，白底，圆角 8，flex gap 8）
defineProps<{ label?: string }>()
</script>

<template>
  <section class="panel toolbar-panel">
    <span v-if="label" class="toolbar-label">{{ label }}</span>
    <slot />
    <div class="toolbar-spacer" />
    <slot name="right" />
  </section>
</template>

<style scoped>
.toolbar-panel {
  display: flex;
  /* 允许换行：看板工具栏的内容（标签 + 4 档时间 + 三个下拉 + 条件标签 +
     刷新）声明宽合计近千像素，窄屏不换行会溢出面板把内容区顶出横向滚动 */
  flex-wrap: wrap;
  align-items: center;
  gap: var(--gap);
  row-gap: 6px;
  min-height: 42px;
}

.toolbar-label {
  font-size: 13px;
  color: var(--color-text-secondary);
}

.toolbar-spacer {
  flex: 1;
}
</style>
