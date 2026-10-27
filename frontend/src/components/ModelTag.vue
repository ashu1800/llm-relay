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

/** 厂商血统光谱微晶点（方案 1） */
function pedigreeColor(name?: string | null): string {
  const s = (name || '').toLowerCase()
  if (/gpt|o1|o3|text-embedding/.test(s)) return '#10b981'
  if (/claude/.test(s)) return '#d97706'
  if (/gemini/.test(s)) return '#3b82f6'
  if (/deepseek/.test(s)) return '#0284c7'
  if (/qwen/.test(s)) return '#8b5cf6'
  if (/llama|meta/.test(s)) return '#ef4444'
  return 'currentColor'
}
</script>

<template>
  <span class="model-tag" :style="vars" :title="style.label">
    <span class="pedigree-pip" :style="{ '--pip-color': pedigreeColor(name) }" aria-hidden="true" />
    <span class="model-tag-text">{{ style.label }}</span>
  </span>
</template>

<style scoped>
.model-tag {
  /* 明度由主题决定，色相与彩度由模型名决定（见 utils/modelStyle.ts）。
     浅色族 L=0.47、深色族 0.80，都是实测出来的：它让各色相都能满足正文
     4.5:1 的对比度要求，又不会糊在背景里。值走主题令牌
     （2026-09-30 之前深色是下面一条 data-theme='dark' 覆盖，
     多主题之后那种写法要列 8 个深色 id）。 */
  --mt-l: var(--tag-tint-l);

  display: inline-flex;
  align-items: center;
  gap: 5px;
  /* 列宽装不下的长模型名要出省略号，不能把列撑开、更不能折行
     （折行会把这一行撑得比别的行高）。full 名字由 title 提供。 */
  max-width: 100%;
  padding: 1px 7px;
  border-radius: var(--radius-control);
  border: 1px solid oklch(var(--mt-l) var(--mt-c) var(--mt-h) / 0.32);
  background: oklch(var(--mt-l) var(--mt-c) var(--mt-h) / 0.13);
  color: oklch(var(--mt-l) var(--mt-c) var(--mt-h));
  font-size: var(--font-size-sm);
  font-weight: 500;
  line-height: 20px;
  vertical-align: middle;
}

.pedigree-pip {
  flex: none;
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--pip-color);
  box-shadow: 0 0 3px var(--pip-color);
}

.model-tag-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 深色族提亮那一档现在写在 theme.css 的 --tag-tint-l 里（每个深色主题块各写一次）：
   同一个色相在深底上必须提亮才够对比，直接沿用浅色主题的值会糊在背景里。 */
</style>
