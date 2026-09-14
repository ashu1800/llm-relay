<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, DownloadOutlined } from '@ant-design/icons-vue'
import { useRoute } from 'vue-router'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import GroupTag from '@/components/GroupTag.vue'
import ChannelOption from '@/components/ChannelOption.vue'
import { onLive } from '@/composables/useLive'
import { symbolOf } from '@/utils/money'
import { channelOption } from '@/utils/channelOption'
import type { Channel, ChannelGroup, Paged, RequestLog } from '@/api/types'

// 分组表：日志里的模型、密钥、分组三处标签共用该请求所属分组的颜色。
//
// 为什么日志要按分组着色而不是按模型名（原来是后者）：
// 分组是用户自己配的边界（哪个密钥能走哪批渠道），日志里要一眼看出
// 「这条请求走的是哪个分组」；模型名着色对排障没有帮助，反而多一套颜色规则。
const groups = ref<ChannelGroup[]>([])

function groupOf(id: number) {
  return groups.value.find((g) => g.id === id)
}

/**
 * 胶囊的颜色参数：同一个分组下的模型 / 密钥 / 分组三个标签共用它。
 *
 * colorFrom 必须传分组名 —— 三个标签的展示名各不相同，
 * 若让它们各自按自己的名字派色，同一行会出现三种颜色（实测踩过）。
 */
function tagColorOf(id: number) {
  const g = groupOf(id)
  // 找不到分组时（分组已删、或历史日志里 group_id=0）统一用一个固定的来源名：
  // 否则同一行的三个胶囊会各按自己的名字派色，看起来像三个不同分组
  return { color: g?.color, colorFrom: g ? g.name : '未知分组' }
}

function groupName(id: number) {
  return groupOf(id)?.name ?? (id ? String(id) : '—')
}

const loading = ref(false)
const rows = ref<RequestLog[]>([])
const total = ref(0)
const detailOpen = ref(false)
const current = ref<RequestLog | null>(null)

// 'all' 是这个项目里「全部」的哨兵值（渠道页与看板同一套）。
// 不用 0：后端的约定是「不传参数＝不筛选」，而界面上的「全部分组」与
// 「分组 id=0」是两件事，混用迟早出错
const ALL = 'all'

const query = reactive({
  page: 1,
  page_size: 50,
  group_id: ALL,
  channel_id: ALL,
  model: ALL,
  // '' 表示不限时间范围
  range: 'today'
})

// 工具栏之外的额外条件：工具栏只留分组 / 渠道 / 模型三个下拉，
// 但 trace_id（从一条报错跳到完整链路）与「只看失败」是排障时要用的。
// 它们从 URL 进来（?trace_id=… / ?status_class=error），以可关闭的小标签
// 出现在工具栏末尾 —— 平时不占地方，要用时也没丢。
const extra = reactive({ trace_id: '', status_class: '' })

const route = useRoute()

// 渠道列表：三个下拉的候选与渠道列都取自它（分组列表另见 groups）
const channels = ref<Channel[]>([])

// 「今天」按本地零点算，而不是「最近 24 小时」：
// 后者在早上看会把昨天的调用也算进来，与看板上的今日口径对不上 ——
// 两个页面显示同一个上午的请求数却不一样，是最容易被当成 bug 的那种不一致。
const rangeOptions = [
  { value: '1h', label: '近 1 小时' },
  { value: 'today', label: '今天' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
  { value: '', label: '不限时间' }
]

// rangeToSince 把「近 N 小时/天」换算成绝对时刻。
// 不用「N 小时前的此刻」之外的写法：后端按 created_at 过滤，
// 相对区间每次请求都变会导致翻页时结果漂移，所以要固定成绝对时间。
function rangeToSince(range: string): string {
  if (!range) return ''
  if (range === 'today') {
    // 本地零点，再转成绝对时刻 —— 与看板的「今天」用同一套边界
    const d = new Date()
    d.setHours(0, 0, 0, 0)
    return d.toISOString()
  }
  const hours: Record<string, number> = { '1h': 1, '24h': 24, '7d': 24 * 7, '30d': 24 * 30 }
  const h = hours[range]
  if (!h) return ''
  return new Date(Date.now() - h * 3600 * 1000).toISOString()
}

// buildParams 在列表与导出之间共用：两边条件必须完全一致，
// 否则「导出的」和「看到的」不是一回事，排障时最容易被误导。
function buildParams(includePaging: boolean): URLSearchParams {
  const params = new URLSearchParams()
  if (includePaging) {
    params.set('page', String(query.page))
    params.set('page_size', String(query.page_size))
  }
  // 三个下拉：'all' 就是不传（后端「不传参数＝不筛选」）
  if (query.group_id !== ALL) params.set('group_id', query.group_id)
  if (query.channel_id !== ALL) params.set('channel_id', query.channel_id)
  if (query.model !== ALL) params.set('model', query.model)
  // URL 带来的额外条件，同样要进导出，否则导出的不是当前看到的这批
  if (extra.trace_id) params.set('trace_id', extra.trace_id)
  if (extra.status_class) params.set('status_class', extra.status_class)
  const since = rangeToSince(query.range)
  if (since) params.set('since', since)
  return params
}

const groupOptions = computed(() => [
  { value: ALL, label: '全部分组' },
  ...groups.value.map((g) => ({ value: String(g.id), label: g.name }))
])

// 选了分组就只列它的渠道。不收窄的话「分组 A + 属于分组 B 的渠道」这种组合
// 能选出来，而它查出来永远是 0 条，看起来像日志丢了。
const visibleChannels = computed(() =>
  query.group_id === ALL
    ? channels.value
    : channels.value.filter((c) => String(c.group_id) === query.group_id)
)

// 渠道选项：图标 + 名字，分组名只在「全部分组」时才补上
// （见 utils/channelOption.ts，数据看板用的是同一个函数）
const channelOptions = computed(() => [
  { value: ALL, label: '全部渠道' },
  ...visibleChannels.value.map((c) => channelOption(c, groups.value, query.group_id === ALL))
])

// 模型候选取渠道白名单（/channels 的 models）：它是系统当前认识的模型全集。
// 不从「这段时间有流量的模型」取 —— 那样下拉会随流量变动，
// 昨天用过的模型今天就选不出来了。
const visibleModels = computed(() => {
  const src =
    query.channel_id === ALL
      ? visibleChannels.value
      : visibleChannels.value.filter((c) => String(c.id) === query.channel_id)
  return [...new Set(src.flatMap((c) => c.models || []))].sort()
})

const modelOptions = computed(() => [
  { value: ALL, label: '全部模型' },
  ...visibleModels.value.map((m) => ({ value: m, label: m }))
])

// syncFilters 把下级筛选夹回合法值：换了分组，原来选的渠道可能已不属于它；
// 换了分组或渠道，原来选的模型可能已不在候选里。
// 不夹的话查询条件会停在一个空集合上（列表恒为 0 条），而界面上看不出原因。
function syncFilters() {
  if (
    query.channel_id !== ALL &&
    !visibleChannels.value.some((c) => String(c.id) === query.channel_id)
  ) {
    query.channel_id = ALL
  }
  if (query.model !== ALL && !visibleModels.value.includes(query.model)) {
    query.model = ALL
  }
}

// 三个下拉都用 @change + v-model：a-select 的 change 传的是**值**
// （a-radio-group 传的是事件对象，两者不一样，看板上踩过），
// 这里仍显式赋值一次 —— 不依赖 v-model 与 change 的先后顺序。
function onGroupChange(v: string) {
  query.group_id = v
  syncFilters()
  search()
}

function onChannelChange(v: string) {
  query.channel_id = v
  syncFilters()
  search()
}

function onModelChange(v: string) {
  query.model = v
  search()
}

// applyUrlFilters 只在进入页面时读一次 URL：这个页面的筛选状态不进地址栏，
// 免得用户以为地址栏能当书签用、却越用越乱。
function applyUrlFilters() {
  const tid = String(route.query.trace_id || '').trim()
  if (tid) extra.trace_id = tid
  const sc = String(route.query.status_class || '').trim()
  if (sc === 'error' || sc === 'success') extra.status_class = sc
}

function clearExtra(key: 'trace_id' | 'status_class') {
  extra[key] = ''
  search()
}

// 详情里的「只看这条链路」：原来工具栏上有个 trace_id 输入框，
// 但 trace_id 是从日志详情里才看得到的东西 —— 入口放在看得见它的地方更顺手。
function onlyThisTrace() {
  if (!current.value) return
  extra.trace_id = current.value.trace_id
  detailOpen.value = false
  search()
}

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为这段时间本来就没有调用
const loadError = ref('')

// loadGroups 只在首次加载时取一次：分组与渠道是低频变更的配置，
// 跟着每次翻页/刷新去拉一份纯属浪费（日志页刷新很频繁）。
//
// 它同时是三件事的数据源，所以拿不到时的降级要各自说明：
// 分组颜色（模型/密钥/分组三列标签）、三个下拉的候选、渠道列的名称。
async function loadGroups() {
  try {
    const [g, c] = await Promise.all([
      api.get<{ items: ChannelGroup[] }>('/groups'),
      api.get<{ items: Channel[] }>('/channels')
    ])
    groups.value = g.items || []
    channels.value = c.items || []
  } catch {
    // 拿不到不影响看日志：标签会退回按名字派生的颜色，下拉只剩「全部」，
    // 渠道列显示日志里记下的名字
  }
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<Paged<RequestLog>>('/logs?' + buildParams(true).toString())
    rows.value = res.items || []
    total.value = res.total || 0
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  load()
}

// 详情直接用列表行数据：列表接口返回的字段已经完整，
// 报文相关展示移除后，也不必再为它请求 /logs/:id。
function openDetail(row: RequestLog) {
  current.value = row
  detailOpen.value = true
}

// 耗时分级：5 秒内绿色、6-15 秒橙黄、15 秒以上红色。
//
// 分档按「用户体感」定：5 秒内是正常对话该有的速度，
// 5-15 秒已经明显在等，超过 15 秒基本可以判定这次调用有问题（上游慢或重试）。
// 边界取 <=5000 / <=15000 而不是 5000~6000 之间留缝隙：
// 中间的毫秒必须落进某一档，否则会出现「不着色」的空档。
//
// fmtMs 对 0 与空值都返回 '-'，那种情况不着色，避免把「没有数据」显示成「很快」。
function latencyClass(ms: number | null | undefined) {
  if (!ms) return 'lat-none'
  if (ms <= 5000) return 'lat-fast'
  if (ms <= 15000) return 'lat-mid'
  return 'lat-slow'
}

/** 悬停说明这一档的判据：颜色本身不该是唯一的信息来源 */
function latencyTitle(ms: number | null | undefined) {
  if (!ms) return '没有记录到耗时'
  if (ms <= 5000) return '5 秒内'
  if (ms <= 15000) return '5-15 秒'
  return '超过 15 秒'
}

function statusColor(code: number) {
  if (code >= 200 && code < 300) return 'green'
  if (code === 429) return 'orange'
  if (code >= 400) return 'red'
  return 'default'
}

// 命中率分母为全部输入 = 未命中 + 命中 + 缓存写入，与看板口径保持一致
function cacheRate(row: RequestLog) {
  const denom = row.prompt_tokens + row.cached_tokens + row.cache_creation_tokens
  if (denom <= 0) return '-'
  return ((row.cached_tokens / denom) * 100).toFixed(2) + '%'
}

// 固定成 YYYY-MM-DD HH:mm:ss —— 与参考站日志列表一致。
// 原来用 toLocaleString('zh-CN')，出来的是 2026/9/12 13:06:02：
// 斜杠分隔、月日不补零，同一列里宽度还会随月份变化而抖动。
// 手工补零而不是再用一次 toLocale*，是为了不受运行环境区域设置影响。
function pad2(n: number) {
  return n < 10 ? '0' + n : String(n)
}
function fmtTime(t: string) {
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d.getTime())) return t
  return (
    d.getFullYear() +
    '-' + pad2(d.getMonth() + 1) +
    '-' + pad2(d.getDate()) +
    ' ' + pad2(d.getHours()) +
    ':' + pad2(d.getMinutes()) +
    ':' + pad2(d.getSeconds())
  )
}

// 计价时刻：快照里存的是 RFC3339（如 2026-09-14T09:58:08+08:00），原样摆出来是给机器看的
// —— T 分隔、带秒级以上的偏移量，和同一行里的其他文案不是一种语气。
// 这里把它改成与日志列表列一致的 YYYY-MM-DD HH:mm:ss，但**不做时区换算**：
// 那个偏移量正是服务器判定「工作日高峰」时用的时钟（时段规则按服务器本地时区判断），
// 换成访客所在时区后 09:58 会显示成 01:58，跟同一行的时段标签对不上。
// 只有访客时区与快照不一致时，才把偏移量补在括号里，免得墙上时间被误读成本地时间。
function fmtTimeAt(t: string) {
  if (!t) return '—'
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2})(?:\.\d+)?(Z|[+-]\d{2}:?\d{2})$/.exec(t)
  // 形态不认识时退回按浏览器时区解析：显示原文比显示 ISO 串更糟
  if (!m) return fmtTime(t)
  const wall = m[1] + ' ' + m[2]
  const off = m[3]
  const offMin =
    off === 'Z'
      ? 0
      : (off[0] === '-' ? -1 : 1) * (Number(off.slice(1, 3)) * 60 + Number(off.slice(-2)))
  // getTimezoneOffset 返回的是「UTC 减本地」，符号与 RFC3339 相反，这里取反后再比
  if (offMin === -new Date().getTimezoneOffset()) return wall
  const abs = Math.abs(offMin)
  return wall + ' (UTC' + (offMin < 0 ? '-' : '+') + pad2(Math.floor(abs / 60)) + ':' + pad2(abs % 60) + ')'
}

function fmtMs(v: number) {
  if (!v) return '-'
  return v >= 1000 ? (v / 1000).toFixed(2) + 's' : v + 'ms'
}

// 词元超过 1K 后改用 K 显示：列宽有限，4457 这种原始数字扫一眼读不出量级。
// 与 fmtMs 同一档阈值（>=1000），但这里按两位小数截断而不是四舍五入 ——
// 4.457K 显示 4.45K，避免展示值比真实用量大。
function fmtTokens(v: number | null | undefined) {
  if (v == null || !Number.isFinite(v)) return '-'
  if (v < 1000) return String(v)
  return (Math.floor(v / 10) / 100).toFixed(2) + 'K'
}

// 费用：符号取这条日志自己的币种快照（渠道后来改了币种也不影响历史行），
// 不做任何换算 —— 人民币渠道的钱就是人民币
function fmtCost(v: string, currency?: string) {
  const n = Number(v)
  return n > 0 ? symbolOf(currency) + n.toFixed(6) : '-'
}

// 费用为什么是这么多：把当时生效的倍率摊在金额旁边。
// 时段倍率按服务器本地时间命中，用户核对账单时最常问的就是「这笔怎么贵了」。
function costMultiplierTag(row: RequestLog) {
  const s = row.pricing_snapshot
  if (!s || s.multiplier == null) return { peak: '', fixed: '' }
  const m = Number(s.multiplier)
  if (!m || m === 1) return { peak: '', fixed: '' }
  if (s.peak_applied) {
    return { peak: '×' + m + ' ' + (s.peak_label || '时段倍率'), fixed: '' }
  }
  if (s.multiplier_source === 'fixed') {
    return { peak: '', fixed: '×' + m + ' 固定倍率' }
  }
  return { peak: '', fixed: '' }
}

// 计价一行：把快照里的单价与来源写清楚，便于与「价格试算」对照
function multiplierSourceText(row: RequestLog) {
  const s = row.pricing_snapshot
  if (!s) return ''
  // 库里可能是 {}（早期版本对这个字段写过空对象）：没有单价就没有「计价」可讲，
  // 不判断的话界面上会出现「输入 $undefined」
  if (s.input_per_1m == null && s.input == null) return ''
  const parts: string[] = []
  // 单价符号取快照里的币种：日志里存的单价与金额必须是同一个口径，
  // 否则这一行会和上面的费用对不上（¥ 的金额配 $ 的单价）
  const sym = symbolOf(s.currency)
  parts.push('输入 ' + sym + (s.input_per_1m ?? s.input) + ' / 输出 ' + sym + (s.output_per_1m ?? s.output) + ' 每 1M')
  if (Number(s.fixed_multiplier) && Number(s.fixed_multiplier) !== 1) {
    parts.push('固定倍率 ×' + s.fixed_multiplier)
  }
  if (s.peak_applied) {
    parts.push('命中时段：' + (s.peak_label || '未命名') + ' ×' + s.multiplier)
  } else if (s.multiplier_source === 'fixed') {
    parts.push('按固定倍率 ×' + s.multiplier)
  } else {
    parts.push('原价')
  }
  if (s.resolved_at) parts.push('计价时刻 ' + fmtTimeAt(s.resolved_at))
  return parts.join('；')
}

// exportCsv 导出当前筛选条件下的**全部**日志。
//
// 原来只把当前页（默认 50 条）拼成 CSV，用户点「导出」拿到的文件却像是全部记录；
// 这种「看起来成功、实际只有一小部分」的问题不会报错，只会让人得出错误结论。
// 现在由服务端按同一套筛选条件全量导出，并把实际条数回报给用户。
const exporting = ref(false)

async function exportCsv() {
  exporting.value = true
  try {
    const res = await fetch('/api/admin/logs/export?' + buildParams(false).toString())
    if (!res.ok) {
      const text = await res.text()
      let msg = '导出失败 ' + res.status
      try {
        msg = JSON.parse(text)?.error?.message || msg
      } catch {
        // 非 JSON 错误体，保留默认文案
      }
      throw new Error(msg)
    }
    const blob = await res.blob()
    const exported = Number(res.headers.get('X-Exported-Count') || 0)
    const total = Number(res.headers.get('X-Total-Count') || 0)
    const truncated = res.headers.get('X-Exported-Truncated') === 'true'

    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'request-logs.csv'
    a.click()
    URL.revokeObjectURL(a.href)

    if (truncated) {
      // 被截断时必须说出来，否则用户会以为拿到了全部
      message.warning('已导出 ' + exported + ' 条，但符合条件的有 ' + total + ' 条，结果被截断', 6)
    } else {
      message.success('已导出 ' + exported + ' 条')
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    exporting.value = false
  }
}

const pagination = computed(() => ({
  current: query.page,
  pageSize: query.page_size,
  total: total.value,
  showSizeChanger: true,
  pageSizeOptions: ['20', '50', '100'],
  showTotal: (t: number) => '共 ' + t + ' 条',
  onChange: (p: number, ps: number) => {
    query.page = p
    query.page_size = ps
    load()
  }
}))

// 实时插入：服务端每秒查一次新日志（id 增量），有就推过来。
// 只在「看的是第一页且没有任何筛选」时插进去 —— 翻了页或筛过之后，
// 新来的日志不一定属于当前视图，硬插会让列表与筛选条件对不上。
onLive('logs', (items: RequestLog[]) => {
  if (!Array.isArray(items) || !items.length) return
  if (query.page !== 1) return
  if (query.group_id !== ALL || query.channel_id !== ALL || query.model !== ALL) return
  if (extra.trace_id || extra.status_class) return
  // 新日志的时间一定落在当前时间范围里（今天/近 1 小时……），
  // 只有「不限时间」之外的范围需要担心，而边界只差几毫秒，不值得再过滤一次
  const fresh = items.filter((it) => !rows.value.some((r) => r.id === it.id))
  if (!fresh.length) return
  rows.value = [...fresh.reverse(), ...rows.value].slice(0, query.page_size)
  total.value += fresh.length
})

onMounted(() => {
  // 分组与渠道必须先加载：三列标签的颜色、三个下拉的候选、渠道列的名称
  // 都取自它们，拿不到就会退回「按名字派生」，三列出现三种颜色（实测踩过）
  loadGroups()
  // URL 里的额外条件要在第一次取数之前生效，否则会先闪一次全量列表
  applyUrlFilters()
  load()
})
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
          <a-button :loading="exporting" @click="exportCsv"><DownloadOutlined /> 导出</a-button>
        </div>
        <!-- 三个下拉都是「改了即生效」，所以没有「查询」按钮（左侧也已有「刷新」） -->
        <a-select
          v-model:value="query.group_id"
          :options="groupOptions"
          style="width: 150px"
          @change="onGroupChange"
        />
        <!-- 240px = 最长的一条「图标 + 渠道名 · 分组名」量出来的
             （CommanCode · DeepSeek 的文字需要 203px，加图标与间距后约 225px）；
             给窄了会把分组名截成「DeepSe…」，而分组名正是重名渠道之间唯一的区分 -->
        <a-select
          v-model:value="query.channel_id"
          :options="channelOptions"
          style="width: 240px"
          @change="onChannelChange"
        >
          <!-- 下拉项与选中值是 antd 的两个插槽，内容交给同一个组件渲染：
               分开写迟早会出现「下拉里有图标、选完就没了」这种不一致 -->
          <template #option="opt"><ChannelOption :option="opt" /></template>
          <template #optionLabel="opt"><ChannelOption :option="opt" /></template>
        </a-select>
        <a-select
          v-model:value="query.model"
          :options="modelOptions"
          style="width: 180px"
          @change="onModelChange"
        />
        <a-select
          v-model:value="query.range"
          :options="rangeOptions"
          style="width: 130px"
          @change="search"
        />
        <!-- 额外条件：只从 URL 或详情里进来，平时不占地方 -->
        <a-tag
          v-if="extra.trace_id"
          closable
          :title="extra.trace_id"
          @close="clearExtra('trace_id')"
        >
          链路 {{ extra.trace_id.slice(0, 8) }}…
        </a-tag>
        <a-tag v-if="extra.status_class" closable @close="clearExtra('status_class')">
          {{ extra.status_class === 'error' ? '仅失败' : '仅成功' }}
        </a-tag>
      </div>

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="请求日志加载失败"
        @retry="load"
      >
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="pagination"
        row-key="id"
        size="small"
        :scroll="{ x: 1384 }"
      >
        <template #emptyText>
          <a-empty description="当前筛选条件下没有日志，可放宽筛选条件：把时间范围改成「近 7 天」，或把分组 / 渠道 / 模型改回「全部」" />
        </template>
        <a-table-column title="请求时间" :width="155" fixed="left">
          <template #default="{ record }">{{ fmtTime(record.created_at) }}</template>
        </a-table-column>
        <a-table-column title="模型" :width="155">
          <template #default="{ record }">
            <!-- 模型、密钥、分组三处用的是同一个组件与同一个颜色：
                 它们描述的是「这次请求属于哪个分组」，颜色因此必须一致 -->
            <GroupTag :name="record.model_requested" v-bind="tagColorOf(record.group_id)" />
            <div
              v-if="record.model_upstream && record.model_upstream !== record.model_requested"
              class="sub-text"
            >
              上游：{{ record.model_upstream }}
            </div>
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="72">
          <template #default="{ record }">
            <a-tag :color="statusColor(record.status_code)">{{ record.status_code }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="密钥" :width="120" ellipsis>
          <template #default="{ record }">
            <GroupTag v-if="record.api_key_name" :name="record.api_key_name" v-bind="tagColorOf(record.group_id)" />
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <a-table-column title="分组" :width="110" ellipsis>
          <template #default="{ record }">
            <GroupTag :name="groupName(record.group_id)" v-bind="tagColorOf(record.group_id)" />
          </template>
        </a-table-column>
        <!-- 渠道列是随「按渠道筛选」一起加的：筛了渠道却在列表里看不出
             每行走的是哪条渠道，这个筛选等于只生效一半。
             失败请求没走到渠道（channel_id=0）、渠道事后被删都会是空值，显示 — -->
        <a-table-column title="渠道" :width="110" ellipsis>
          <template #default="{ record }">
            <span v-if="record.channel_name">{{ record.channel_name }}</span>
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <a-table-column title="词元（输入/输出/缓存）" :width="180">
          <template #default="{ record }">
            <span class="token-cell">
              <span class="tk tk-in">{{ fmtTokens(record.prompt_tokens) }}</span>
              <span class="tk-sep">/</span>
              <span class="tk tk-out">{{ fmtTokens(record.completion_tokens) }}</span>
              <span class="tk-sep">/</span>
              <span class="tk tk-cache">{{ fmtTokens(record.cached_tokens) }}</span>
            </span>
          </template>
        </a-table-column>
        <a-table-column title="缓存命中" :width="110">
          <template #default="{ record }">
            <span class="cache-hit">{{ cacheRate(record) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="首字耗时" :width="100">
          <template #default="{ record }">
            <span :class="latencyClass(record.first_byte_ms)" :title="latencyTitle(record.first_byte_ms)">
              {{ fmtMs(record.first_byte_ms) }}
            </span>
          </template>
        </a-table-column>
        <a-table-column title="总共耗时" :width="100">
          <template #default="{ record }">
            <span :class="latencyClass(record.total_ms)" :title="latencyTitle(record.total_ms)">
              {{ fmtMs(record.total_ms) }}
            </span>
          </template>
        </a-table-column>
        <a-table-column title="费用" :width="100">
          <template #default="{ record }">{{ fmtCost(record.estimated_cost, record.cost_currency) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="72" fixed="right">
          <template #default="{ record }">
            <a @click="openDetail(record)">详情</a>
          </template>
        </a-table-column>
      </a-table>
      </DataState>
    </section>

    <a-drawer v-model:open="detailOpen" title="调用详情" width="720">
      <a-descriptions v-if="current" :column="1" bordered size="small">
        <a-descriptions-item label="Trace ID">
          {{ current.trace_id }}
          <a class="trace-link" @click="onlyThisTrace">只看这条链路</a>
        </a-descriptions-item>
        <a-descriptions-item label="请求模型">
          <GroupTag :name="current.model_requested" v-bind="tagColorOf(current.group_id)" />
        </a-descriptions-item>
        <a-descriptions-item label="上游模型">
          <GroupTag v-if="current.model_upstream" :name="current.model_upstream" v-bind="tagColorOf(current.group_id)" />
          <template v-else>-</template>
        </a-descriptions-item>
        <a-descriptions-item label="分组">
          <GroupTag :name="groupName(current.group_id)" v-bind="tagColorOf(current.group_id)" />
        </a-descriptions-item>
        <a-descriptions-item label="入站 → 出站协议">
          {{ current.inbound_protocol }} → {{ current.upstream_protocol || '-' }}
        </a-descriptions-item>
        <a-descriptions-item label="渠道">{{ current.channel_name || '-' }}</a-descriptions-item>
        <a-descriptions-item label="密钥">{{ current.api_key_name }}</a-descriptions-item>
        <a-descriptions-item label="客户端 IP">{{ current.client_ip }}</a-descriptions-item>
        <a-descriptions-item label="流式">{{ current.stream ? '是' : '否' }}</a-descriptions-item>
        <a-descriptions-item label="词元明细">
          输入 {{ fmtTokens(current.prompt_tokens) }} · 输出 {{ fmtTokens(current.completion_tokens) }} ·
          缓存命中 {{ fmtTokens(current.cached_tokens) }} · 缓存写入 {{ fmtTokens(current.cache_creation_tokens) }} ·
          推理 {{ fmtTokens(current.reasoning_tokens) }} · 命中率 {{ cacheRate(current) }}
        </a-descriptions-item>
        <a-descriptions-item label="重试">
          {{ current.retry_count > 0 ? '重试 ' + current.retry_count + ' 次' : '无' }}
        </a-descriptions-item>
        <a-descriptions-item label="耗时">
          首字 {{ fmtMs(current.first_byte_ms) }} · 上游握手 {{ fmtMs(current.upstream_ms) }} ·
          总共 {{ fmtMs(current.total_ms) }}
        </a-descriptions-item>
        <a-descriptions-item label="费用">
          {{ symbolOf(current.cost_currency) }}{{ Number(current.estimated_cost).toFixed(8) }}
          <a-tag v-if="current.usage_estimated" color="orange" style="margin-left: 6px">用量为估算值</a-tag>
          <!-- 金额为什么是这个数：把当时生效的倍率与来源摊开，省得去猜 -->
          <a-tag v-if="costMultiplierTag(current).peak" color="orange" style="margin-left: 6px">
            {{ costMultiplierTag(current).peak }}
          </a-tag>
          <a-tag v-else-if="costMultiplierTag(current).fixed" style="margin-left: 6px">
            {{ costMultiplierTag(current).fixed }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item v-if="multiplierSourceText(current)" label="计价">
          {{ multiplierSourceText(current) }}
        </a-descriptions-item>
        <a-descriptions-item v-if="current.error" label="错误">
          <pre class="err-box">{{ current.error }}</pre>
        </a-descriptions-item>
      </a-descriptions>

    </a-drawer>
  </div>
</template>

<style scoped>
.manage-container { padding: var(--gap); }
.manage-panel { padding: 0; overflow: hidden; }
.manage-toolbar {
  display: flex;
  align-items: center;
  gap: var(--gap);
  padding: var(--gap);
  min-height: 64px;
  flex-wrap: wrap;
}
.toolbar-left { display: flex; gap: var(--gap); }
.sub-text { font-size: 12px; color: var(--color-text-secondary); }
.token-cell { font-variant-numeric: tabular-nums; }
/* 详情里的「只看这条链路」：贴着 trace_id 放，弱化成次要操作，
   别让人以为它是个必须点的按钮 */
.trace-link { margin-left: 8px; font-size: 12px; }

/* 模型、密钥、分组三列各是一个胶囊，用的是同一个组件（components/GroupTag.vue）
   与同一个颜色 —— 该请求所属分组的颜色。

   原来的分工是「模型按模型名着色、密钥用中性色」：那套规则在排障时没用，
   日志里真正要回答的是「这条请求走的是哪个分组」。三者同色之后，
   扫一眼就能按颜色把同一分组的请求归到一起，也不必再记住两套配色规则。
   颜色不是唯一线索：三列里都写着名字。 */
/* 词元三段各自的颜色（变量定义见 theme.css，深色主题自动换档） */
.tk-in { color: var(--token-input); }
.tk-out { color: var(--token-output); }
.tk-cache { color: var(--token-cache); }
.tk-sep { color: var(--color-border); margin: 0 3px; }

/* 耗时分级 */
.lat-fast { color: var(--latency-fast); }
.lat-mid { color: var(--latency-mid); }
.lat-slow { color: var(--latency-slow); font-weight: 500; }
.lat-none { color: var(--color-text-secondary); }

/* 缓存命中率用缓存那段的颜色，与词元列第三个数是同一个语义 */
.cache-hit { color: var(--token-cache); font-variant-numeric: tabular-nums; }
/* 错误原文：详情里唯一保留的代码块（排障时最常看的就是上游报错） */
.err-box {
  font-family: var(--font-family-mono);
  max-height: 160px;
  overflow: auto;
  padding: 8px;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  color: var(--color-red);
}
</style>
