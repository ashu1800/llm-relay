<script setup lang="ts">
// 渠道生命灯：一行一枚小圆点，用形状与节奏回答「这条渠道现在怎么样」。
//
//   灰点静止   —— 已停用（不参与路由，没什么好说的）
//   绿点慢呼吸 —— 正常
//   橙点快闪   —— 最近有失败（degraded 或连击 > 0）
//   橙环收缩   —— 熔断冷却倒计时：环从满圈收到空，收到头冷却就结束了
//
// 冷却环只画「剩余量的流逝」而不是「占总冷却的比例」：冷却总时长随
// Retry-After 与触发方式（手动 429/自动连击）变化，快照里只有剩余值。
// 环的语义是「这条渠道的剩余寿命正在流逝」，刷新页面后环重新从满圈开始 ——
// 误差不超过一次冷却长度，换来的是不用给后端加「总时长」字段。
import { computed, onUnmounted, ref, watch } from 'vue'
import type { Channel } from '@/api/types'

const props = withDefaults(
  defineProps<{
    enabled: boolean
    health: string
    runtime?: Channel['runtime'] | null
    /** 点的直径（px）；冷却环略大于点 */
    size?: number
  }>(),
  { size: 12 }
)

// ---- 本地倒计时 ----
// 快照是「拉取那一刻的剩余毫秒」，页面开着它会静止不动；这里按
// 截止时刻每 250ms 递减。列表重取 / channel_health 推送都会换新的
// runtime 引用，watch 到就重新校准（以新的剩余为准，误差不累积）。
const remain = ref(0)
/** 进入当前这一轮冷却时的总剩余，环的收缩以此为满圈 */
const initial = ref(1)
let timer: number | null = null

function sync() {
  const ms = props.runtime?.cooldown_ms ?? 0
  remain.value = ms
  initial.value = Math.max(1, ms)
  if (timer !== null) {
    window.clearInterval(timer)
    timer = null
  }
  if (ms > 0) {
    const deadline = Date.now() + ms
    timer = window.setInterval(() => {
      remain.value = Math.max(0, deadline - Date.now())
      if (remain.value === 0 && timer !== null) {
        window.clearInterval(timer)
        timer = null
      }
    }, 250)
  }
}
watch(() => props.runtime, sync, { immediate: true, deep: true })
onUnmounted(() => {
  if (timer !== null) window.clearInterval(timer)
})

type State = 'off' | 'cooling' | 'warn' | 'ok'
const state = computed<State>(() => {
  if (!props.enabled) return 'off'
  if (remain.value > 0) return 'cooling'
  const streak = props.runtime?.fail_streak ?? 0
  if (streak > 0 || props.health === 'degraded') return 'warn'
  return 'ok'
})

// 冷却环（SVG 周长固定 2πr，用 dashoffset 表示「还剩多少」）
const R = 8
const CIRC = 2 * Math.PI * R
const dashOffset = computed(() => CIRC * (1 - remain.value / initial.value))

const tip = computed(() => {
  switch (state.value) {
    case 'off':
      return '已停用：不参与任何路由'
    case 'cooling':
      return `熔断冷却中，剩 ${Math.ceil(remain.value / 1000)} 秒，期间请求自动绕开`
    case 'warn':
      return `最近有失败（连击 ${props.runtime?.fail_streak ?? 0} 次），恢复成功后会转绿`
    default:
      return '运行正常'
  }
})
</script>

<template>
  <span
    class="life-dot"
    :class="state"
    :style="{ width: size + 'px', height: size + 'px' }"
    :title="tip"
  >
    <svg v-if="state === 'cooling'" class="ring" viewBox="0 0 20 20" aria-hidden="true">
      <circle class="ring-bg" cx="10" cy="10" :r="R" />
      <circle
        class="ring-fg"
        cx="10"
        cy="10"
        :r="R"
        :stroke-dasharray="CIRC"
        :stroke-dashoffset="dashOffset"
      />
    </svg>
    <span class="dot" aria-hidden="true" />
  </span>
</template>

<style scoped>
.life-dot {
  position: relative;
  display: inline-flex;
  flex: 0 0 auto;
  /* 光晕会画出点外，别被行裁掉 */
  overflow: visible;
}
.dot {
  width: 100%;
  height: 100%;
  border-radius: 50%;
}

/* 正常：绿点慢呼吸。呼吸而不是常亮 —— 常亮的绿点看两天就「隐形」了，
   节奏感让它持续留在余光里 */
.life-dot.ok .dot {
  background: var(--color-green);
  animation: life-breathe 3.2s ease-in-out infinite;
  box-shadow: 0 0 6px color-mix(in oklab, var(--color-green) 45%, transparent);
}

/* 有失败：橙点快闪 */
.life-dot.warn .dot {
  background: var(--color-orange);
  animation: life-blink 1s ease-in-out infinite;
}

/* 已停用：灰点静止 */
.life-dot.off .dot {
  background: var(--color-gray);
  opacity: 0.45;
}

/* 冷却中：橙点 + 收缩环 */
.life-dot.cooling .dot {
  background: var(--color-orange);
}
.ring {
  position: absolute;
  inset: -4px;
  width: calc(100% + 8px);
  height: calc(100% + 8px);
  transform: rotate(-90deg);
}
.ring-bg,
.ring-fg {
  fill: none;
  stroke-width: 2.6;
  stroke-linecap: round;
}
.ring-bg {
  stroke: color-mix(in oklab, var(--color-orange) 22%, transparent);
}
.ring-fg {
  stroke: var(--color-orange);
}

@keyframes life-breathe {
  0%,
  100% {
    opacity: 1;
    transform: scale(1);
  }
  50% {
    opacity: 0.45;
    transform: scale(0.8);
  }
}
@keyframes life-blink {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.25;
  }
}
</style>
