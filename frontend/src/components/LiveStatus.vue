<script setup lang="ts">
// 实时连接状态胶囊（2026-10-11）。
//
// ---- 为什么需要它 ----
//
// useLive.ts 从建立起就导出了 liveConnected，注释里写着「界面上用它显示
// 实时/已断开」—— 但全仓一个消费者都没有，功能从没落地。后果是：后端重启、
// 笔记本休眠唤醒、NAT 超时之后，看板上的数字、脉搏条、日志列表一起冻住，
// 界面与「今天真的没流量」完全一样；而脉搏条还会主动说「暂无流量」，
// 把用户引去查渠道和上游 —— 真正的故障是「连接断了」，与流量无关。
//
// 断线本身会自动重连（指数退避 1s→15s，见 useLive.scheduleReconnect），
// 所以这里要做的是两件事：**说出来**、并给一个「现在，别等」的按钮。
//
// ---- 为什么不说「N 秒前更新」 ----
//
// 服务端 25 秒发一次空心跳（闲时也发），所以「最后一帧」的年龄会稳定地
// 每 25 秒推进一次 —— 把它显示成「数据 N 秒前更新」是在说假话：那只是心跳，
// 数据一帧都没变。这里只说连接状态，不说数据新鲜度。
//
// 三档与阈值都在 useLive.liveStateOf 里（有单测）：live / stale / offline。
// stale 那一档是「连着但 35 秒没收到任何帧」，比 45 秒的看门狗判死早 10 秒 ——
// 给用户一个预警窗口，而不是直接跳到「已断开」。
import { computed, onUnmounted, ref } from 'vue'
import { CheckCircleOutlined, DisconnectOutlined } from '@ant-design/icons-vue'
import { liveConnected, liveLastMessageAt, liveStateOf, reconnect, STALE_HINT_MS } from '@/composables/useLive'

// 每 5 秒重算一次「多久没收到帧」。
//
// 为什么需要这个 ticker：liveStateOf 的判据里有 now，而 Date.now() 不是响应式的
// —— 没有它，live → stale 这一档永远不会自己发生（只有收到新帧或断开时才会重算）。
// 只在**连着**的时候跑：断开时状态由 liveConnected 直接给出，不必空转。
// 5 秒的粒度对 35 秒的阈值足够（最坏晚 5 秒显示），而每秒一次纯属浪费。
const now = ref(Date.now())
const ticker = window.setInterval(() => {
  if (liveConnected.value) now.value = Date.now()
}, 5000)
onUnmounted(() => window.clearInterval(ticker))

const state = computed(() => liveStateOf(liveConnected.value, liveLastMessageAt.value, now.value))

const text = computed(() => {
  if (state.value === 'offline') return '实时连接已断开'
  if (state.value === 'stale') return `实时连接已 ${Math.round(STALE_HINT_MS / 1000)} 秒无数据`
  return '实时'
})

const title = computed(() =>
  state.value === 'live'
    ? '实时连接正常：看板数字与日志会随新请求自动更新'
    : state.value === 'stale'
      ? '连接还在，但已经很久没收到任何数据（含心跳）。可能是网络中断或后端异常'
      : '实时连接已断开，界面上的数字可能不是最新的。会自动重连，也可以点右侧立即重试'
)
</script>

<template>
  <!-- role=status + aria-live=polite：状态变化要能被读屏听到。
       视觉上「数字停止跳动」是断线的唯一线索，而这个线索对读屏用户不存在。 -->
  <span class="live-status" :class="'is-' + state" role="status" aria-live="polite" :title="title">
    <CheckCircleOutlined v-if="state === 'live'" class="ls-icon" aria-hidden="true" />
    <DisconnectOutlined v-else class="ls-icon" aria-hidden="true" />
    <span class="ls-text">{{ text }}</span>
    <!-- 「重试」只在非正常态出现：正常时多一个按钮只是噪音，
         而异常时它是唯一的出路（退避最坏要等 15 秒） -->
    <button v-if="state !== 'live'" type="button" class="ls-retry" @click="reconnect()">重试</button>
  </span>
</template>

<style scoped>
/* 一枚小胶囊，与脉搏条同高、同一行右侧。配色全走语义令牌：
   --text-green / --text-amber / --text-red 都是各自主题里过了 AA 的正文色
   （--color-green 那类只保证 3:1，用在文字上不够 —— 见 theme.css 的令牌说明）。 */
.live-status {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  background: var(--color-fg);
  font-size: var(--font-size-sm);
  line-height: 18px;
  white-space: nowrap;
}

.ls-icon {
  /* 走字号刻度（--font-size-2xs = 10px）：契约检查禁止组件里写死 px 字号，
     否则改刻度时这一处会掉队 */
  font-size: var(--font-size-2xs);
}

.live-status.is-live {
  color: var(--text-green);
}

.live-status.is-stale {
  color: var(--text-amber);
  border-color: var(--text-amber);
}

.live-status.is-offline {
  color: var(--text-red);
  border-color: var(--text-red);
}

/* 正常态不需要那个占位的对勾图标参与排版 */
.live-status .ls-text {
  font-variant-numeric: tabular-nums;
}

.ls-retry {
  margin-left: 2px;
  padding: 0 4px;
  border: none;
  border-radius: var(--radius-control);
  background: transparent;
  color: inherit;
  font-family: inherit;
  font-size: var(--font-size-sm);
  text-decoration: underline;
  cursor: var(--cursor-hand);
}

.ls-retry:hover {
  background: var(--color-icon-hover-bg);
}
</style>
