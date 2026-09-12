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

/* 规格实测自参考站的 section-title（见 docs/ui-spec.md）：
   16px / 700 / 主色。这里原来只有 flex: 1，
   标题一直按浏览器对 h2 的默认样式渲染，字号与字重都和参考站对不上。 */
.section-title {
  flex: 1;
  margin: 0;
  font-size: 16px;
  font-weight: 700;
}

/* 这个类在模板里一直挂着，却从未定义过 ——
   accentTitle 这个 prop 因此完全不起作用，配色也没生效。
   注意模板里写的是 accentTitle !== false，也就是默认为真，
   所以标题默认就该是主色。 */
.section-title.is-accent {
  color: var(--color-primary);
}

.panel-header-extra {
  display: flex;
  align-items: center;
  gap: var(--gap);
}
</style>
