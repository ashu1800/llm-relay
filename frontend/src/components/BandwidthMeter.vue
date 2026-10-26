<script setup lang="ts">
// 服务器带宽读数（2026-10-23）：实时速率 + 过去一分钟的迷你曲线。
//
// ---- 它回答什么问题 ----
//
// 看板上其余数字都在说「网关处理了多少请求」，这一格说的是
// 「这台机器现在还有多少带宽余量」—— 它包含备份同步、系统更新、SSH 传输、
// 以及所有别的进程。两者不是一回事：网关流量很小而带宽打满时，
// 问题在上游连接或磁盘同步，不在渠道配置。
//
// ---- 为什么放在脉搏条那一行 ----
//
// 看板整页锁在一屏里（见 DashboardView 的 .dashboard 说明），日志面板吃掉
// 剩余高度。再加一块卡片或面板会把表格压矮，而这一格的信息量配不上那代价 ——
// 它是个「仪器读数」，与旁边的脉搏条、连接状态胶囊同属一类，排在同一行最自然。
//
// ---- 三种状态必须分开（见 bandwidth.ts 的 bandwidthState）----
//
//   live       有流量：显示速率 + 曲线
//   idle       真的没流量：曲线照画（贴底），读数显示 0 B/s
//   unavailable 采集不到：显示「—」与原因。**不能显示成 0** ——
//              空闲站点整夜都是 0，两者混在一起时采集坏了永远发现不了。
//
// 展开后能看到收 / 发分开的速率、本进程观测到的累计流量、以及网卡名。
// 网卡名是排障时第一个要问的问题（「我明明走了专线，读数怎么是 0」），
// 但它平时不需要占位置，所以收在展开区里。
import { computed, onUnmounted, ref } from 'vue'
import { SwapOutlined } from '@ant-design/icons-vue'
import { onLive } from '@/composables/useLive'
import {
  bandwidthState,
  curveCeiling,
  dualBandwidthPoints,
  formatBytes,
  formatRate,
  mergeBandwidth,
  pointTotal,
  polylinePoints,
  type BandwidthFrame,
  type BandwidthPoint
} from '@/components/bandwidth'

const points = ref<BandwidthPoint[]>([])

onLive('bandwidth', (frame: BandwidthFrame) => {
  if (!frame?.latest) return
  points.value = mergeBandwidth(points.value, frame)
})

// latest 是 points 的派生值，不再单独维护一个 ref：
// mergeBandwidth 的两条分支（替换历史 / 追加一拍）都以「最后一拍」为最新，
// 两个 ref 分开赋值只是让这个不变式靠两行相邻代码维持。
const latest = computed(() => points.value[points.value.length - 1] ?? null)

const state = computed(() => bandwidthState(latest.value))

// 主读数显示的是「收 + 发」：站主问的是「带宽用了多少」，
// 而单看其中一个方向会把「上行打满」的备份同步显示成 0。
const total = computed(() => (latest.value ? pointTotal(latest.value) : 0))
const rx = computed(() => (latest.value?.available ? latest.value.rx_bytes_per_sec : 0))
const tx = computed(() => (latest.value?.available ? latest.value.tx_bytes_per_sec : 0))

const text = computed(() => {
  if (state.value === 'unavailable') return '—'
  return formatRate(total.value)
})

const title = computed(() => {
  if (state.value === 'unavailable') {
    const why = latest.value?.error || '还没有收到读数'
    return `读不到网卡流量：${why}`
  }
  const iface = latest.value?.interface ? `网卡 ${latest.value.interface}` : '服务器'
  return `${iface} 当前实时速率（收 + 发），曲线为最近一分钟`
})

// ---- 曲线 ----
//
// 用内联 SVG 而不是引图表库：这是一条 60 个点的折线，
// 而站点至今没有图表依赖（消耗趋势等图表被移除后就没有了）。
// 为一条 24px 高的曲线引入 ECharts 是拿 900 KB 换十几行代码。
const CURVE_W = 96
const CURVE_H = 22

const dual = computed(() => dualBandwidthPoints(points.value, CURVE_W, CURVE_H, curveCeiling(points.value)))
const line = computed(() => dual.value.rxLine || dual.value.txLine)

// ---- 展开明细 ----
const expanded = ref(false)
// 展开态在 8 秒后自动收回：它是个「看一眼」的读数，
// 忘了收起来会把这一行撑高、把日志面板挤下去
let collapseTimer: number | null = null
function expand() {
  expanded.value = true
  if (collapseTimer !== null) window.clearTimeout(collapseTimer)
  collapseTimer = window.setTimeout(() => {
    expanded.value = false
  }, 8000)
}
function collapse() {
  if (collapseTimer !== null) window.clearTimeout(collapseTimer)
  collapseTimer = null
  expanded.value = false
}
onUnmounted(() => {
  if (collapseTimer !== null) window.clearTimeout(collapseTimer)
})
</script>

<template>
  <!-- 根节点吃 hover 与 focus：鼠标移上去展开明细，键盘 Tab 也能到达。
       用鼠标移出收起而不是点击切换 —— 它是一个读数，不是开关。 -->
  <div
    class="bw"
    role="group"
    aria-label="服务器带宽"
    tabindex="0"
    :title="title"
    @mouseenter="expand()"
    @mouseleave="collapse()"
    @focus="expand()"
    @blur="collapse()"
  >
    <span class="bw-label">带宽</span>

    <!-- 曲线：不可用时整条换成斜纹底（与脉搏条断线同一语言），
         表示「这条线现在不传递信息」。
         注意 v-if 里必须同时排除 unavailable：只判 line 非空的话，
         曲线会画成一条贴着底边的直线 —— 那看起来像「一直没流量」，
         正是这个功能最不该有的误读（见 bandwidthState 的说明）。 -->
    <span class="bw-curve" aria-hidden="true">
      <svg
        v-if="line && state !== 'unavailable'"
        :viewBox="`0 0 ${CURVE_W} ${CURVE_H}`"
        :width="CURVE_W"
        :height="CURVE_H"
        preserveAspectRatio="none"
      >
        <polygon v-if="dual.rxArea" class="bw-area is-rx" :points="dual.rxArea" />
        <polyline v-if="dual.rxLine" class="bw-line is-rx" :points="dual.rxLine" />
        <polygon v-if="dual.txArea" class="bw-area is-tx" :points="dual.txArea" />
        <polyline v-if="dual.txLine" class="bw-line is-tx" :points="dual.txLine" />
      </svg>
      <span v-else-if="state === 'unavailable'" class="bw-curve-off hatch-off" />
    </span>

    <span class="bw-value">{{ text }}</span>

    <!-- 明细：收 / 发分开、累计、网卡名。展开时**绝对定位**浮在这一行下方，
         而不是作为 flex 项挤进来 —— 实测（2026-10-23，展开后总宽 502px）
         挤进来会把曲线压成一条 4px 宽的竖条，而曲线正是这一格的主视觉。 -->
    <Transition name="bw-detail">
      <span v-if="expanded && latest" class="bw-detail">
        <span class="bw-item" title="下行（接收）">
          <SwapOutlined class="bw-dir is-down" aria-hidden="true" />
          {{ formatRate(rx) }}
        </span>
        <span class="bw-item" title="上行（发送）">
          <SwapOutlined class="bw-dir is-up" aria-hidden="true" />
          {{ formatRate(tx) }}
        </span>
        <span class="bw-item bw-total" title="本进程启动以来观测到的累计流量">
          ↓{{ formatBytes(latest.rx_total_bytes) }} ↑{{ formatBytes(latest.tx_total_bytes) }}
        </span>
        <span v-if="latest.interface" class="bw-iface">{{ latest.interface }}</span>
      </span>
    </Transition>
  </div>
</template>

<style scoped>
/* 读数条：与脉搏条同一行、同一视觉重量 —— 它是仪器外壳，不是内容卡片。
   常态只占 label + 曲线 + 数值，高度由脉搏条（3px）撑出来的行高决定。 */
.bw {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: 0 0 auto;
  padding: 1px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  /* 明细是绝对定位的浮层，需要它作为定位基准 */
  position: relative;
  /* 焦点环走全站 :focus-visible（theme.css） */
  outline: none;
}

.bw-label {
  font-size: var(--font-size-sm);
  color: var(--color-text-tertiary);
  /* 不需要换行：一行里它是固定的三字标题 */
  white-space: nowrap;
}

/* 数值用正文色而不是语义色：带宽没有「好 / 坏」两端，
   染成绿色会暗示「越大越好」，而现实中打满带宽恰恰是问题。 */
.bw-value {
  font-size: var(--font-size-sm);
  font-weight: 600;
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  /* 数字变化时宽度会跳（1.0 KB/s ↔ 120 KB/s），固定一个够宽的最小宽度，
     避免每帧都在推挤右侧的波形 */
  min-width: 62px;
  text-align: right;
}

.bw-curve {
  display: inline-flex;
  width: 96px;
  height: 22px;
  /* 曲线描边允许溢出到 viewBox 外一点点，但不要影响行高 */
  overflow: hidden;
}
.bw-curve svg {
  display: block;
}

.bw-line {
  fill: none;
  stroke: var(--color-blue);
  stroke-width: 1.1;
  stroke-linejoin: round;
  stroke-linecap: round;
  /* 非等比缩放下描边会被横向拉细，vector-effect 让它保持 1.1px */
  vector-effect: non-scaling-stroke;
}
.bw-line.is-rx {
  stroke: var(--color-blue);
}
.bw-line.is-tx {
  stroke: var(--color-orange);
}

.bw-area {
  stroke: none;
}
.bw-area.is-rx {
  fill: color-mix(in oklab, var(--color-blue) 14%, transparent);
}
.bw-area.is-tx {
  fill: color-mix(in oklab, var(--color-orange) 14%, transparent);
}

/* 不可用：斜纹底，与脉搏条断线同一视觉语言 —— 类名 hatch-off 加在模板上
   （定义在 theme.css），这里只负责尺寸。
   不用红色 —— 采集不到不等于服务出问题（macOS 开发机上永远如此），
   红色会让它看起来像故障告警。 */
.bw-curve-off {
  display: block;
  width: 100%;
  height: 100%;
}

/* 向上弹：这一行下方紧挨着概览卡片（实测浮层 bottom=125 会压住卡片标题），
   而上方是工具栏的留白。放在上面既不遮卡片，也不与脉搏条抢那一行的高度。 */
.bw-detail {
  position: absolute;
  bottom: calc(100% + 6px);
  right: 0;
  z-index: 20;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  /* 浮层要有自己的底与描边：它压在下方的卡片上，
     半透明会让下面那行数字透上来（实测很花） */
  background: var(--color-fg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  box-shadow: var(--color-fg-shadow);
  font-size: var(--font-size-sm);
  color: var(--color-text-tertiary);
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.bw-item {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

/* 收发方向：同一枚图标转 90°。下行（接收）朝下、上行（发送）朝上 ——
   用旋转而不是两枚图标，少一次引入，两个方向的形状也天然成对。 */
.bw-dir {
  font-size: var(--font-size-sm);
  color: var(--color-text-placeholder);
}
.bw-dir.is-down {
  transform: rotate(90deg);
}
.bw-dir.is-up {
  transform: rotate(-90deg);
}

.bw-total {
  color: var(--color-text-placeholder);
}

.bw-iface {
  color: var(--color-text-placeholder);
}

/* 明细展开：高度与透明度一起过渡，避免这一行突然长高把下面推一下 */
.bw-detail-enter-active,
.bw-detail-leave-active {
  transition: opacity 0.15s ease;
}
.bw-detail-enter-from,
.bw-detail-leave-to {
  opacity: 0;
}

/* 窄屏：曲线与明细都收起来，只留「带宽 + 数值」。
   640 以下这一行还要并排容纳连接状态胶囊，曲线放不下会把它挤出去。 */
@media (max-width: 640px) {
  .bw-curve,
  .bw-detail {
    display: none;
  }
  .bw-value {
    min-width: 0;
  }
}
</style>
