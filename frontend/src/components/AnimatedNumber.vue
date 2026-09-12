<script setup lang="ts">
// 数字滚动：数值变化时从旧值补间到新值，而不是直接跳过去。
//
// 为什么值得做：看板每两秒会收到一次推送，直接换文本的话，
// 用户根本注意不到「哪个数字变了」—— 滚动过程本身就是变化的提示。
//
// 用 requestAnimationFrame + 缓出（easeOutCubic）而不是 CSS transition：
// 数字内容不是可插值的 CSS 属性，只能用 JS 逐帧算。
// 时长 0.5s：比参考站的 0.3s 稍长，24px 的大字号下太快会看着像闪烁。
import { computed, onUnmounted, ref, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    value: number | null | undefined
    /** 补间时长（毫秒） */
    duration?: number
    /**
     * 格式化方式。预设三种，也接受自定义函数：
     *   int（默认）整数千分位 / money 金额 / percent 百分比
     * 用预设而不是让调用方各自传函数：这几个格式在看板、日志页要完全一致，
     * 分散在各页面里迟早会对不上（金额的小数位数尤其容易各写各的）
     */
    format?: 'int' | 'money' | 'percent' | ((v: number) => string)
  }>(),
  { duration: 500 }
)

const shown = ref(props.value ?? 0)
let raf = 0
let fallbackTimer: number | null = null

const text = computed(() => {
  if (props.value === null || props.value === undefined) return '--'
  return applyFormat(shown.value)
})

function applyFormat(v: number) {
  const f = props.format
  if (typeof f === 'function') return f(v)
  switch (f) {
    case 'money':
      // 与看板原生的 money() 同口径：金额通常远小于 1，
      // 固定 4 位会全变成 0.0000
      if (!v) return '0.0000'
      return '$' + v.toFixed(v < 1 ? 6 : 4)
    case 'percent':
      return v.toFixed(1) + '%'
    default:
      // 词元、次数这类都是整数
      return Math.round(v).toLocaleString('en-US')
  }
}

watch(
  () => props.value,
  (to) => {
    if (to === null || to === undefined) {
      cancelAnimationFrame(raf)
      return
    }
    const from = shown.value
    if (from === to) return
    const start = performance.now()
    const dur = Math.max(1, props.duration)
    cancelAnimationFrame(raf)
    // 兜底定时器：浏览器在标签页不可见时会**暂停 requestAnimationFrame**，
    // 只靠 rAF 的话数字会永远停在旧值 —— 而且 watch 已经触发过，不会再来一次。
    // 实测就是这么发现的：CDP 打开的页面在后台，采样到的数字一直没动。
    if (fallbackTimer !== null) window.clearTimeout(fallbackTimer)
    fallbackTimer = window.setTimeout(() => {
      fallbackTimer = null
      cancelAnimationFrame(raf)
      shown.value = to
    }, dur + 60)
    const step = (now: number) => {
      const t = Math.min(1, (now - start) / dur)
      const eased = 1 - Math.pow(1 - t, 3)
      shown.value = from + (to - from) * eased
      if (t < 1) {
        raf = requestAnimationFrame(step)
      } else {
        // 收尾必须精确落到目标值：补间累积的浮点误差会让 1000 显示成 999
        shown.value = to
        if (fallbackTimer !== null) {
          window.clearTimeout(fallbackTimer)
          fallbackTimer = null
        }
      }
    }
    raf = requestAnimationFrame(step)
  }
)

onUnmounted(() => {
  cancelAnimationFrame(raf)
  if (fallbackTimer !== null) window.clearTimeout(fallbackTimer)
})
</script>

<template>
  <span class="an-num">{{ text }}</span>
</template>

<style scoped>
/* 等宽数字：补间时每一位的宽度固定，数字不会左右抖动 */
.an-num { font-variant-numeric: tabular-nums; }
</style>
