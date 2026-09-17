<script setup lang="ts">
// 渠道图标：优先用配置的图标，没有就用「渠道名首字母 + 按名字派生的底色」。
// 默认图标不做成一张固定图片：一屏渠道全是同一个灰色方块时，
// 图标就失去了「一眼认出是哪条渠道」的作用。
import { computed } from 'vue'
import { hash32 } from '@/utils/hash'

const props = withDefaults(
  defineProps<{
    name?: string | null
    icon?: string | null
    /** 边长（px） */
    size?: number
  }>(),
  { size: 22 }
)

const letter = computed(() => (props.name || '?').trim().charAt(0).toUpperCase() || '?')

// 图片类图标：data URI 或 http(s) 地址。
// 其余一律当成「用户直接写的文字/emoji」——那样比强制填图片地址好用得多。
const isImage = computed(() => {
  const v = (props.icon || '').trim()
  return v.startsWith('data:image/') || /^https?:\/\//i.test(v)
})

const textIcon = computed(() => {
  const v = (props.icon || '').trim()
  return v && !isImage.value ? v : ''
})

const style = computed(() => {
  const s = props.size
  if (isImage.value || textIcon.value) {
    return { width: s + 'px', height: s + 'px', fontSize: Math.round(s * 0.62) + 'px' }
  }
  const hue = hash32((props.name || '').trim().toLowerCase()) % 360
  return {
    width: s + 'px',
    height: s + 'px',
    fontSize: Math.round(s * 0.5) + 'px',
    // 色相随渠道名走；亮度档位交给 CSS 变量（theme.css 的
    // --ch-icon-bg-l / --ch-icon-ink-l）——原来把 oklch(0.93 …) 整段写死在
    // 内联样式里，[data-theme='dark'] 永远覆盖不到它，暗色主题下
    // 亮色图标块排在深色表格里非常刺眼
    '--ch-h': hue
  }
})
</script>

<template>
  <span
    class="ch-icon"
    :class="{ 'ch-icon-default': !isImage && !textIcon }"
    :style="style"
    :title="name || ''"
  >
    <img v-if="isImage" :src="icon || ''" alt="" />
    <template v-else-if="textIcon">{{ textIcon }}</template>
    <template v-else>{{ letter }}</template>
  </span>
</template>

<style scoped>
.ch-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  border-radius: var(--radius-control);
  overflow: hidden;
  line-height: 1;
  font-weight: 600;
  vertical-align: middle;
}
/* 默认图标（首字母 + 派生色）的配色：色相来自内联 --ch-h（随渠道名散列），
   亮度档位读全局变量 —— theme.css 里浅色给亮底深字、暗色换暗底亮字，
   与 GroupTag/ModelTag 的双主题分支同一套做法 */
.ch-icon-default {
  background: oklch(var(--ch-icon-bg-l) 0.05 var(--ch-h));
  color: oklch(var(--ch-icon-ink-l) 0.13 var(--ch-h));
}
.ch-icon img { width: 100%; height: 100%; object-fit: contain; }
</style>
