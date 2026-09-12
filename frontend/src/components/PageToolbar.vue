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
  align-items: center;
  gap: var(--gap);
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
