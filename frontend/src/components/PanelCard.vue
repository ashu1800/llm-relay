<script setup lang="ts">
// 全站统一面板：实测自参考站的背景/边框/圆角/阴影/内边距规格
defineProps<{
  title?: string
  /** 标题是否使用主色（参考站 section-title 为橙色加粗） */
  accentTitle?: boolean
}>()
</script>

<template>
  <section class="panel">
    <header v-if="title || $slots.extra" class="panel-header">
      <h2 v-if="title" class="section-title" :class="{ 'is-accent': accentTitle !== false }">{{ title }}</h2>
      <div class="panel-header-extra">
        <slot name="extra" />
      </div>
    </header>
    <slot />
  </section>
</template>

<style scoped>
.panel-header {
  display: flex;
  align-items: center;
  gap: var(--gap);
  margin-bottom: var(--gap);
  min-height: 24px;
}

.section-title {
  flex: 1;
}

.panel-header-extra {
  display: flex;
  align-items: center;
  gap: var(--gap);
}
</style>
