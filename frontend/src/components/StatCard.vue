<script setup lang="ts">
// 概览统计卡：左侧彩色图标 + 标题 + 大号数值（对齐参考站 summary-card）
//
// 各项数值来自 docs/layout-dashboard.json 的实测抓取，
// 不是照着截图目测的：
//   summary-icon   48x48，border-radius 8px（圆角方块，不是圆形），
//                  字号 24px，背景为该色调 15% 透明度
//   summary-label  16px / 400 / rgba(48,48,48,0.65)
//   summary-value  24px / 700，颜色 = 该卡的色调色
withDefaults(defineProps<{
  label: string
  value: string | number
  /** 语义色：purple | orange | blue | green | red | gray */
  tone?: 'purple' | 'orange' | 'blue' | 'green' | 'red' | 'gray'
  hint?: string
}>(), { tone: 'purple' })
</script>

<template>
  <!-- 色调类挂在卡片根节点上，图标与数值都从它取色。
       原来只挂在图标上，于是数值只能固定用正文色 ——
       参考站里数值和图标是同一个颜色，这正是「数值全是黑的」的原因。 -->
  <article class="summary-card" :class="'tone-' + tone">
    <div class="summary-icon">
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

/* 每种色调只在这里定义一次，图标与数值共用。
   用自定义属性而不是给每个色调各写一条选择器：
   否则加一种色调要改两处，迟早会漏。 */
.tone-purple { --tone-color: var(--color-purple); --tone-bg: rgba(139, 92, 245, 0.15); }
.tone-orange { --tone-color: var(--color-orange); --tone-bg: rgba(245, 158, 11, 0.15); }
.tone-blue   { --tone-color: var(--color-blue);   --tone-bg: rgba(6, 182, 212, 0.15); }
.tone-green  { --tone-color: var(--color-green);  --tone-bg: rgba(16, 179, 125, 0.15); }
.tone-red    { --tone-color: var(--color-red);    --tone-bg: rgba(234, 67, 67, 0.15); }
.tone-gray   { --tone-color: var(--color-gray);   --tone-bg: rgba(107, 114, 128, 0.15); }

.summary-icon {
  width: 48px;
  height: 48px;
  flex: 0 0 48px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  font-size: 24px;
  color: var(--tone-color);
  background: var(--tone-bg);
}

.summary-body { min-width: 0; }

.summary-label {
  font-size: 16px;
  font-weight: 400;
  line-height: 1.15;
  /* 用变量而不是写死 rgba(48,48,48,0.65)：
     那个字面值正是本变量在亮色主题下的取值（见 docs/ui-spec.json），
     写死的话暗色主题下会变成深色字压深色底 */
  color: var(--color-text-secondary);
}

.summary-value {
  font-size: 24px;
  font-weight: 700;
  line-height: 1.2;
  color: var(--tone-color);
  font-variant-numeric: tabular-nums;
  word-break: break-all;
}

.summary-hint {
  font-size: 14px;
  line-height: 1.3;
  color: var(--color-text-secondary);
}
</style>
