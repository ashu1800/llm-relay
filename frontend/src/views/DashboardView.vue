<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
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
import AnimatedNumber from '@/components/AnimatedNumber.vue'
import { onLive } from '@/composables/useLive'
// 金额一律走 utils/money.ts：符号与小数位数只此一份（见那里的说明）
import { costsText, currencyKeys, moneyText, primaryCurrency, symbolOf } from '@/utils/money'
import EChart from '@/components/EChart.vue'
import { useChartTheme } from '@/utils/chartTheme'
import DataState from '@/components/DataState.vue'
import type { Channel, ChannelGroup } from '@/api/types'
import { readStoredChoice, writeStoredChoice } from '@/utils/persistedChoice'
import { channelOption } from '@/utils/channelOption'
import ChannelOption from '@/components/ChannelOption.vue'

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
  /**
   * 金额按币种分开：{"CNY":"12.34","USD":"5.67"}。
   * 不同币种不能相加（人民币和美元之间没有汇率），所以看板只分别统计，
   * 不做任何折算 —— 见 utils/money.ts
   */
  costs: Record<string, string>
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
  costs: Record<string, string>
}
type GroupItem = {
  name: string
  requests: number
  errors: number
  tokens: number
  costs: Record<string, string>
  avg_ms: number
}
type HeatItem = { day: string; hour: number; requests: number; costs: Record<string, string>; tokens: number }

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
// ---- 筛选条件（时间范围 / 分组 / 渠道）全部持久化 ----
//
// 为什么要持久化：「只看某个分组」是常态视角，每次打开页面、或从别的页面
// 切回来都要重选一遍，是纯粹的重复劳动。与渠道列表页的筛选同一套做法
// （见 utils/persistedChoice.ts）。
const RANGE_KEY = 'dashboard-range'
const GROUP_KEY = 'dashboard-group'
const CHANNEL_KEY = 'dashboard-channel'

// 哨兵值用 'all' 而不是 0：后端的约定是「不传参数＝不筛选」，
// 而界面上的「全部分组」与「分组 id=0」是两件事，混用迟早出错
const ALL = 'all'

const range = ref('today')
const groupFilter = ref<string>(ALL)
const channelFilter = ref<string>(ALL)
// 筛选下拉的候选：来自管理接口，不是统计接口 —— 统计接口只回有流量的渠道，
// 而「筛一条今天还没被用过的渠道」是合理需求（结果就是 0）
const filterGroups = ref<ChannelGroup[]>([])
const filterChannels = ref<Channel[]>([])

const groupOptions = computed(() => [
  { value: ALL, label: '全部分组' },
  ...filterGroups.value.map((g) => ({ value: String(g.id), label: g.name }))
])

// 渠道选项：图标 + 名字，分组名只在「全部分组」时才补上
// （见 utils/channelOption.ts，请求日志页用的是同一个函数）
const visibleChannels = computed(() =>
  groupFilter.value === ALL
    ? filterChannels.value
    : filterChannels.value.filter((c) => String(c.group_id) === groupFilter.value)
)

const channelOptions = computed(() => [
  { value: ALL, label: '全部渠道' },
  ...visibleChannels.value.map((c) => channelOption(c, filterGroups.value, groupFilter.value === ALL))
])

function persistFilters() {
  writeStoredChoice(RANGE_KEY, range.value)
  writeStoredChoice(GROUP_KEY, groupFilter.value)
  writeStoredChoice(CHANNEL_KEY, channelFilter.value)
}

// 存下来的筛选值可能指向已经删掉的分组 / 渠道。那种状态的表现是
// 「所有数字都是 0」，从界面上完全看不出原因 —— 所以列表到手后校验一次，
// 不合法就退回「全部」（与渠道列表页的 applyStoredGroupFilter 同一套做法）。
function applyStoredFilters() {
  range.value = readStoredChoice(RANGE_KEY, ranges.map((r) => r.key), 'today')
  groupFilter.value = readStoredChoice(
    GROUP_KEY,
    [ALL, ...filterGroups.value.map((g) => String(g.id))],
    ALL
  )
  // 渠道的允许集合按「当前分组下可见的渠道」算，否则会恢复出
  // 「分组 A + 属于 B 的渠道」这种共存状态
  channelFilter.value = readStoredChoice(
    CHANNEL_KEY,
    [ALL, ...visibleChannels.value.map((c) => String(c.id))],
    ALL
  )
  persistFilters()
}

async function loadFilters() {
  try {
    const [g, c] = await Promise.all([
      api.get<{ items: ChannelGroup[] }>('/groups'),
      api.get<{ items: Channel[] }>('/channels')
    ])
    filterGroups.value = g.items || []
    filterChannels.value = c.items || []
  } catch {
    // 拉不到就只剩「全部」两个选项，看板本身照常取数 ——
    // 一个筛选框不该让整页打不开
  }
  applyStoredFilters()
}

// 注意：a-radio-group 的 change 给的是**事件对象**，不是值（a-select 给的是值，
// 两者不一样）。所以这里不接收参数、也不赋值 —— v-model 已经更新过 range。
// 之前写成 onRangeChange(v) { range.value = v }，range 就变成了一个事件对象：
// 查询串成了 ?range=[object Object]，后端认不出、退回「今天」，
// 存储里也写进 "[object Object]" —— 界面上筛选项看着是选中的，数据却是今天的。
function onRangeChange() {
  persistFilters()
  load()
}

function onGroupChange(v: string) {
  groupFilter.value = v
  // 换分组后原来选的渠道可能不属于新分组，那组组合查出来永远是 0
  if (!channelOptions.value.some((o) => o.value === channelFilter.value)) {
    channelFilter.value = ALL
  }
  persistFilters()
  load()
}

function onChannelChange(v: string) {
  channelFilter.value = v
  persistFilters()
  load()
}

// 只有筛选条件、不含时间范围：热力图的时间轴是固定的近 7 天，不吃 range。
// 分组 / 渠道不选时不带参数（后端把「不传」当作不筛选）
function filterSuffix() {
  let s = ''
  if (groupFilter.value !== ALL) s += '&group_id=' + groupFilter.value
  if (channelFilter.value !== ALL) s += '&channel_id=' + channelFilter.value
  return s
}

function statsQuery() {
  return '?range=' + range.value + filterSuffix()
}


// 与 theme.css 的语义色保持一致，保证图表和界面同色系
// 图表配色跟着主题走：option 里不再写死颜色（详见 utils/chartTheme.ts）
const ct = useChartTheme()

const PALETTE = ['#c87864', '#8b5cf5', '#06b6d4', '#10b37d', '#f59e0b', '#ea4343', '#6b7280', '#3b82f6']

function n(v: number | undefined) {
  return (v ?? 0).toLocaleString('zh-CN')
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
    const q = statsQuery()
    // 「服务状态」卡片移除后，healthz 与 system/info 已无人读取，
    // 一并去掉：它们挂在 Promise.all 里，任何一个失败都会让整个看板报错
    const [s, ts, m, ch, hm] = await Promise.all([
      api.get<Summary>('/stats/summary' + q),
      api.get<{ bucket: string; items: SeriesPoint[] }>('/stats/timeseries' + q),
      api.get<{ items: GroupItem[] }>('/stats/models' + q + '&limit=8'),
      api.get<{ items: GroupItem[] }>('/stats/channels' + q + '&limit=8'),
      api.get<{ items: HeatItem[] }>('/stats/heatmap?days=' + HEAT_DAYS + filterSuffix())
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
// 趋势里出现过的币种：整段区间取并集，而不是只看某一条点 ——
// 只看当前点的话，图例会随着数据来回闪。
const trendCurrencies = computed(() => {
  const seen: Record<string, string> = {}
  for (const p of series.value) for (const c of Object.keys(p.costs ?? {})) seen[c] = ''
  return currencyKeys(seen)
})
const trendLegend = computed(() => trendCurrencies.value.map((c) => '消费 ' + symbolOf(c)).concat(['请求数']))
// 第一条沿用原来的消费色（看板上「钱」一直是这个橙色）
const TREND_COLORS = ['#f59e0b', '#8b5cf5', '#06b6d4', '#10b37d']

const trendOption = computed(() => {
  const labels = series.value.map((p) => fmtBucket(p.ts, seriesBucket.value))
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
  const axisName = { color: ct.value.secondary, fontSize: 11 }
  return {
    tooltip: { trigger: 'axis' },
    legend: { data: trendLegend.value, top: 0, left: 'center', textStyle: { color: ct.value.text } },
    grid: { left: 54, right: 56, top: 46, bottom: 28 },
    xAxis: {
      type: 'category',
      data: labels,
      // 曲线要从左边缘起笔，不能像柱状图那样两侧留白
      boundaryGap: false,
      axisLine: { lineStyle: { color: ct.value.border } },
      axisTick: { show: false },
      axisLabel: { color: ct.value.secondary, fontSize: 11 }
    },
    yAxis: [
      {
        type: 'value',
        // 筛选把币种钉死时把符号写进轴名：这时左轴上的数只可能是那一种钱
        name: scopeCurrency.value ? '金额（' + symbolOf(scopeCurrency.value) + '）' : '金额',
        nameTextStyle: axisName,
        splitLine: { lineStyle: { color: ct.value.split, type: 'dashed' } },
        axisLine: { show: false },
        axisLabel: { color: ct.value.secondary, fontSize: 11 }
      },
      {
        type: 'value',
        name: '请求',
        nameTextStyle: axisName,
        splitLine: { show: false },
        axisLine: { show: false },
        axisLabel: { color: ct.value.secondary, fontSize: 11 }
      }
    ],
    series: [
      // 每个币种单独一条线，名称里带符号。刻意**不**给第二个币种开第二根 Y 轴：
      // 两根轴会让「谁更高」变成由画法决定，而不是由数据决定；
      // 同一条轴上至少各自的趋势读得对。
      ...trendCurrencies.value.map((c, i) =>
        lineSeries(
          '消费 ' + symbolOf(c),
          TREND_COLORS[i % TREND_COLORS.length],
          series.value.map((p) => Number(p.costs?.[c] ?? 0))
        )
      ),
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

// 实时推送的数值：金额与成功率不是整数，滚动组件用 format 预设走不同的格式化。
// 用 computed 而不是直接传字符串：滚动需要的是**数字**，
// 传 "12.3%" 过去它没法补间
// 金额：主币种给大数字，其余币种并排写在提示里。
// 之所以不做「合计」：SUM 只在同一币种内成立，把人民币和美元加起来会得到一个
// 既不是人民币也不是美元的数，而且账面上看不出任何异常。
const costCurrencies = computed(() => currencyKeys(summary.value?.costs))
// 筛选范围把币种钉死时就用它，否则按原来的规则（CNY 优先，其余进提示）。
//
// 为什么需要：筛到一条只记美元账的渠道时，若还按「人民币优先」，
// 大数字会是 ¥0.0000、真实金额被塞进「另有 $…」—— 同一块卡片上，
// 最显眼的位置显示的是一个恒为 0 的数。
const scopeCurrency = computed(() => {
  if (channelFilter.value !== ALL) {
    const c = filterChannels.value.find((x) => String(x.id) === channelFilter.value)
    return (c?.currency ?? '').toUpperCase()
  }
  if (groupFilter.value !== ALL) {
    const set = new Set(
      filterChannels.value
        .filter((c) => String(c.group_id) === groupFilter.value)
        .map((c) => (c.currency ?? '').toUpperCase())
    )
    // 分组里混着两种币就不猜：交给 primaryCurrency，硬挑一个会让另一半金额
    // 看起来像不存在
    if (set.size === 1) return [...set][0]
  }
  return ''
})
const costCur = computed(() => scopeCurrency.value || primaryCurrency(summary.value?.costs))
const costValue = computed(() => (summary.value ? Number(summary.value.costs?.[costCur.value] ?? 0) : null))
const costHint = computed(() => {
  const rest = costCurrencies.value.filter((c) => c !== costCur.value)
  if (!rest.length) return '按渠道币种计，不折算'
  const text = rest.map((c) => moneyText(summary.value?.costs?.[c], c)).join(' / ')
  return '另有 ' + text
})
const rateValue = computed(() => (summary.value ? summary.value.success_rate * 100 : null))

const compositionOption = computed(() => {
  const s = summary.value
  const data = TOKEN_PARTS.map((p) => ({ name: p.name, value: p.pick(s) }))
    .filter((x) => x.value > 0)
    .map((x) => ({ ...x, itemStyle: { color: TOKEN_PARTS.find((p) => p.name === x.name)?.color } }))
  return {
    tooltip: { trigger: 'item', valueFormatter: (v: number) => n(v) + ' 词元' },
    legend: { bottom: 0, icon: 'circle', textStyle: { fontSize: 12, color: ct.value.text } },
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
      splitLine: { lineStyle: { color: ct.value.split, type: 'dashed' } },
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: ct.value.secondary, fontSize: 11 }
    },
    yAxis: {
      type: 'category',
      data: items.map((x) => x.name),
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: ct.value.text, fontSize: 11, width: 132, overflow: 'truncate' }
    },
    series: [
      {
        type: 'bar',
        barMaxWidth: 16,
        itemStyle: { borderRadius: [0, 4, 4, 0] },
        label: {
          show: true,
          position: 'right',
          color: ct.value.secondary,
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
// 占比必须限定在一种币种内：跨币种的「占比」分母是两种钱的和，没有意义。
// 只有一种币种时切换器整个不显示，页面与以前完全一样。
const pieCurrencies = computed(() => {
  const seen: Record<string, string> = {}
  for (const x of byModel.value) for (const c of Object.keys(x.costs ?? {})) seen[c] = ''
  return currencyKeys(seen)
})
const pieCurrency = ref('')
watch(
  [pieCurrencies, scopeCurrency],
  ([list, scoped]) => {
    // 筛选范围钉死了币种就跟着它走：筛到美元渠道而饼图还在算人民币占比，
    // 会得到一张全是 0 的图
    if (scoped && list.includes(scoped)) {
      pieCurrency.value = scoped
      return
    }
    // 选中的币种消失了（换时间范围 / 换筛选）就回到第一个，不留一个空图
    if (!list.includes(pieCurrency.value)) pieCurrency.value = list[0] ?? ''
  },
  { immediate: true }
)
const pieCur = computed(() => pieCurrency.value || pieCurrencies.value[0] || '')

const modelPieOption = computed(() => {
  const data = byModel.value
    .map((x) => ({
      name: x.name,
      value: Number(x.costs?.[pieCur.value] ?? 0),
      itemStyle: { color: modelColors.value.get(x.name) || '#c87864' }
    }))
    .filter((x) => x.value > 0)
  return {
    color: PALETTE,
    tooltip: { trigger: 'item', valueFormatter: (v: number) => moneyText(v, pieCur.value) },
    legend: { type: 'scroll', bottom: 0, icon: 'circle', textStyle: { fontSize: 12, color: ct.value.text } },
    series: [
      {
        type: 'pie',
        radius: '66%',
        center: ['50%', '44%'],
        minShowLabelAngle: 1,
        label: { formatter: '{b} {d}%', fontSize: 11, color: ct.value.text },
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
  show: false, x: 0, y: 0, day: '', hour: 0, requests: 0, costs: {} as Record<string, string>, tokens: 0
})
// 一格里可能有两种币种的账（同一小时里既有人民币渠道又有美元渠道），
// 提示框逐币种列出来，不合成一个数
const heatTipCosts = computed(() => costsText(heatTip.value.costs))

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
    costs: it?.costs ?? {},
    tokens: it?.tokens ?? 0
  }
}

function hideHeatTip() {
  heatTip.value.show = false
}

const heatTotal = computed(() => heat.value.reduce((a, b) => a + b.requests, 0))

// 时间范围改由 a-radio-group 的 v-model 直接更新，
// 它的 change 只在取值真的变化时触发，所以这里不需要再判一次重


// 实时数值：服务端每两秒比一次今日汇总，变了才推。
// 这里只做字段合并 —— 它不带 range 等本地查询字段，
// 整个替换会把页面依赖的其它字段抹掉。
onLive('stats', (data: Record<string, unknown>) => {
  // 首屏还没加载完时忽略推送：那一份由 load() 负责，
  // 提前合并会得到一个缺字段的 summary
  if (!summary.value) return
  // 推来的永远是「今天 + 全站」那一份（见 live.go），所以只在这个视角下合并。
  // 否则选着「近7天」或某个分组时，卡片会在两秒后被悄悄换成今天的全站数字：
  // 两个数看上去都像真的，谁也不会去怀疑。
  // 筛选视角靠「刷新」按钮取数 —— 要让推送按筛选走，得给每个订阅者存一份
  // 筛选状态，那是另一件事。
  if (range.value !== 'today' || groupFilter.value !== ALL || channelFilter.value !== ALL) return
  summary.value = { ...summary.value, ...(data as object) } as Summary
})

// 顺序不能反：先把分组 / 渠道列表拿到、把存下来的筛选值校验过，再去取统计。
// 反过来的话会先用旧值查一遍、再用校验后的值查一遍，
// 中间那一帧的数字（以及可能的「筛选框显示 A、数字是全部」）都是错的。
onMounted(async () => {
  await loadFilters()
  await load()
})
</script>

<template>
  <div class="dashboard">
    <!-- 工具栏：时间范围 + 分组 / 渠道筛选 + 刷新（对齐参考站 dashboard-toolbar） -->
    <PageToolbar label="时间范围">
      <!-- 用 a-radio-group 而不是手写 <button>：
           这里原来写的是 class="pill-btn"，但那个类在项目里从未定义过，
           于是按钮一直是浏览器默认样式（灰底、深色描边、字号偏小），
           和其余部分完全不像一套东西。
           换成 Ant Design 的组件还能自动跟随明暗主题与设计令牌。 -->
      <a-radio-group v-model:value="range" button-style="solid" @change="onRangeChange">
        <a-radio-button v-for="r in ranges" :key="r.key" :value="r.key">{{ r.label }}</a-radio-button>
      </a-radio-group>
      <!-- 分组 / 渠道紧跟在时间范围右边：它们回答的是同一类问题
           （「下面这些数字算的是哪一部分」），放在一起才读得成一句话 -->
      <a-select
        v-model:value="groupFilter"
        :options="groupOptions"
        style="width: 150px"
        @change="onGroupChange"
      />
      <!-- 240px = 最长的一条「图标 + 渠道名 · 分组名」量出来的，
           与请求日志页同一个宽度；给窄了会把分组名截掉 -->
      <a-select
        v-model:value="channelFilter"
        :options="channelOptions"
        style="width: 240px"
        @change="onChannelChange"
      >
        <!-- 下拉项与选中值是 antd 的两个插槽，内容交给同一个组件渲染：
             分开写迟早会出现「下拉里有图标、选完就没了」这种不一致 -->
        <template #option="opt"><ChannelOption :option="opt" /></template>
        <template #optionLabel="opt"><ChannelOption :option="opt" /></template>
      </a-select>
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
          <template #value>
            <AnimatedNumber :value="summary?.requests ?? null" />
          </template>
          <template #icon><ApiOutlined /></template>
        </StatCard>
        <!-- 大数字是主币种，其余币种写在提示里。不同币种不能相加，
             所以这张卡永远不会出现「合计」 -->
        <StatCard
          label="消耗金额"
          :value="moneyText(summary?.costs?.[costCur], costCur)"
          tone="orange"
          :hint="costHint"
        >
          <template #value>
            <AnimatedNumber :value="costValue" format="money" :currency="costCur" />
          </template>
          <template #icon><DollarOutlined /></template>
        </StatCard>
        <StatCard label="词元数量" :value="n(summary?.total_tokens)" tone="blue" :hint="'命中率 ' + ((summary?.cache_hit_rate ?? 0) * 100).toFixed(1) + '%'">
          <template #value>
            <AnimatedNumber :value="summary?.total_tokens ?? null" />
          </template>
          <template #icon><ThunderboltOutlined /></template>
        </StatCard>
        <StatCard
          label="成功率"
          :value="summary ? (summary.success_rate * 100).toFixed(1) + '%' : '--'"
          tone="green"
          :hint="'平均首字耗时 ' + Math.round(summary?.avg_first_byte_ms ?? 0) + 'ms'"
        >
          <template #value>
            <AnimatedNumber :value="rateValue" format="percent" />
          </template>
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
            <div v-if="heatTipCosts">消费 {{ heatTipCosts }}</div>
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
        <template #extra>
          <!-- 占比不能跨币种相加，所以这里限定一种币种；只有一种时不显示 -->
          <a-radio-group v-if="pieCurrencies.length > 1" v-model:value="pieCurrency" size="small">
            <a-radio-button v-for="c in pieCurrencies" :key="c" :value="c">{{ symbolOf(c) }} {{ c }}</a-radio-button>
          </a-radio-group>
          <span v-else class="panel-note">{{ pieCur ? symbolOf(pieCur) + ' ' + pieCur : '' }}</span>
        </template>
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
