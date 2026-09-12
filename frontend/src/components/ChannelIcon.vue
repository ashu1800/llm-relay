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
    // 与分组胶囊同一套配色思路：浅底 + 同色文字，深浅主题都不刺眼
    background: `oklch(0.93 0.05 ${hue})`,
    color: `oklch(0.45 0.13 ${hue})`
  }
})
</script>

<template>
  <span class="ch-icon" :style="style" :title="name || ''">
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
.ch-icon img { width: 100%; height: 100%; object-fit: contain; }
</style>
