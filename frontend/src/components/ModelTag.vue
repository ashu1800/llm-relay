<script setup lang="ts">
import { computed } from 'vue'
import { modelStyle, modelVars } from '@/utils/modelStyle'

// 模型标签：同一个模型永远同一种颜色（色相由模型名派生，见 utils/modelStyle.ts）。
// 颜色只是辅助 —— 模型名始终写在胶囊里，色盲用户与黑白打印同样能分辨。
const props = defineProps<{
  name?: string | null
}>()

const style = computed(() => modelStyle(props.name))
const vars = computed(() => modelVars(style.value))
</script>

<template>
  <span class="model-tag" :style="vars" :title="style.label">{{ style.label }}</span>
</template>

<style scoped>
.model-tag {
  /* 明度由主题决定，色相与彩度由模型名决定（见 utils/modelStyle.ts）。
     L=0.47 是实测出来的：它让各色相都能满足正文 4.5:1 的对比度要求，
     又不会在浅色主题下糊成一片。 */
  --mt-l: 0.47;

  display: inline-block;
  /* 列宽装不下的长模型名要出省略号，不能把列撑开、更不能折行
     （折行会把这一行撑得比别的行高）。full 名字由 title 提供。 */
  max-width: 100%;
  padding: 1px 8px;
  border-radius: var(--radius-control);
  border: 1px solid oklch(var(--mt-l) var(--mt-c) var(--mt-h) / 0.32);
  background: oklch(var(--mt-l) var(--mt-c) var(--mt-h) / 0.13);
  color: oklch(var(--mt-l) var(--mt-c) var(--mt-h));
  font-size: 12px;
  font-weight: 500;
  line-height: 20px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}

/* 深色主题下换成高明度文字：同一个色相在深底上必须提亮才够对比，
   直接沿用浅色主题的值会糊在背景里。 */
:root[data-theme='dark'] .model-tag {
  --mt-l: 0.80;
}
</style>
