<script setup lang="ts">
// 请求脉搏条：一条 3px 细线，每个到达的请求闪过一道流光。
//
// 看板上所有数字都在说「过去发生了多少」，这一条说的是「此刻还有没有人在用」——
// 有流量时光带持续跳动，光带安静了就说明没流量。比任何数字都直白。
// 数据走现成的 live logs 推送，零额外请求；不引图表库，纯 CSS 动画。
//
// ---- 断线时必须改口（2026-10-11）----
//
// 光带安静的两种原因是完全相反的：「真的没流量」与「实时连接断了」。
// 原来两者共用一句「暂无流量」—— 断线时这句是假话，而且它把用户引去查
// 渠道和上游（真正的故障在连接上）。现在断线走独立的一档：斜纹底轨 +
// 灰掉的色，title 直接说连接断了。
import { computed, onUnmounted, ref } from 'vue'
import { liveConnected, onLive } from '@/composables/useLive'
import type { RequestLog } from '@/api/types'

type Beat = { kind: 'ok' | 'bad'; at: number }

const beat = ref<Beat | null>(null)
let clearTimer: number | null = null

// 一批里只要有失败就闪红：失败比成功更值得被看见，混着到也按坏消息处理
onLive('logs', (items: RequestLog[]) => {
  if (!items || items.length === 0) return
  const bad = items.some((it) => it.status_code < 200 || it.status_code >= 400 || !!it.error)
  // at 参与模板 :key：同一毫秒内的下一批也要重放动画
  beat.value = { kind: bad ? 'bad' : 'ok', at: Date.now() }
  if (clearTimer !== null) window.clearTimeout(clearTimer)
  clearTimer = window.setTimeout(() => {
    beat.value = null
  }, 720)
})

onUnmounted(() => {
  if (clearTimer !== null) window.clearTimeout(clearTimer)
})

// 断线的判断排在 beat 之前：连接断了的时候，「刚有请求完成」这句话本身
// 也可能是几秒前的残留（推送已经收不到了），先说实话更重要。
const title = computed(() => {
  if (!liveConnected.value) return '实时连接已断开，这条脉搏现在不代表真实流量'
  if (beat.value) return beat.value.kind === 'bad' ? '刚有请求失败' : '刚有请求完成'
  return '暂无流量'
})
</script>

<template>
  <div
    class="pulse-bar"
    :class="[beat?.kind, { 'is-offline': !liveConnected, 'hatch-off': !liveConnected }]"
    :title="title"
  >
    <!-- :key 绑时间戳：每来一批就换一个新节点，CSS 动画从头重放 -->
    <span v-if="beat" :key="beat.at" class="pulse-spark" aria-hidden="true" />
  </div>
</template>

<style scoped>
.pulse-bar {
  position: relative;
  height: 3px;
  border-radius: 2px;
  overflow: hidden;
  /* 底轨用主色掺透明而不是再引一个变量：它不是语义色，只是「仪器面板的底」 */
  background: color-mix(in oklab, var(--text-primary-ink) 10%, transparent);
}
.pulse-bar.ok { --pulse-color: var(--color-green); }
.pulse-bar.bad { --pulse-color: var(--color-red); }

/* 断线：底轨换成斜纹（与带宽采集失败共用 .hatch-off，定义在 theme.css），
   明确表达「这条线现在不传递信息」。条纹是 45° 的细线，不抢视线
   （它只占 3px 高）；--pulse-color 同时换成灰，亮带不再闪。 */
.pulse-bar.is-offline {
  --pulse-color: var(--color-text-tertiary);
}

.pulse-spark {
  position: absolute;
  inset: 0;
  background: linear-gradient(90deg, transparent 5%, var(--pulse-color) 50%, transparent 95%);
  filter: drop-shadow(0 0 3px var(--pulse-color));
  animation: pulse-sweep 0.68s ease-out forwards;
}
@keyframes pulse-sweep {
  from { transform: translateX(-100%); }
  to { transform: translateX(100%); }
}
</style>
