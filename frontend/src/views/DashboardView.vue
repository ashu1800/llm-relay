<script setup lang="ts">
// 数据看板 —— 2026-09-16 起「请求日志」整页并入这里：
// 上面是筛选栏与四张概览卡，下面是请求日志列表。
//
// 两者**不**共用筛选条件（2026-09-16 站主要求）：
//   概览卡按工具栏的时间范围 / 分组 / 渠道 / 模型统计；
//   日志列表恒定显示全部最新请求，只受 ?trace_id= / ?status_class= 两个深链约束。
// 列表的用处是「盯着最新发生了什么」，筛过之后反而看不到刚进来的调用。
//
// 为什么并成一页：这两块回答的本来就是同一个问题（「这段时间跑得怎么样」），
// 分在两页时最常做的动作是「在日志页看到异常 → 切到看板看总量 → 再切回去查明细」。
//
// 热力图与四张图表（消耗趋势 / 消耗分布 / 模型调用分析 / 模型消耗占比）已移除：
// 概览卡与日志列表已经覆盖了「多少 / 多少钱 / 多快 / 哪些失败」这几个问题，
// 图表属于「再往下研究」的需求，等真有人用再看要不要以别的方式补回。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  ApiOutlined,
  DollarOutlined,
  ThunderboltOutlined,
  CheckCircleOutlined,
  ReloadOutlined,
  SwapOutlined
} from '@ant-design/icons-vue'
import { useRoute } from 'vue-router'
import { api } from '@/api/client'
import PageToolbar from '@/components/PageToolbar.vue'
import StatCard from '@/components/StatCard.vue'
import AnimatedNumber from '@/components/AnimatedNumber.vue'
import DataState from '@/components/DataState.vue'
import PulseBar from '@/components/PulseBar.vue'
import DailyReport from '@/components/DailyReport.vue'
import RequestLogPanel from '@/components/RequestLogPanel.vue'
import { onLive, createThrottledLiveReloader } from '@/composables/useLive'
// 金额一律走 utils/money.ts：符号与小数位数只此一份（见那里的说明）
import { currencyKeys, moneyText, primaryCurrency, symbolOf } from '@/utils/money'
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
const summary = ref<Summary | null>(null)
const loading = ref(false)
// 「加载失败」不能只留一条转瞬即逝的消息：失败后卡片会显示成 0，
// 被读成「这段时间没有流量」。列表那边有自己的错误态（面板内部），
// 所以这里只管卡片这一排 —— 两个数据源可以各自失败，互不连坐。
const loadError = ref('')

// 「是否已有统计数据」。用它区分首次加载失败（整排换成错误说明）
// 与刷新失败（保留数字只提示）。
// 现在只剩 summary 一个数据源了：图表与热力图已移除，别的接口不再请求。
const hasStats = computed(() => summary.value !== null)

const ranges = [
  { key: 'today', label: '今天' },
  { key: '3d', label: '近3天' },
  { key: '7d', label: '近7天' },
  { key: '30d', label: '近30天' }
]
// ---- 筛选条件（时间范围 / 分组 / 渠道 / 模型）全部持久化 ----
//
// 为什么要持久化：「只看某个分组」是常态视角，每次打开页面、或从别的页面
// 切回来都要重选一遍，是纯粹的重复劳动。与渠道列表页的筛选同一套做法
// （见 utils/persistedChoice.ts）。
//
// 这四个条件是**整页**的：上面的卡片与下面的列表都按它们取数。
// 合并之前列表页自己记了一套 logs-* 键、看板记了一套 dashboard-*：
// 同一页面上出现两套视角，是这次合并要消灭的东西之一。
const RANGE_KEY = 'dashboard-range'
const GROUP_KEY = 'dashboard-group'
const CHANNEL_KEY = 'dashboard-channel'
const MODEL_KEY = 'dashboard-model'
// 词元卡片的数字格式：完整千分位 / 紧凑缩写（一律带 M/B 单位）。
// 阅读习惯跟着人走，与上面那组筛选共用同一套持久化机制，默认完整。
const TOKEN_FMT_KEY = 'dashboard-token-fmt'

// 哨兵值用 'all' 而不是 0：后端的约定是「不传参数＝不筛选」，
// 而界面上的「全部分组」与「分组 id=0」是两件事，混用迟早出错
const ALL = 'all'

// 紧凑格式的持久化取值是字符串（persistedChoice 只存字符串），
// 界面上用布尔更顺手：读时比较、写时反推
const tokenCompact = ref(readStoredChoice(TOKEN_FMT_KEY, ['full', 'compact'], 'full') === 'compact')
function toggleTokenFmt() {
  tokenCompact.value = !tokenCompact.value
  writeStoredChoice(TOKEN_FMT_KEY, tokenCompact.value ? 'compact' : 'full')
}

const range = ref('today')
const groupFilter = ref<string>(ALL)
const channelFilter = ref<string>(ALL)
const modelFilter = ref<string>(ALL)
// 排障深链带来的两个临时条件（?trace_id=… / ?status_class=error）。
// 它们不持久化：下次打开还被它们筛着，会看到一张恒为 0 条的列表，
// 而且不记得自己什么时候套上的。
const traceId = ref('')
const statusClass = ref('')
// 只在进入页面时读一次（见 applyUrlFilters）：筛选状态不进地址栏，
// 免得用户以为地址栏能当书签用、却越用越乱
const route = useRoute()

// 列表面板的句柄：工具栏的「刷新」要连它一起刷（一页一个刷新按钮）
const logPanel = ref<{ reload: () => void } | null>(null)
// 筛选下拉的候选：来自管理接口，不是统计接口 —— 统计接口只回有流量的渠道，
// 而「筛一条今天还没被用过的渠道」是合理需求（结果就是 0）
const filterGroups = ref<ChannelGroup[]>([])
const filterChannels = ref<Channel[]>([])

const groupOptions = computed(() => [
  { value: ALL, label: '全部分组' },
  ...filterGroups.value.map((g) => ({ value: String(g.id), label: g.name }))
])

// 渠道选项：图标 + 名字，分组名只在「全部分组」时才补上
// （见 utils/channelOption.ts，请求日志面板用的是同一个组件）
const visibleChannels = computed(() =>
  groupFilter.value === ALL
    ? filterChannels.value
    : filterChannels.value.filter((c) => String(c.group_id) === groupFilter.value)
)

const channelOptions = computed(() => [
  { value: ALL, label: '全部渠道' },
  ...visibleChannels.value.map((c) => channelOption(c, filterGroups.value, groupFilter.value === ALL))
])

// 模型候选取渠道白名单（/channels 的 models）：它是系统当前认识的模型全集。
// 不从「这段时间有流量的模型」取 —— 那样下拉会随流量变动，
// 昨天用过的模型今天就选不出来了。
//
// 这个条件以前只在日志页有，现在卡片也吃它（后端 /stats 支持 model）：
// 同一个筛选栏下，上面的数字与下面的行必须说的是同一批请求。
const visibleModels = computed(() => {
  const src =
    channelFilter.value === ALL
      ? visibleChannels.value
      : visibleChannels.value.filter((c) => String(c.id) === channelFilter.value)
  return [...new Set(src.flatMap((c) => c.models || []))].sort()
})

const modelOptions = computed(() => [
  { value: ALL, label: '全部模型' },
  ...visibleModels.value.map((m) => ({ value: m, label: m }))
])

// syncFilters 把下级筛选夹回合法值：换了分组，原来选的渠道可能已不属于它；
// 换了分组或渠道，原来选的模型可能已不在候选里。
// 不夹的话查询条件会停在一个空集合上（列表恒为 0 条、卡片全是 0），
// 而界面上看不出原因。顺序固定：先渠道后模型，因为后者的候选由前者收窄。
function syncFilters() {
  if (
    channelFilter.value !== ALL &&
    !visibleChannels.value.some((c) => String(c.id) === channelFilter.value)
  ) {
    channelFilter.value = ALL
  }
  if (modelFilter.value !== ALL && !visibleModels.value.includes(modelFilter.value)) {
    modelFilter.value = ALL
  }
}

function persistFilters() {
  writeStoredChoice(RANGE_KEY, range.value)
  writeStoredChoice(GROUP_KEY, groupFilter.value)
  writeStoredChoice(CHANNEL_KEY, channelFilter.value)
  writeStoredChoice(MODEL_KEY, modelFilter.value)
}

// 存下来的筛选值可能指向已经删掉的分组 / 渠道 / 模型。那种状态的表现是
// 「所有数字都是 0」或「列表恒为 0 条」，从界面上完全看不出原因 ——
// 所以列表到手后校验一次，不合法就退回「全部」（与渠道列表页的
// applyStoredGroupFilter 同一套做法）。
//
// 从 URL 带着 trace_id / status_class 进来的那一次**不恢复**本地筛选：
// 那种链接是要发给别人、或以后自己再打开的，同一个链接应该在哪台机器上、
// 隔多久打开都显示同一批记录。若再与收件人自己记着的渠道筛选相交，
// 链接会显示成一张空表 —— 看起来像日志丢了，而且两个人看到的还不一样。
// 这里只是「这一次不套用」，不写回存储：用户并没有改自己的视角。
function applyStoredFilters() {
  if (traceId.value || statusClass.value) return
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
  modelFilter.value = readStoredChoice(MODEL_KEY, [ALL, ...visibleModels.value], ALL)
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

// applyUrlFilters 只在进入页面时读一次 URL：筛选状态不进地址栏，
// 免得用户以为地址栏能当书签用、却越用越乱（工具栏那几个筛选记在本地，
// 见上面的 dashboard-* 键 —— 「下次打开还是这个视角」与「地址栏当书签」是两件事）。
// 用法上它是排障深链：?trace_id=… 看一条链路、?status_class=error 只看失败。
function applyUrlFilters() {
  const tid = String(route.query.trace_id || '').trim()
  if (tid) traceId.value = tid
  const sc = String(route.query.status_class || '').trim()
  if (sc === 'error' || sc === 'success') statusClass.value = sc
}

// 注意：a-radio-group 的 change 给的是**事件对象**，不是值（a-select 给的是值，
// 两者不一样）。所以这里不接收参数、也不赋值 —— v-model 已经更新过 range。
// 之前写成 onRangeChange(v) { range.value = v }，range 就变成了一个事件对象：
// 查询串成了 ?range=[object Object]，后端认不出、退回「今天」，
// 存储里也写进 "[object Object]" —— 界面上筛选项看着是选中的，数据却是今天的。
//
// 这四个处理函数只重取**统计**（卡片）。列表不吃这四个条件，所以这里不该顺手调
// logPanel.reload()：那会让一次「只看 glm」的筛选白刷一遍列表，而它的内容按定义
// 不会变。
// （历史上这里还踩过一个坑：那时列表跟着 props 重取，而父组件的事件处理函数是同步
// 跑的 —— 调 reload 时子组件读到的还是旧 props，于是多发一次旧条件的查询。旧查询
// （deepseek 的 5562 条）比新查询（glm 的 0 条）慢，回来把新结果盖掉，界面成了
// 「卡片 0、列表 5562」。两者解耦之后这个坑不存在了，但「改筛选不动列表」依然成立。）
// 只有一个「刷新」按钮例外：它的语义就是两块都刷（见 reloadAll）。
function reloadStats() {
  load()
}

function onRangeChange() {
  persistFilters()
  reloadStats()
}

function onGroupChange(v: string) {
  groupFilter.value = v
  syncFilters()
  persistFilters()
  reloadStats()
}

function onChannelChange(v: string) {
  channelFilter.value = v
  syncFilters()
  persistFilters()
  reloadStats()
}

// 三个下拉都用 @change + v-model：a-select 的 change 传的是**值**
// （a-radio-group 传的是事件对象，上面踩过），这里仍显式赋值一次 ——
// 不依赖 v-model 与 change 的先后顺序。
function onModelChange(v: string) {
  modelFilter.value = v
  syncFilters()
  persistFilters()
  reloadStats()
}

// 工具栏上那两个临时标签的关闭动作：清掉之后立即重取，
// 让「关掉筛选」与「列表已经变回全量」发生在同一帧里
function clearExtra(key: 'trace' | 'status') {
  if (key === 'trace') traceId.value = ''
  else statusClass.value = ''
}

// 一页只有一个「刷新」：卡片与列表一起刷。
// 拆成两个按钮的话，用户永远要猜「哪个按钮刷的是哪块」。
//
// 这里可以安全地直接叫面板重取（与上面四个处理函数不同）：
// 点刷新时没有任何筛选在变，不存在「面板拿着旧 props 发查询」的问题。
function reloadAll() {
  load()
  logPanel.value?.reload()
}

// 分组 / 渠道 / 模型不选时不带参数（后端把「不传」当作不筛选）
function statsQuery() {
  let s = '?range=' + range.value
  if (groupFilter.value !== ALL) s += '&group_id=' + groupFilter.value
  if (channelFilter.value !== ALL) s += '&channel_id=' + channelFilter.value
  if (modelFilter.value !== ALL) s += '&model=' + encodeURIComponent(modelFilter.value)
  return s
}


// 卡片上的数字统一走千分位（与参考站一致）。
// 图表相关的那些取值入口（色板、分桶时间格式、日期键）随图表一起删掉了。
function n(v: number | undefined) {
  return (v ?? 0).toLocaleString('zh-CN')
}

/**
 * 取当前时间范围与筛选下的概览统计。
 *
 * silent 用于实时推送触发的重取：不显示加载态（否则每两秒闪一次骨架），
 * 失败也不把页面上的数字换掉 —— 宁可显示旧数字，也不能显示错的。
 *
 * 请求序号（loadSeq）只让**最后一次**请求的结果落地。与列表是同一个坑
 * （RequestLogPanel.load 有同款守卫与实测记录）：切换筛选时新旧两个
 * /stats/summary 并发在途，覆盖面大的旧查询更慢，返回时把新数字盖掉 ——
 * 看板显示的就一直是错的时间范围，直到下一次操作才纠正。
 */
let loadSeq = 0

async function load(opts: { silent?: boolean } = {}) {
  const silent = !!opts.silent
  const seq = ++loadSeq
  if (!silent) loading.value = true
  if (!silent) loadError.value = ''
  try {
    // 只剩概览这一个请求了：图表与热力图移除后，timeseries / models /
    // channels / heatmap 四个接口不再由前端调用（后端保留，见 docs/ui-spec.md 第九节）。
    // 这也让卡片刷新变快 —— 原来一次刷新要打五个接口，任何一个慢都会拖住整排数字。
    const data = await api.get<Summary>('/stats/summary' + statsQuery())
    // 已经有更新的请求发出去了：这次的结果（以及它的错误、它的 loading）都作废
    if (seq !== loadSeq) return
    summary.value = data
  } catch (e: any) {
    if (silent || seq !== loadSeq) return
    loadError.value = e.message || '加载失败'
  } finally {
    // 与 RequestLogPanel.load 同款（那边有完整说明）：loading 只由非静默请求
    // 开关。写成 `!silent && seq === loadSeq` 时，被静默重取顶掉的那次非静默
    // 请求就既不落数据、也不熄灯 —— 整屏数字永远停在加载态（第三轮 F-中2）。
    if (!silent) loading.value = false
  }
}

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

// 实时数值：服务端**有新日志就立刻**算一次今日汇总，变了才推；
// 没流量时另有一条 2 秒的兜底节拍（见 backend/internal/api/live.go）。
// 之所以强调「有新日志就立刻」：这条推送与下面列表的新行必须落在同一拍上，
// 否则会出现「入场动画播完了数字才开始滚」（2026-09-18 站主反馈，实测差 1064ms）。
//
// 推送来的永远是「今天 + 全站」那一份（见 live.go），所以：
// - 当前正好是「今天 + 全站」→ 直接合并（它不带 range 等本地查询字段，
//   整个替换会把页面依赖的其它字段抹掉，这里只做字段合并）；
// - 当前是别的视角（近 7 天 / 筛了分组或渠道）→ 合并不了，但**收到推送本身
//   就说明有新流量**（服务端只在数据真的变了才推），于是立刻静默重取一次
//   当前视角。以前这里是直接 return，选着「近 7 天」或某个分组时卡片就
//   再也不动了，只能靠手点「刷新」；后来改成 setTimeout(1500) 之后卡片会动了，
//   但那 1.5 秒人为延迟恰好把数字的变动推到入场动画（sweep 2s / glow 1.6s）
//   结束之后 —— 站主二次反馈「动画播完了数值才变动」指的就是它。
//   现在：首帧立即重取（数字与动画同时开始变动），只保留 1 秒最小间隔挡
//   持续流量下的请求风暴（节流器的取舍见 useLive.createThrottledLiveReloader）。
const statsReloader = createThrottledLiveReloader(() => load({ silent: true }))

onUnmounted(() => {
  statsReloader.dispose()
})

// ---- 金钱流：花费卡对「进账」的即时反馈 ----
//
// costs 真的变了（不是首屏加载）就给消耗金额卡一次金色微光。流量大时
// 每次推送都重新计时，光会常亮 —— 钱持续在进来，灯不该灭（见 StatCard.flash）。
// 注意比较的是整个 costs 对象：币种之间不能相加，哪一笔进了哪个币种
// 都值得闪一下。
const costFlash = ref(0)
watch(
  () => summary.value?.costs,
  (nv, ov) => {
    if (!nv || !ov) return
    if (JSON.stringify(nv) !== JSON.stringify(ov)) costFlash.value = Date.now()
  }
)

// ---- 预算热度：消耗卡的体温 ----
//
// 后端只在分组预算跨过 80%/100% 档位时推一次 budget_alert（每天每档一次）。
// 看板消耗卡是全站视角，跟单条分组预算没有严格对应，但「有分组烧到警戒线」
// 对站主来说就是「钱包在发热」—— 卡片进入对应的温度档，当天不退烧
// （sessionStorage 按天存档，跨天自然冷却）。refresh 后热度也还在。
const costHeat = ref<'warm' | 'hot' | null>(null)
const HEAT_KEY = 'llm-relay-cost-heat'
try {
  const raw = JSON.parse(sessionStorage.getItem(HEAT_KEY) || 'null') as { day: string; level: 'warm' | 'hot' } | null
  if (raw && raw.day === new Date().toDateString()) costHeat.value = raw.level
} catch {
  // 存档坏了就当没有：热度只是氛围，不值得为它报错
}
onLive('budget_alert', (a: { level: '80' | '100' }) => {
  const level = a.level === '100' ? 'hot' : 'warm'
  if (costHeat.value === 'hot') return // 已经烧红了不会再降温
  costHeat.value = level
  try {
    sessionStorage.setItem(HEAT_KEY, JSON.stringify({ day: new Date().toDateString(), level }))
  } catch {
    // 存不下就不存：本轮会话里热度仍然生效
  }
})

onLive('stats', (data: Record<string, unknown>) => {
  // 首屏还没加载完时忽略推送：那一份由 load() 负责，
  // 提前合并会得到一个缺字段的 summary
  if (!summary.value) return
  // 推送来的那份是「今天 + 全站」，只有当前正好是这个视角才能直接合并。
  // 模型筛选也算别的视角：不判它的话，筛着某个模型时收到的全站数字
  // 会把卡片顶掉（比不刷新更糟 —— 它看起来像是刷新了）
  if (
    range.value !== 'today' ||
    groupFilter.value !== ALL ||
    channelFilter.value !== ALL ||
    modelFilter.value !== ALL
  ) {
    statsReloader.request()
    return
  }
  summary.value = { ...summary.value, ...(data as object) } as Summary
})

// 顺序不能反：先把分组 / 渠道列表拿到、把存下来的筛选值校验过，再去取统计。
// 反过来的话会先用旧值查一遍、再用校验后的值查一遍，
// 中间那一帧的数字（以及可能的「筛选框显示 A、数字是全部」）都是错的。
//
// URL 里的额外条件要更早生效：applyStoredFilters 靠它判断「这次是带条件的链接」
// （见那里的说明，顺序反了就判不出来）。
//
// 列表本身由 RequestLogPanel 在挂载时自己取：两个数据源各取各的，
// 一个慢或一个失败都不会拖住另一个。
onMounted(async () => {
  applyUrlFilters()
  await loadFilters()
  await load()
})
</script>

<template>
  <div class="dashboard">
    <!-- 工具栏：只服务上面的概览卡 —— 时间范围 + 分组 / 渠道 / 模型 + 刷新。
         下面的日志列表不吃这四个条件（它恒定全量最新，理由见下）。

         这里曾经放过一句说明筛选作用范围的提示，2026-09-17 应站主要求**移除**
         （提交 c56354b）。所以别再往这里加提示 ——
         `frontend/scripts/check-contracts.mjs` 已把当初的契约反转成禁止式，
         断言工具栏**不得**出现该文案；那条断言存在的意义正是防止有人
         （包括只读了旧注释的 AI）从旧分支或旧文档里把它带回来。
         这个文件里连那句话的原文都不写 —— 契约是整文件正则匹配，
         注释里带上同样会把检查弄红。
         别处若还写着「按钮区有这句提示」，那是过期描述，以本注释与契约脚本为准。
         行为本身未变：列表恒定全量最新，只受 trace_id / status_class 两个深链约束。 -->
    <PageToolbar label="时间范围">
      <!-- 用 a-radio-group 而不是手写 <button>：
           这里原来写的是 class="pill-btn"，但那个类在项目里从未定义过，
           于是按钮一直是浏览器默认样式（灰底、深色描边、字号偏小），
           和其余部分完全不像一套东西。
           换成 Ant Design 的组件还能自动跟随明暗主题与设计令牌。 -->
      <a-radio-group v-model:value="range" button-style="solid" @change="onRangeChange">
        <a-radio-button v-for="r in ranges" :key="r.key" :value="r.key">{{ r.label }}</a-radio-button>
      </a-radio-group>
      <!-- 分组 / 渠道 / 模型紧跟在时间范围右边：它们回答的是同一类问题
           （「下面这些数字算的是哪一部分」），放在一起才读得成一句话 -->
      <a-select
        v-model:value="groupFilter"
        :options="groupOptions"
        style="width: 150px"
        @change="onGroupChange"
      />
      <!-- 240px = 最长的一条「图标 + 渠道名 · 分组名」量出来的，
           与请求日志列表同一个宽度；给窄了会把分组名截掉 -->
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
      <!-- 模型候选按渠道白名单收窄（见 visibleModels） -->
      <a-select
        v-model:value="modelFilter"
        :options="modelOptions"
        style="width: 180px"
        @change="onModelChange"
      />
      <!-- 排障深链带来的两个临时条件：只从 URL 或日志详情里进来，平时不占地方 -->
      <a-tag v-if="traceId" closable :title="traceId" @close="clearExtra('trace')">
        链路 {{ traceId.slice(0, 8) }}…
      </a-tag>
      <a-tag v-if="statusClass" closable @close="clearExtra('status')">
        {{ statusClass === 'error' ? '仅失败' : '仅成功' }}
      </a-tag>
      <template #right>
        <a-button :loading="loading" @click="reloadAll()"><ReloadOutlined /> 刷新</a-button>
      </template>
    </PageToolbar>

    <!-- 请求脉搏条：一个请求一道流光，失败闪红。它回答的是数字回答不了的
         「此刻还有没有人用」—— 光带安静了就是没流量 -->
    <PulseBar class="dash-pulse" />

    <DataState
      :error="loadError"
      :has-data="hasStats"
      :loading="loading"
      title="看板数据加载失败"
      hint="看板数据来自后端统计接口，请确认后端服务是否正常，然后重试。"
      @retry="load()"
    >
    <!-- 概览四卡：一排四张（原来在左半边排成 2x2，右边的位置留给热力图；
         热力图与图表区移除后，四张卡占满整行，信息密度与参考站一致） -->
    <section class="summary-grid">
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
      :flash="costFlash"
      :heat="costHeat ?? undefined"
      :title="costHeat === 'hot' ? '有分组今日预算已超支' : costHeat === 'warm' ? '有分组今日消费已过预算 80%' : undefined"
    >
      <template #value>
        <AnimatedNumber :value="costValue" format="money" :currency="costCur" />
      </template>
      <template #icon><DollarOutlined /></template>
    </StatCard>
    <StatCard label="词元数量" :value="n(summary?.total_tokens)" tone="blue" :hint="'命中率 ' + ((summary?.cache_hit_rate ?? 0) * 100).toFixed(1) + '%'">
      <template #value>
        <!-- 格式切换的过渡：key 只绑格式，所以数值推送（每 2s）不触发这里，
             补间仍由 AnimatedNumber 自己做；点按钮换格式时旧值淡出上移、新值淡入 -->
        <Transition name="num-fmt" mode="out-in">
          <AnimatedNumber
            :key="tokenCompact ? 'compact' : 'full'"
            :value="summary?.total_tokens ?? null"
            :format="tokenCompact ? 'compact' : 'int'"
          />
        </Transition>
      </template>
      <template #suffix>
        <a-tooltip :title="tokenCompact ? '切换为完整数字' : '切换为紧凑缩写（M / B）'">
          <!-- 原生 button 而不是 a-button：这里只要一个图标位，
               antd 的链接按钮自带 padding 与字体色会跟卡片色调打架 -->
          <button
            type="button"
            class="fmt-toggle"
            :aria-label="tokenCompact ? '词元数量改为完整数字显示' : '词元数量改为紧凑缩写显示'"
            :aria-pressed="tokenCompact"
            @click="toggleTokenFmt"
          >
            <SwapOutlined />
          </button>
        </a-tooltip>
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
    </section>
    </DataState>

    <!-- 请求日志列表：**不**吃上面那组筛选 —— 它恒定显示全部最新请求
         （2026-09-16 站主要求）。筛过之后列表就看不到刚进来的调用了，
         而刚进来的几条恰恰是最该被看到的；筛选留给上面的概览卡，
         「这一部分用了多少」才是那些条件要回答的问题。
         两个排障深链（链路 / 仅失败）仍然生效：那是明确的排障动作，不是日常视角。
         面板自带外壳、表格、分页与详情抽屉（标题栏与导出按钮已去掉，见组件里的
         说明），失败时也有自己的错误态，不会把卡片那一排一起换成报错。 -->
    <RequestLogPanel
      ref="logPanel"
      :trace-id="traceId"
      :status-class="statusClass"
      :groups="filterGroups"
      :channels="filterChannels"
      @update:trace-id="traceId = $event"
      @update:status-class="statusClass = $event"
    />

    <!-- 昨日战报：每天第一次打开看板时自弹（含里程碑彩带判定），
         组件自包含 —— 拉不到数据就完全安静，页面不为它操心 -->
    <DailyReport />
  </div>
</template>

<style scoped>
.dashboard {
  /* 整页锁在一屏里：工具栏与卡片排按内容占高，下面的日志面板吃掉剩下的全部高度，
     面板底边因此与左侧栏最后一行齐平（两侧底部留白都是内容区的 --gap）。
     这一页的内容区是确定高度的弹性容器，见 MainLayout 的 .content-inner.is-fill
     与路由上的 meta.fill。

     为什么让面板「填满」而不是继续给表格体算一个「100vh 减一笔固定账」的高度：
     那笔账要把工具栏、卡片排、表头、分页、各处内外边距六七项加起来，其中卡片排
     的高度本身还会随文案换行在 83~98px 之间变（实测）—— 常量一旦与现实差一点，
     面板底边就差一点，而且差在哪一项上完全看不出来。站主 2026-09-17 反馈的
     「左侧栏与右侧容器底部不在一条水平线上」，实测正是面板底边比左侧栏高 7.6px。
     交给弹性分配之后，卡片换行变高、工具栏多一句提示、顶上多一条告警，
     表体自己让位，底边始终对齐。

     min-height: 0 不能省：弹性子项的自动最小尺寸默认是内容高度（这里是
     「50 行的表格」两千多像素），不归零就压不下去。 */
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

/* 日志面板 = 最后那一块，它负责吃掉剩余高度。
   margin-bottom 要去掉：内容区的下内边距已经是底部留白了，再叠一个 8px 外边距，
   面板就比左侧栏高出这 8px（正是上面那 7.6px 的来源）。 */
.dashboard > .panel:last-child {
  flex: 1 1 auto;
  min-height: 0;
  margin-bottom: 0;
  display: flex;
  flex-direction: column;
}

/* 概览四卡：一排四张，gap 恒为 8px（实测）。
   原来这里是 overview-row（左边 summary-grid 2x2 + 右边热力图面板）——
   热力图与图表区移除后不再需要外层两列，四张卡直接占满整行。
   参考站的四张卡本身也是等宽的一排，它之所以排成 2x2，
   是因为右半边让给了热力图 —— 那是「有热力图」时的布局。 */
.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--gap);
  margin-bottom: var(--gap);
}

/* 脉搏条贴在工具栏与概览卡之间：间距取 gap 的一半，
   它是仪器读数不是内容块，不该与卡片抢视觉重量 */
.dash-pulse {
  margin: calc(var(--gap) / 2) 0;
}

/* 词元卡右上角的格式切换按钮。
   尺寸刻意小于左侧图标块（48px 是主视觉，这里是辅助控件），
   圆角与其一致（8px）；配色从卡片根上的 tone 变量继承 ——
   词元卡是 blue 调，hover 浅底与图标块的 15% 透明底同一体系。
   --tone-* 由 StatCard 的 .tone-blue 定义在卡片根元素上，
   插槽内容渲染在其内部，变量沿 DOM 继承，不需要在这里重复取色 */
.fmt-toggle {
  width: 32px;
  height: 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
  transition:
    background-color 0.15s ease,
    color 0.15s ease,
    transform 0.1s ease;
}
.fmt-toggle:hover {
  background: var(--tone-bg);
  color: var(--tone-ink);
}
/* 按压反馈：缩一下再弹回，与 antd 按钮的体感一致 */
.fmt-toggle:active {
  transform: scale(0.92);
}
/* 键盘焦点必须可见（项目一贯的可访问性口径）：
   环的取值改由 theme.css 的全站 :focus-visible 统一给出（P1-7）——
   这里原来用卡片色调 --tone-ink，与其余控件的焦点环不是一个颜色。 */

/* 词元数字的格式切换过渡：旧值淡出上移、新值淡入（out-in 模式）。
   key 只绑格式，数值推送不经过这里 —— 滚动补间是 AnimatedNumber 自己的事。
   prefers-reduced-motion 由 theme.css 的全站块把 transition 压到 0.01ms，
   这里不用单独降级 */
.num-fmt-enter-active,
.num-fmt-leave-active {
  transition:
    opacity 0.2s ease,
    transform 0.2s ease;
}
.num-fmt-enter-from {
  opacity: 0;
  transform: translateY(4px);
}
.num-fmt-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}

/* DataState 的错误提示自带左右外边距（为列表页的面板布局设计），
   这里外层已经有内边距，去掉以免出现双重缩进 */
.dashboard :deep(.ds-alert) { margin: 0 0 var(--gap); }

/* 窄屏：卡片从四列退到两列，再退到一列。
   退档是为了不把卡片压到读不出数字，而不是为了塞下更多卡。 */
@media (max-width: 1100px) {
  .summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 700px) {
  .summary-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
