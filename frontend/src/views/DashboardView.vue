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
type HeatItem = { day: string; hour: number; requests: number; cost: string; tokens: number }

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
const PALETTE = ['#c87864', '#8b5cf5', '#06b6d4', '#10b37d', '#f59e0b', '#ea4343', '#6b7280', '#3b82f6']

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
    // 「服务状态」卡片移除后，healthz 与 system/info 已无人读取，
    // 一并去掉：它们挂在 Promise.all 里，任何一个失败都会让整个看板报错
    const [s, ts, m, ch, hm] = await Promise.all([
      api.get<Summary>('/stats/summary' + q),
      api.get<{ bucket: string; items: SeriesPoint[] }>('/stats/timeseries' + q),
      api.get<{ items: GroupItem[] }>('/stats/models' + q + '&limit=8'),
      api.get<{ items: GroupItem[] }>('/stats/channels' + q + '&limit=8'),
      api.get<{ items: HeatItem[] }>('/stats/heatmap?days=' + HEAT_DAYS)
    ])
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

// ---- 趋势：消费金额（左轴）+ 请求数（右轴），两条平滑曲线 ----
// 对齐参考站：两条都是带圆点标记的平滑曲线（原来请求数画的是柱状），
// 图例居中在顶部、左右轴各带名称（金额 / 请求），只保留横向虚线网格，
// 金额在左、请求在右。配色取自参考站的 --color-orange / --color-blue。
const trendOption = computed(() => {
  const labels = series.value.map((p) => fmtBucket(p.ts, seriesBucket.value))
  const COST = '#f59e0b'
  const REQ = '#06b6d4'
  // 圆点是空心的：填充用卡片底色、描边用线色
  const lineSeries = (name: string, color: string, data: number[]) => ({
    name,
    type: 'line',
    smooth: true,
    symbol: 'circle',
    symbolSize: 7,
    lineStyle: { width: 2, color },
    itemStyle: { color: '#fff', borderColor: color, borderWidth: 2 },
    data
  })
  const axisName = { color: '#8c8c8c', fontSize: 11 }
  return {
    tooltip: { trigger: 'axis' },
    legend: { data: ['消费金额', '请求数'], top: 0, left: 'center' },
    grid: { left: 54, right: 56, top: 46, bottom: 28 },
    xAxis: {
      type: 'category',
      data: labels,
      // 曲线要从左边缘起笔，不能像柱状图那样两侧留白
      boundaryGap: false,
      axisLine: { lineStyle: { color: '#d9d9d9' } },
      axisTick: { show: false },
      axisLabel: { color: '#8c8c8c', fontSize: 11 }
    },
    yAxis: [
      {
        type: 'value',
        name: '金额',
        nameTextStyle: axisName,
        splitLine: { lineStyle: { color: '#f0f0f0', type: 'dashed' } },
        axisLine: { show: false },
        axisLabel: { color: '#8c8c8c', fontSize: 11 }
      },
      {
        type: 'value',
        name: '请求',
        nameTextStyle: axisName,
        splitLine: { show: false },
        axisLine: { show: false },
        axisLabel: { color: '#8c8c8c', fontSize: 11 }
      }
    ],
    series: [
      lineSeries('消费金额', COST, series.value.map((p) => Number(p.cost))),
      { ...lineSeries('请求数', REQ, series.value.map((p) => p.requests)), yAxisIndex: 1 }
    ]
  }
})

// ---- 消耗分布：输入未命中 / 缓存命中 / 输出 ----
// 词元构成的配色：用主色的深浅阶。
//
// 不复用 PALETTE —— 模型图的颜色表示「身份」（这是哪个模型），
// 这里的颜色表示「构成」（同一批词元分成哪几部分），两套语义共用调色板，
// 会让同一屏上出现「同一个颜色指两件事」：改之前 #c87864 既是
// 「输入（未命中）」又是「deepseek-v4-flash」。
// 深浅阶还顺带表达了输入 -> 缓存 -> 输出的先后关系。
//
// 颜色随类别一起定义，**不能按数组下标取色**：
// 下面会滤掉为 0 的类别，按下标取色的话滤掉一个后面就全部错位
// （模型饼图正是踩了这个坑：gpt-5.6-sol 在两图里显示成两种颜色）。
//
// 名称、颜色、取值三样写在同一项里，是为了让它们不可能对不上：
// 早先的写法把颜色放在一张按名称索引的表里，靠字符串在另一处再匹配一次，
// 改了一处的名字而忘了另一处就会静默退回默认色，不会有任何报错。
const TOKEN_PARTS: { name: string; color: string; pick: (s: Summary | null) => number }[] = [
  { name: '输入（未命中）', color: '#c87864', pick: (s) => s?.prompt_tokens ?? 0 },
  { name: '缓存命中', color: '#e0a090', pick: (s) => s?.cached_tokens ?? 0 },
  { name: '输出', color: '#f2d3c9', pick: (s) => s?.completion_tokens ?? 0 }
]

const compositionOption = computed(() => {
  const s = summary.value
  const data = TOKEN_PARTS.map((p) => ({ name: p.name, value: p.pick(s) }))
    .filter((x) => x.value > 0)
    .map((x) => ({ ...x, itemStyle: { color: TOKEN_PARTS.find((p) => p.name === x.name)?.color } }))
  return {
    tooltip: { trigger: 'item', valueFormatter: (v: number) => n(v) + ' 词元' },
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
// 模型配色：按 byModel 的原始顺序统一分配，条形图与饼图共用同一份。
//
// 两张图必须共用，否则同一个模型会显示成两种颜色：
// 饼图会先滤掉零消耗的模型，如果它自己按 PALETTE 下标取色，
// 只要滤掉一个，它后面所有模型的颜色就整体错位了。
const modelColors = computed(() => {
  const m = new Map<string, string>()
  byModel.value.forEach((x, i) => m.set(x.name, PALETTE[i % PALETTE.length]))
  return m
})

// ---- 模型调用分析：横向条形，按请求数降序 ----
// 两处针对性优化：
//  1. 标签宽度原先交给 containLabel 让 echarts 自己算，窄窗口下算不下时它会
//     把文字直接切在字母中间（截图里出现过 no-such-model- / slow-concurrency-t）。
//     改为固定宽度 + truncate，宁可显示省略号也不要半个字母；
//     112px 足够放下最长的模型名（实测 slow-concurrency-test 在 11px 下 114px，
//     差 2px 时出省略号，比硬切可读）。
//  2. 数据是极端长尾（179 对 2~18），只显示计数的话除首项外都读不出量级，
//     所以在数值后补一个占比。
const modelBarOption = computed(() => {
  const items = [...byModel.value].reverse()
  const total = items.reduce((a, b) => a + b.requests, 0)
  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    grid: { left: 142, right: 78, top: 8, bottom: 8 },
    xAxis: {
      type: 'value',
      splitLine: { lineStyle: { color: '#f0f0f0', type: 'dashed' } },
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: '#8c8c8c', fontSize: 11 }
    },
    yAxis: {
      type: 'category',
      data: items.map((x) => x.name),
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: '#595959', fontSize: 11, width: 132, overflow: 'truncate' }
    },
    series: [
      {
        type: 'bar',
        barMaxWidth: 16,
        itemStyle: { borderRadius: [0, 4, 4, 0] },
        label: {
          show: true,
          position: 'right',
          color: '#8c8c8c',
          fontSize: 11,
          formatter: (p: any) =>
            p.value + (total > 0 ? ' · ' + ((p.value / total) * 100).toFixed(1) + '%' : '')
        },
        // 每个模型一个颜色：既能一眼区分，也便于和右侧饼图里的同名模型对上号
        data: items.map((x) => ({
          value: x.requests,
          itemStyle: { color: modelColors.value.get(x.name) || '#c87864' }
        }))
      }
    ]
  }
})

// ---- 模型消耗占比：按费用 ----
const modelPieOption = computed(() => {
  const data = byModel.value
    .map((x) => ({
      name: x.name,
      value: Number(x.cost),
      itemStyle: { color: modelColors.value.get(x.name) || '#c87864' }
    }))
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
// 热力图：24 小时 × 近 14 天，一格一个 div。
//
// 参考站就是这么做的（docs/layout-dashboard.json 里的 heatmap-body /
// heatmap-cells / heatmap-cell 实测项，display:grid、格子 15x13、圆角 3.2px）。
// 我们原来用 echarts 画 30 天 × 24 小时，有两个问题：
//   1. 面板只有 272px 高，30 行摊下来每行 7px，格子被压成又扁又长的条
//   2. 类目轴每隔 4 天才标一个日期，**最上面那行（今天）恰好轮不到标签**，
//      于是最上方的色带被读成落在前一天 —— 看起来就像坐标轴弄反了
// 改成网格后每行都能标出来，格子的宽高比也和参考站一致。
const HEAT_DAYS = 7

// 从最早到最晚排列：参考站的行标签自上而下是旧 → 新，也就是今天在最下面
const heatDays = computed(() => {
  const out: { key: string; label: string }[] = []
  for (let i = HEAT_DAYS - 1; i >= 0; i--) {
    const d = new Date()
    d.setDate(d.getDate() - i)
    const k = dayKey(d)
    out.push({ key: k, label: k.slice(5) })
  }
  return out
})

const heatMax = computed(() => heat.value.reduce((a, b) => Math.max(a, b.requests), 0))

// 按请求数分 5 档：0 档是中性底色，其余逐级加深主色（与参考站的 level-0..4 一致）
function heatLevel(n: number, max: number) {
  if (!n || n <= 0) return 0
  if (max <= 1) return 4
  const r = n / max
  if (r <= 0.25) return 1
  if (r <= 0.5) return 2
  if (r <= 0.75) return 3
  return 4
}

const heatGrid = computed(() => {
  const byKey = new Map<string, HeatItem>()
  for (const it of heat.value) byKey.set(it.day + '#' + it.hour, it)
  const max = heatMax.value
  return heatDays.value.map((d) => ({
    key: d.key,
    label: d.label,
    cells: Array.from({ length: 24 }, (_, h) => {
      const it = byKey.get(d.key + '#' + h)
      const req = it?.requests ?? 0
      return { hour: h, requests: req, level: heatLevel(req, max) }
    })
  }))
})

// 悬浮提示：移到格子上时显示那一小时的明细（时间 / 请求数 / 消费 / 词元），
// 与参考站一致。用「整块网格共用一个提示框 + 事件委托」，而不是给 168 个格子
// 各挂一个气泡 —— 格子自带 data-key，提示框按被指格子的位置定位。
const heatTip = ref({
  show: false, x: 0, y: 0, day: '', hour: 0, requests: 0, cost: '0', tokens: 0
})

const heatLookup = computed(() => {
  const m = new Map<string, HeatItem>()
  for (const it of heat.value) m.set(it.day + '#' + it.hour, it)
  return m
})

function onHeatOver(e: MouseEvent) {
  const el = (e.target as HTMLElement)?.closest('.heatmap-cell') as HTMLElement | null
  const host = el?.closest('.heatmap') as HTMLElement | null
  if (!el || !host) return
  const key = el.dataset.key
  if (!key) return
  const [day, hour] = key.split('#')
  const it = heatLookup.value.get(key)
  const cr = el.getBoundingClientRect()
  const hr = host.getBoundingClientRect()
  heatTip.value = {
    show: true,
    x: cr.left - hr.left + cr.width / 2,
    y: cr.top - hr.top,
    day,
    hour: Number(hour),
    requests: it?.requests ?? 0,
    cost: it?.cost ?? '0',
    tokens: it?.tokens ?? 0
  }
}

function hideHeatTip() {
  heatTip.value.show = false
}

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
        <StatCard label="消耗金额" :value="'$' + money(summary?.estimated_cost)" tone="orange" hint="按官方单价折算">
          <template #icon><DollarOutlined /></template>
        </StatCard>
        <StatCard label="词元数量" :value="n(summary?.total_tokens)" tone="blue" :hint="'命中率 ' + ((summary?.cache_hit_rate ?? 0) * 100).toFixed(1) + '%'">
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

      <PanelCard title="请求热力图">
        <template #extra>
          <span class="panel-note">{{ n(heatTotal) }} 次请求</span>
        </template>
        <div class="heatmap" @mouseover="onHeatOver" @mouseleave="hideHeatTip">
          <div class="heatmap-corner" />
          <div class="heatmap-col-labels">
            <!-- 每 3 小时标一个，与参考站一致 -->
            <span v-for="h in 24" :key="h" class="heatmap-col-label">
              {{ (h - 1) % 3 === 0 ? h - 1 : '' }}
            </span>
          </div>
          <div class="heatmap-row-labels">
            <span v-for="d in heatGrid" :key="d.key" class="heatmap-row-label">{{ d.label }}</span>
          </div>
          <div class="heatmap-cells">
            <template v-for="d in heatGrid" :key="d.key">
              <div
                v-for="c in d.cells"
                :key="d.key + '-' + c.hour"
                class="heatmap-cell"
                :class="'heatmap-cell-level-' + c.level"
                :data-key="d.key + '#' + c.hour"
              />
            </template>
          </div>
          <div
            v-if="heatTip.show"
            class="heat-tip"
            :style="{ left: heatTip.x + 'px', top: heatTip.y + 'px' }"
          >
            <div class="heat-tip-time">{{ heatTip.day }} {{ heatTip.hour }}:00</div>
            <div>{{ heatTip.requests }} 次请求</div>
            <div>消费 ${{ money(heatTip.cost) }}</div>
            <div>词元 {{ n(heatTip.tokens) }}</div>
          </div>
        </div>
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
    </DataState>
  </div>
</template>

<style scoped>
/* 概览区与图表区均使用 grid，gap 恒为 8px（实测） */
.overview-row {
  display: grid;
  /* 两列等宽，与参考站的 summary-grid 501 + heatmap-panel 501 一致。
     原来是 1fr + 1.35fr，热力图那块被拉宽，格子跟着变形 */
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: var(--gap);
  margin-bottom: var(--gap);
}

/* .panel 全局带 margin-bottom: 8px，作为网格项时会把面板高度吃掉 8px，
   底边比左侧卡片区高出一截。这里清零，并让面板成为纵向 flex，
   好让热力图网格撑满剩余高度而不是靠写死格子高度去凑。 */
.overview-row > :deep(.panel) {
  margin-bottom: 0;
  display: flex;
  flex-direction: column;
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

/* 热力图：一格一个 div 的网格。
   尺寸取自参考站的实测值（docs/layout-dashboard.json 的 heatmap-* 项）：
   区域 gap 4px 8px、格子 gap 3.2px、圆角 3.2px、标签 11.2px。
   格子的宽高比也照参考站（15:13），避免又被压成扁条。 */
.heatmap {
  --heat-gap: 3.2px;
  /* 悬浮提示按相对本容器的坐标定位 */
  position: relative;
  display: grid;
  /* 左上留白角 + 小时标签；下一行是日期标签 + 格子。
     第二行用 1fr，由面板把剩余高度分给格子 —— 这样卡片文案变化、
     面板高度跟着变时，格子会自动适配，不会错位。 */
  grid-template-columns: 36px 1fr;
  grid-template-rows: 18px 1fr;
  gap: 4px 8px;
  flex: 1;
  min-height: 0;
}

/* 列宽用 1fr 让 24 个小时格铺满整行 —— 参考站就是这么做的
   （它的 cells 区 439px 正好等于 24*15 + 23*3.2）。
   原来写死 18px 再居中，网格浮在面板中间、右侧空一大片。 */
.heatmap-col-labels,
.heatmap-cells {
  display: grid;
  grid-template-columns: repeat(24, 1fr);
  gap: var(--heat-gap);
}

.heatmap-row-labels {
  display: grid;
  grid-auto-rows: 1fr;
  gap: var(--heat-gap);
}

.heatmap-col-label {
  font-size: 11.2px;
  line-height: 17.6px;
  color: var(--color-text-secondary);
}

.heatmap-row-label {
  display: flex;
  align-items: center;
  font-size: 11.2px;
  color: var(--color-text-secondary);
}

.heatmap-cell {
  /* 高度由所属网格行决定（1fr），不再写死，
     这样面板变高变矮时格子和日期标签始终对齐 */
  min-height: 10px;
  border-radius: 3.2px;
}

/* 悬浮提示：深色气泡，位置由被指格子算出（左中对齐格子上沿） */
.heat-tip {
  position: absolute;
  z-index: 20;
  pointer-events: none;
  transform: translate(-50%, -100%);
  margin-top: -4px;
  padding: 7px 11px;
  border-radius: 6px;
  background: rgba(0, 0, 0, 0.85);
  color: #fff;
  font-size: 13px;
  line-height: 1.55;
  white-space: nowrap;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.18);
}

/* 底部小三角，指向被指的格子（与参考站一致） */
.heat-tip::after {
  content: '';
  position: absolute;
  left: 50%;
  top: 100%;
  transform: translateX(-50%);
  border: 5px solid transparent;
  border-top-color: rgba(0, 0, 0, 0.85);
}

.heat-tip-time {
  font-weight: 600;
}

/* 五档配色：0 档中性底色，1~4 逐级加深主色（对应参考站的 level-0..4）。
   0 档用 color-mix 把文字色压到 10% 透明度 —— 这在亮色下正好等于
   参考站实测的 rgba(48,48,48,0.1)，暗色下又自动跟着换成浅色，
   比写死字面值或借用 --color-border（偏深）都合适。 */
.heatmap-cell-level-0 { background: color-mix(in srgb, var(--color-text) 10%, transparent); }
.heatmap-cell-level-1 { background: rgba(200, 120, 100, 0.28); }
.heatmap-cell-level-2 { background: rgba(200, 120, 100, 0.52); }
.heatmap-cell-level-3 { background: rgba(200, 120, 100, 0.76); }
.heatmap-cell-level-4 { background: rgb(200, 120, 100); }

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
