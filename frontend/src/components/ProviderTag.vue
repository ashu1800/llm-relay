<script setup lang="ts">
import { computed } from 'vue'
import { providerStyle, providerVars } from '@/utils/providerStyle'

// 模型商标签：首字母徽标 + 名称，颜色由模型商决定。
// 首字母不是装饰 —— 没有它就只能靠颜色区分，色盲用户和黑白打印会失去信息。
const props = withDefaults(
  defineProps<{
    code?: string | null
    name?: string | null
    /** compact 只显示徽标，用于列宽紧张的位置 */
    compact?: boolean
  }>(),
  { code: '', name: '', compact: false }
)

const style = computed(() => providerStyle(props.code, props.name))
const vars = computed(() => providerVars(style.value))
</script>

<template>
  <span class="provider-tag" :style="vars" :title="style.label">
    <span class="provider-mark">{{ style.short }}</span>
    <span v-if="!compact" class="provider-name">{{ style.label }}</span>
  </span>
</template>

<style scoped>
.provider-tag {
  /* 明度由主题决定，色相与彩度由模型商决定（见 utils/providerStyle.ts）。
     L=0.47 是实测值：oklch 感知均匀，这个明度对各色相都能满足
     正文 4.5:1 的对比度要求，不会出现黄色够、蓝色不够的情况。 */
  --pt-l: 0.47;

  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 1px 7px 1px 2px;
  border-radius: var(--radius-pill);
  border: 1px solid oklch(var(--pt-l) var(--pt-c) var(--pt-h) / 0.25);
  background: oklch(var(--pt-l) var(--pt-c) var(--pt-h) / 0.11);
  color: oklch(var(--pt-l) var(--pt-c) var(--pt-h));
  font-size: 12px;
  line-height: 18px;
  white-space: nowrap;
  vertical-align: middle;
}

.provider-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: oklch(var(--pt-l) var(--pt-c) var(--pt-h));
  color: #fff;
  font-size: 10px;
  font-weight: 700;
  line-height: 1;
}

.provider-name {
  font-weight: 500;
}

/* 深色主题下换成高明度文字：同一个色相在深底上必须提亮才够对比，
   直接沿用浅色主题的值会糊在背景里。 */
:root[data-theme='dark'] .provider-tag {
  /* 深色底要提亮：同一个色相在 #202020 上沿用浅色主题的明度会糊进背景 */
  --pt-l: 0.80;
}

:root[data-theme='dark'] .provider-mark {
  background: oklch(var(--pt-l) var(--pt-c) var(--pt-h));
  color: #1a1a1a;
}
</style>
