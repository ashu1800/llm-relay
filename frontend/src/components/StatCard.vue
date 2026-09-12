<script setup lang="ts">
// 概览统计卡：左侧彩色圆形图标 + 标题 + 大号数值（对齐参考站 summary-card）
withDefaults(defineProps<{
  label: string
  value: string | number
  /** 语义色：purple | orange | blue | green | red | gray */
  tone?: 'purple' | 'orange' | 'blue' | 'green' | 'red' | 'gray'
  hint?: string
}>(), { tone: 'purple' })
</script>

<template>
  <article class="summary-card">
    <div class="summary-icon" :class="'tone-' + tone">
      <slot name="icon" />
    </div>
    <div class="summary-body">
      <div class="summary-label">{{ label }}</div>
      <div class="summary-value">{{ value }}</div>
      <div v-if="hint" class="summary-hint">{{ hint }}</div>
    </div>
  </article>
</template>

<style scoped>
.summary-card {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: var(--pad-panel);
  background: var(--color-fg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  box-shadow: var(--color-fg-shadow);
}

.summary-icon {
  width: 38px;
  height: 38px;
  flex: 0 0 38px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  font-size: 19px;
}

.tone-purple { color: var(--color-purple); background: rgba(139, 92, 245, 0.12); }
.tone-orange { color: var(--color-orange); background: rgba(245, 158, 11, 0.12); }
.tone-blue   { color: var(--color-blue);   background: rgba(6, 182, 212, 0.12); }
.tone-green  { color: var(--color-green);  background: rgba(16, 179, 125, 0.12); }
.tone-red    { color: var(--color-red);    background: rgba(234, 67, 67, 0.12); }
.tone-gray   { color: var(--color-gray);   background: rgba(107, 114, 128, 0.12); }

.summary-body { min-width: 0; }

.summary-label {
  font-size: 13px;
  color: var(--color-text-secondary);
  line-height: 1.4;
}

.summary-value {
  font-size: 24px;
  font-weight: 600;
  line-height: 1.3;
  color: var(--color-text);
  font-variant-numeric: tabular-nums;
  word-break: break-all;
}

.summary-hint {
  font-size: 12px;
  color: var(--color-text-secondary);
}
</style>
