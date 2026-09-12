<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  ApiOutlined,
  DollarOutlined,
  ThunderboltOutlined,
  CheckCircleOutlined,
  ReloadOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import PageToolbar from '@/components/PageToolbar.vue'
import PanelCard from '@/components/PanelCard.vue'
import StatCard from '@/components/StatCard.vue'
import EChart from '@/components/EChart.vue'
import DataState from '@/components/DataState.vue'

type Health = { status: string; uptime: string }
type SystemInfo = { version: string; port: number; pricing_sync: number; payload_store: string }
type Summary = {
  requests: number
  success: number
  errors: number
  success_rate: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  cache_creation_tokens: number
  reasoning_tokens: number
  total_tokens: number
  cache_hit_rate: number
  estimated_cost: string
  avg_first_byte_ms: number
  avg_total_ms: number
}
type SeriesPoint = {
  ts: string
  requests: number
  errors: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  cost: string
}
type GroupItem = {
  name: string
  requests: number
  errors: number
  tokens: number
  cost: string
  avg_ms: number
}
type HeatItem = { day: string; hour: number; requests: number }

const health = ref<Health | null>(null)
const info = ref<SystemInfo | null>(null)
const summary = ref<Summary | null>(null)
const series = ref<SeriesPoint[]>([])
const byModel = ref<GroupItem[]>([])
const byChannel = ref<GroupItem[]>([])
const heat = ref<HeatItem[]>([])
const loading = ref(false)
// 这一页没有表格，但「加载失败」同样不能只留一条转瞬即逝的消息：
// 失败后卡片会显示成 0，被读成「这段时间没有流量」
const loadError = ref('')

// 「是否已有统计数据」：任一数据源拿到过内容就算有。
// 用它区分首次加载失败（整块换成错误说明）与刷新失败（保留图表只提示）
const hasStats = computed(
  () =>
    summary.value !== null ||
    series.value.length > 0 ||
    byModel.value.length > 0 ||
    heat.value.length > 0
)
// 趋势图分桶粒度，由后端按时间范围决定（今天/近3天按小时，更长的按天）
const seriesBucket = ref('hour')

const ranges = [
  { key: 'today', label: '今天' },
  { key: '3d', label: '近3天' },
  { key: '7d', label: '近7天' },
  { key: '30d', label: '近30天' }
]
const range = ref('today')

// 与 theme.css 的语义色保持一致，保证图表和界面同色系
const PALETTE = ['#c87864', '#8b5cf5', '#06b6d4', '#10b37d', '#f59e0b', '#ea4343', '#6b7280']

function n(v: number | undefined) {
  return (v ?? 0).toLocaleString('zh-CN')
}

function money(v: string | undefined) {
  const x = Number(v ?? 0)
  if (!x) return '0.0000'
  return x.toFixed(x < 1 ? 6 : 4)
}

function fmtBucket(ts: string, bucket: string) {
  const d = new Date(ts)
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  if (bucket === 'day') return mm + '-' + dd
  return dd + ' ' + String(d.getHours()).padStart(2, '0') + ':00'
}

function dayKey(d: Date) {
  return (
    d.getFullYear() +
    '-' +
    String(d.getMonth() + 1).padStart(2, '0') +
    '-' +
    String(d.getDate()).padStart(2, '0')
  )
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const q = '?range=' + range.value
    const [h, i, s, ts, m, ch, hm] = await Promise.all([
      fetch('/healthz').then((r) => r.json()),
      api.get<SystemInfo>('/system/info'),
      api.get<Summary>('/stats/summary' + q),
      api.get<{ bucket: string; items: SeriesPoint[] }>('/stats/timeseries' + q),
      api.get<{ items: GroupItem[] }>('/stats/models' + q + '&limit=8'),
      api.get<{ items: GroupItem[] }>('/stats/channels' + q + '&limit=8'),
      api.get<{ items: HeatItem[] }>('/stats/heatmap?days=30')
    ])
    health.value = h
    info.value = i
    summary.value = s
    series.value = ts.items || []
    seriesBucket.value = ts.bucket || 'hour'
    byModel.value = m.items || []
    byChannel.value = ch.items || []
    heat.value = hm.items || []
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
  } finally {
    loading.value = false
  }
}

// ---- 趋势：请求数（柱） + 费用（线，双 Y 轴）----
const trendOption = computed(() => {
  const labels = series.value.map((p) => fmtBucket(p.ts, seriesBucket.value))
  return {
    color: PALETTE,
    tooltip: { trigger: 'axis' },
    legend: { data: ['请求数', '费用'], right: 0, top: 0, icon: 'roundRect' },
    grid: { left: 44, right: 52, top: 36, bottom: 28 },
    xAxis: {
      type: 'category',
      data: labels,
      axisLine: { lineStyle: { color: '#d9d9d9' } },
      axisLabel: { color: '#8c8c8c', fontSize: 11 }
    },
    yAxis: [
      {
        type: 'value',
        name: '请求',
        nameTextStyle: { color: '#8c8c8c', fontSize: 11 },
        splitLine: { lineStyle: { color: '#f0f0f0' } },
        axisLabel: { color: '#8c8c8c', fontSize: 11 }
      },
      {
        type: 'value',
        name: 'USD',
        nameTextStyle: { color: '#8c8c8c', fontSize: 11 },
        splitLine: { show: false },
        axisLabel: { color: '#8c8c8c', fontSize: 11 }
      }
    ],
    series: [
      {
        name: '请求数',
        type: 'bar',
        barMaxWidth: 18,
        itemStyle: { color: '#c87864', borderRadius: [3, 3, 0, 0] },
        data: series.value.map((p) => p.requests)
      },
      {
        name: '费用',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        symbolSize: 5,
        itemStyle: { color: '#8b5cf5' },
        data: series.value.map((p) => Number(p.cost))
      }
    ]
  }
})

// ---- 消耗分布：输入未命中 / 缓存命中 / 输出 ----
const compositionOption = computed(() => {
  const s = summary.value
  const data = [
    { name: '输入（未命中）', value: s?.prompt_tokens ?? 0 },
    { name: '缓存命中', value: s?.cached_tokens ?? 0 },
    { name: '输出', value: s?.completion_tokens ?? 0 }
  ].filter((x) => x.value > 0)
  return {
    color: ['#c87864', '#10b37d', '#06b6d4'],
    tooltip: { trigger: 'item', valueFormatter: (v: number) => n(v) + ' tokens' },
    legend: { bottom: 0, icon: 'circle', textStyle: { fontSize: 12 } },
    series: [
      {
        type: 'pie',
        radius: ['48%', '70%'],
        center: ['50%', '44%'],
        avoidLabelOverlap: true,
        label: { formatter: '{d}%', fontSize: 12 },
        data
      }
    ]
  }
})

// ---- 模型调用分析：横向柱状 ----
const modelBarOption = computed(() => {
  const items = [...byModel.value].reverse()
  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    grid: { left: 8, right: 40, top: 10, bottom: 10, containLabel: true },
    xAxis: {
      type: 'value',
      splitLine: { lineStyle: { color: '#f0f0f0' } },
      axisLabel: { color: '#8c8c8c', fontSize: 11 }
    },
    yAxis: {
      type: 'category',
      data: items.map((x) => x.name),
      axisLine: { lineStyle: { color: '#d9d9d9' } },
      axisLabel: { color: '#595959', fontSize: 11 }
    },
    series: [
      {
        type: 'bar',
        barMaxWidth: 14,
        itemStyle: { color: '#c87864', borderRadius: [0, 3, 3, 0] },
        label: { show: true, position: 'right', color: '#8c8c8c', fontSize: 11 },
        data: items.map((x) => x.requests)
      }
    ]
  }
})

// ---- 模型消耗占比：按费用 ----
const modelPieOption = computed(() => {
  const data = byModel.value
    .map((x) => ({ name: x.name, value: Number(x.cost) }))
    .filter((x) => x.value > 0)
  return {
    color: PALETTE,
    tooltip: { trigger: 'item', valueFormatter: (v: number) => '$' + v.toFixed(6) },
    legend: { type: 'scroll', bottom: 0, icon: 'circle', textStyle: { fontSize: 12 } },
    series: [
      {
        type: 'pie',
        radius: '66%',
        center: ['50%', '44%'],
        minShowLabelAngle: 1,
        label: { formatter: '{b} {d}%', fontSize: 11, color: '#595959' },
        labelLine: { length: 8, length2: 8 },
        data: data.length ? data : [{ name: '暂无数据', value: 0 }]
      }
    ]
  }
})

// ---- 热力图：近 30 天 x 24 小时 ----
const heatOption = computed(() => {
  const today = new Date()
  const days: string[] = []
  for (let i = 29; i >= 0; i--) {
    const d = new Date(today)
    d.setDate(d.getDate() - i)
    days.push(dayKey(d))
  }
  const hours = Array.from({ length: 24 }, (_, i) => String(i))
  const data: [number, number, number][] = []
  let max = 0
  for (const it of heat.value) {
    const y = days.indexOf(it.day)
    if (y < 0) continue
    data.push([it.hour, y, it.requests])
    if (it.requests > max) max = it.requests
  }
  return {
    tooltip: {
      position: 'top',
      formatter: (p: any) => days[p.value[1]] + ' ' + p.value[0] + ':00 · ' + p.value[2] + ' 次'
    },
    grid: { left: 62, right: 12, top: 8, bottom: 34 },
    xAxis: {
      type: 'category',
      data: hours,
      splitArea: { show: true },
      axisLabel: { color: '#8c8c8c', fontSize: 10, interval: 2 }
    },
    yAxis: {
      type: 'category',
      data: days.map((d) => d.slice(5)),
      splitArea: { show: true },
      axisLabel: { color: '#8c8c8c', fontSize: 11, interval: 3 }
    },
    visualMap: {
      min: 0,
      max: max || 1,
      calculable: false,
      orient: 'horizontal',
      left: 'center',
      bottom: 0,
      itemWidth: 12,
      itemHeight: 80,
      textStyle: { color: '#8c8c8c', fontSize: 10 },
      inRange: { color: ['#f5efe9', '#e0b3a4', '#c87864', '#9c4f3c'] }
    },
    series: [
      {
        type: 'heatmap',
        data,
        itemStyle: { borderColor: '#fff', borderWidth: 1 },
        emphasis: { itemStyle: { shadowBlur: 6, shadowColor: 'rgba(0,0,0,0.2)' } }
      }
    ]
  }
})

const heatTotal = computed(() => heat.value.reduce((a, b) => a + b.requests, 0))

// 时间范围改由 a-radio-group 的 v-model 直接更新，
// 它的 change 只在取值真的变化时触发，所以这里不需要再判一次重


onMounted(load)
</script>

<template>
  <div class="dashboard">
    <!-- 工具栏：时间范围 + 刷新（对齐参考站 dashboard-toolbar） -->
    <PageToolbar label="时间范围">
      <!-- 用 a-radio-group 而不是手写 <button>：
           这里原来写的是 class="pill-btn"，但那个类在项目里从未定义过，
           于是按钮一直是浏览器默认样式（灰底、深色描边、字号偏小），
           和其余部分完全不像一套东西。
           换成 Ant Design 的组件还能自动跟随明暗主题与设计令牌。 -->
      <a-radio-group v-model:value="range" button-style="solid" @change="load">
        <a-radio-button v-for="r in ranges" :key="r.key" :value="r.key">{{ r.label }}</a-radio-button>
      </a-radio-group>
      <template #right>
        <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
      </template>
    </PageToolbar>

    <DataState
      :error="loadError"
      :has-data="hasStats"
      :loading="loading"
      title="看板数据加载失败"
      hint="看板数据来自后端统计接口，请确认后端服务是否正常，然后重试。"
      @retry="load"
    >
    <!-- 概览四卡 -->
    <section class="overview-row">
      <div class="summary-grid">
        <StatCard label="请求数量" :value="n(summary?.requests)" tone="purple" :hint="'失败 ' + n(summary?.errors) + ' 次'">
          <template #icon><ApiOutlined /></template>
        </StatCard>
        <StatCard label="预估金额" :value="'$' + money(summary?.estimated_cost)" tone="orange" hint="仅供参考，非实际扣费">
          <template #icon><DollarOutlined /></template>
        </StatCard>
        <StatCard label="Token 量" :value="n(summary?.total_tokens)" tone="blue" :hint="'命中率 ' + ((summary?.cache_hit_rate ?? 0) * 100).toFixed(1) + '%'">
          <template #icon><ThunderboltOutlined /></template>
        </StatCard>
        <StatCard
          label="成功率"
          :value="summary ? (summary.success_rate * 100).toFixed(1) + '%' : '--'"
          tone="green"
          :hint="'平均首包 ' + Math.round(summary?.avg_first_byte_ms ?? 0) + 'ms'"
        >
          <template #icon><CheckCircleOutlined /></template>
        </StatCard>
      </div>

      <PanelCard title="请求热力图（近 30 天）">
        <template #extra>
          <span class="panel-note">{{ n(heatTotal) }} 次请求</span>
        </template>
        <EChart :option="heatOption" height="272px" />
      </PanelCard>
    </section>

    <!-- 图表区 -->
    <section class="chart-grid">
      <PanelCard title="消耗趋势">
        <EChart :option="trendOption" height="260px" />
      </PanelCard>
      <PanelCard title="消耗分布">
        <EChart :option="compositionOption" height="260px" />
      </PanelCard>
      <PanelCard title="模型调用分析">
        <EChart :option="modelBarOption" height="260px" />
      </PanelCard>
      <PanelCard title="模型消耗占比">
        <EChart :option="modelPieOption" height="260px" />
      </PanelCard>
    </section>

    <!-- 运行状态（本站自检，参考站无此项） -->
    <PanelCard title="服务状态">
      <a-descriptions :column="2" size="small">
        <a-descriptions-item label="服务">
          <a-tag :color="health ? 'green' : 'red'">{{ health ? '运行中' : '不可用' }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="运行时长">{{ health?.uptime ?? '-' }}</a-descriptions-item>
        <a-descriptions-item label="监听端口">{{ info?.port ?? '-' }}</a-descriptions-item>
        <a-descriptions-item label="版本">{{ info?.version ?? '-' }}</a-descriptions-item>
        <a-descriptions-item label="定价同步间隔">{{ info?.pricing_sync ?? '-' }} 小时</a-descriptions-item>
        <a-descriptions-item label="报文留存">{{ info?.payload_store ?? '-' }}</a-descriptions-item>
      </a-descriptions>
    </PanelCard>
    </DataState>
  </div>
</template>

<style scoped>
/* 概览区与图表区均使用 grid，gap 恒为 8px（实测） */
.overview-row {
  display: grid;
  grid-template-columns: minmax(340px, 1fr) minmax(420px, 1.35fr);
  gap: var(--gap);
  margin-bottom: var(--gap);
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: var(--gap);
}

.chart-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--gap);
  margin-bottom: var(--gap);
}

.panel-note {
  color: var(--color-text-secondary);
  font-size: 13px;
}

/* DataState 的错误提示自带左右外边距（为列表页的面板布局设计），
   这里外层已经有内边距，去掉以免出现双重缩进 */
.dashboard :deep(.ds-alert) { margin: 0 0 var(--gap); }

@media (max-width: 1100px) {
  .overview-row,
  .chart-grid {
    grid-template-columns: 1fr;
  }
}
</style>
