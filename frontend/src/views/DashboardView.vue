<script setup lang="ts">
// 数据看板 —— 2026-09-16 起「请求日志」整页并入这里：
// 上面是筛选栏与四张概览卡，下面是请求日志列表。
//
// 两者**不**共用筛选条件（2026-09-16 站主要求）：
//   概览卡按工具栏的时间范围 / 渠道 / 模型统计；
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
  SwapOutlined,
} from '@ant-design/icons-vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '@/api/client'
import { useThemeStore } from '@/stores/theme'
import ThemePicker from '@/components/ThemePicker.vue'
import PageToolbar from '@/components/PageToolbar.vue'
import StatCard from '@/components/StatCard.vue'
import AnimatedNumber from '@/components/AnimatedNumber.vue'
import DataState from '@/components/DataState.vue'
import PulseBar from '@/components/PulseBar.vue'
import LiveStatus from '@/components/LiveStatus.vue'
import BandwidthMeter from '@/components/BandwidthMeter.vue'
import DailyReport from '@/components/DailyReport.vue'
import RequestLogPanel from '@/components/RequestLogPanel.vue'
import { onLive, createThrottledLiveReloader } from '@/composables/useLive'
// 弹层的键盘可达性（打开移焦 / Esc 关闭 / 关闭还焦）：主题弹层、版本面板、列设置共用
import { usePopoverA11y } from '@/composables/usePopoverA11y'
// 轻提示走 utils/feedback 的出口（不是 antd 静态方法）：静态方法拿不到
// ConfigProvider 的主题上下文，8 套深色主题下会弹出浅色默认皮肤（见该文件说明）
import { message } from '@/utils/feedback'
// 金额一律走 utils/money.ts：符号与小数位数只此一份（见那里的说明）
import { currencyKeys, moneyText, primaryCurrency, symbolOf } from '@/utils/money'
import type { Channel, APIKey } from '@/api/types'
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
// ---- 筛选条件（时间范围 / 密钥 / 渠道 / 模型）全部持久化 ----
//
// 为什么要持久化：「只看某个渠道」是常态视角，每次打开页面、或从别的页面
// 切回来都要重选一遍，是纯粹的重复劳动。与渠道列表页的筛选同一套做法
// （见 utils/persistedChoice.ts）。
//
// 这四个条件是**整页**的：上面的卡片与下面的请求日志列表都按它们取数
//（2026-10-26 站主要求列表纳入筛选，撤销 2026-09-16 的「列表恒定全量」决定，
// 契约脚本同步反转 —— 列表与卡片说的是同一批请求，才不会有「卡片筛了、
// 列表没筛」的两套口径）。
const RANGE_KEY = 'dashboard-range'
const CHANNEL_KEY = 'dashboard-channel'
const MODEL_KEY = 'dashboard-model'
const KEY_KEY = 'dashboard-key'
// 词元卡片的数字格式：完整千分位 / 紧凑缩写（一律带 M/B 单位）。
// 阅读习惯跟着人走，与上面那组筛选共用同一套持久化机制，默认完整。
const TOKEN_FMT_KEY = 'dashboard-token-fmt'

// 哨兵值用 'all' 而不是 0：后端的约定是「不传参数＝不筛选」，
// 而界面上的「全部渠道」与「渠道 id=0」是两件事，混用迟早出错
const ALL = 'all'

// 紧凑格式的持久化取值是字符串（persistedChoice 只存字符串），
// 界面上用布尔更顺手：读时比较、写时反推
const tokenCompact = ref(readStoredChoice(TOKEN_FMT_KEY, ['full', 'compact'], 'full') === 'compact')
function toggleTokenFmt() {
  tokenCompact.value = !tokenCompact.value
  writeStoredChoice(TOKEN_FMT_KEY, tokenCompact.value ? 'compact' : 'full')
}

const range = ref('today')
const channelFilter = ref<string>(ALL)
const modelFilter = ref<string>(ALL)
// 密钥筛选（2026-10-26 加，分组筛选的接位者）：「这把密钥这段时间跑了多少」
// 是排查客户端问题的第一入口。卡片与日志列表同时吃它。
const keyFilter = ref<string>(ALL)
// 排障深链带来的两个临时条件（?trace_id=… / ?status_class=error）。
// 它们不持久化：下次打开还被它们筛着，会看到一张恒为 0 条的列表，
// 而且不记得自己什么时候套上的。
const traceId = ref('')
const statusClass = ref('')
/**
 * 详情抽屉当前打开的那一条（?log=123，2026-10-11 加）。
 *
 * 为什么不放在 RequestLogPanel 内部：它属于「这一页正在看什么」，
 * 与 trace/status 是同一类东西，都由 URL 与看板持有 —— 面板只管按它开抽屉。
 */
const logId = ref<number | null>(null)
// 只在进入页面时读一次（见 applyUrlFilters）：筛选状态不进地址栏，
// 免得用户以为地址栏能当书签用、却越用越乱
const route = useRoute()
const router = useRouter()

// 主题选择（工具栏右上角那枚按钮，2026-09-26 从侧栏底部迁来、
// 2026-09-30 从「一键翻浅深」改成「弹层里自选 12 套」——12 套主题再靠
// 一个按钮轮着切不现实）：状态、持久化与圆形扩散动画都在 stores/theme.ts，
// 这里只管弹层开合与顶部那两枚快捷按钮。
const themeStore = useThemeStore()
const themePanelOpen = ref(false)

const themeBtn = ref<HTMLElement | null>(null)
const themePanel = ref<HTMLElement | null>(null)
usePopoverA11y({
  open: themePanelOpen,
  trigger: themeBtn,
  panel: themePanel,
  preferred: '.seg-item.is-active'
})

// 列表面板的句柄：工具栏的「刷新」要连它一起刷（一页一个刷新按钮）
// 面板对外暴露 reload()（见 RequestLogPanel 的 defineExpose）—— 让列表重取一次。
//
// exportQuery() 仍在面板上暴露，但这里不再声明/使用：工具栏的「导出 CSV」
// 按钮已移除（2026-10-27），没有消费者了。留着类型声明等于给后来的人
// 一个「这里还能导出」的错误印象。
const logPanel = ref<{ reload: () => void } | null>(null)
// 筛选下拉的候选：来自管理接口，不是统计接口 —— 统计接口只回有流量的渠道，
// 而「筛一条今天还没被用过的渠道」是合理需求（结果就是 0）。
const filterChannels = ref<Channel[]>([])
const filterKeys = ref<APIKey[]>([])

// 渠道选项：图标 + 名字，平铺（分组已移除，组头随之消失）。
const channelOptions = computed(() => [
  { value: ALL, label: '全部渠道' },
  ...filterChannels.value.map(channelOption)
])

// 密钥选项：全部密钥（含停用的 —— 停用密钥的历史日志仍要能筛出来看）。
const keyOptions = computed(() => [
  { value: ALL, label: '全部密钥' },
  ...filterKeys.value.map((k) => ({
    value: String(k.id),
    label: k.name + (k.enabled ? '' : '（已停用）')
  }))
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
      ? filterChannels.value
      : filterChannels.value.filter((c) => String(c.id) === channelFilter.value)
  return [...new Set(src.flatMap((c) => c.models || []))].sort()
})

const modelOptions = computed(() => [
  { value: ALL, label: '全部模型' },
  ...visibleModels.value.map((m) => ({ value: m, label: m }))
])

// ---- 三个筛选下拉的宽度：跟着当前选中项的文字走（2026-10-27）----
//
// 原来写死 180 / 240 / 180px（合计 600px），而「全部密钥」这类五字选项在
// 240px 里空出一大片。现在由 CSS sizer 技巧按内容撑开，实现与原理见
// <style> 里的 .filter-select（那里才是该解释 fit-content 为什么不行的地方）。
//
// 这里只负责把「当前选中项的文字」取出来喂给 sizer：label 与 antd 实际
// 渲染的文本必须一致，否则撑出来的宽度与内容对不上。
function selectedLabel(options: { value: string; label: string }[], value: string): string {
  return options.find((o) => o.value === value)?.label ?? ''
}

const keyFilterLabel = computed(() => selectedLabel(keyOptions.value, keyFilter.value))
const channelFilterLabel = computed(() => selectedLabel(channelOptions.value, channelFilter.value))
const modelFilterLabel = computed(() => selectedLabel(modelOptions.value, modelFilter.value))

// syncFilters 把下级筛选夹回合法值：换了渠道，原来选的模型可能已不在候选里。
// 不夹的话查询条件会停在一个空集合上（列表恒为 0 条、卡片全是 0），
// 而界面上看不出原因。
function syncFilters() {
  if (modelFilter.value !== ALL && !visibleModels.value.includes(modelFilter.value)) {
    modelFilter.value = ALL
  }
}

function persistFilters() {
  writeStoredChoice(RANGE_KEY, range.value)
  writeStoredChoice(CHANNEL_KEY, channelFilter.value)
  writeStoredChoice(MODEL_KEY, modelFilter.value)
  writeStoredChoice(KEY_KEY, keyFilter.value)
}

// 存下来的筛选值可能指向已经删掉的渠道 / 模型 / 密钥。那种状态的表现是
// 「所有数字都是 0」或「列表恒为 0 条」，从界面上完全看不出原因 ——
// 所以列表到手后校验一次，不合法就退回「全部」（与渠道列表页的
// applyStoredGroupFilter 同一套做法）。
//
// 历史遗留：dashboard-group 这个键是分组筛选（2026-09-26 移除）留下的，
// 不会被读取也不会被清理 —— 孤儿键无害，不值得写迁移代码。
//
// 从 URL 带着条件进来的那一次**不恢复**本地筛选（条件由 applyStoredFilters
// 里的早退判据清点）：那种链接是要发给别人、或以后自己再打开的，同一个链接
// 应该在哪台机器上、隔多久打开都显示同一批记录。若再与收件人自己记着的
// 渠道筛选相交，链接会显示成一张空表 —— 看起来像日志丢了，而且两个人看到
// 的还不一样。
// 这里只是「这一次不套用」，不写回存储：用户并没有改自己的视角。
function applyStoredFilters() {
  // URL 带了任何条件（深链或视角）就整段不跑：applyUrlFilters 已经把这些
  // 值写进各筛选了，本地存储（或空存储的默认值）再来一遍会把链接带来的
  // 视角整体覆盖掉 —— 带条件的链接应当「打开就是那个视角」，
  // 覆盖发生的表现正是打开者看到自己的旧筛选（2026-10-26 评审 R1；HEAD 时
  // 只影响卡片，列表纳入筛选后影响翻倍）。
  //
  // 注：工具栏上的「复制当前视角链接」按钮已移除（2026-10-27），但**读**这一侧
  // 必须保留 —— 用户仍可从地址栏复制、或从收藏夹/历史记录进来。
  const q = route.query
  if (
    q.trace_id || q.status_class || q.log ||
    q.range || q.channel || q.model || q.key
  ) {
    return
  }
  if (traceId.value || statusClass.value) return
  range.value = readStoredChoice(RANGE_KEY, ranges.map((r) => r.key), 'today')
  channelFilter.value = readStoredChoice(
    CHANNEL_KEY,
    [ALL, ...filterChannels.value.map((c) => String(c.id))],
    ALL
  )
  modelFilter.value = readStoredChoice(MODEL_KEY, [ALL, ...visibleModels.value], ALL)
  keyFilter.value = readStoredChoice(
    KEY_KEY,
    [ALL, ...filterKeys.value.map((k) => String(k.id))],
    ALL
  )
  persistFilters()
}

async function loadFilters() {
  try {
    const [c, k] = await Promise.all([
      api.get<{ items: Channel[] }>('/channels'),
      api.get<{ items: APIKey[] }>('/keys')
    ])
    filterChannels.value = c.items || []
    filterKeys.value = k.items || []
  } catch {
    // 拉不到就只剩「全部」一个选项，看板本身照常取数 ——
    // 一个筛选框不该让整页打不开
  }
  applyStoredFilters()
}

// applyUrlFilters 只在进入页面时读一次 URL：筛选状态不进地址栏，
// 免得用户以为地址栏能当书签用、却越用越乱（工具栏那几个筛选记在本地，
// 见上面的 dashboard-* 键 —— 「下次打开还是这个视角」与「地址栏当书签」是两件事）。
// 用法上它是排障深链：?trace_id=… 看一条链路、?status_class=error 只看失败。
// applyUrlFilters 只在进入页面时读一次 URL。
//
// 两类参数在这里合流，但**来源不同**，别把它们混成一件事：
//   1. 排障深链（trace_id / status_class / log）—— 这些会随操作写回地址栏
//      （见 syncUrl），因为它们描述的是「我正在看哪一条 / 哪一批」，交接得出去；
//   2. 分享链接里带的视角（range / channel / model）—— 工具栏上的筛选**变化时
//      不写地址栏**（见 syncUrl 的说明：那会让返回键变成「上一个筛选」），
//      但手写/收藏的链接会带上它们，所以这里必须读 ——
//      否则打开后是另一个视角，而点链接的人无从察觉。
//      （2026-10-27 起「复制当前视角链接」按钮已移除，读取侧不变。）
//      `range` 在这里做四档白名单再入库：/stats 与 /logs 对坏值的处理口径
//      不同（前者静默回「今天」，后者 400），同一页打开坏链接会「卡片正常、
//      列表报错且重试无解」；前端校掉坏值后两半同口径（2026-10-26 评审 R5）。
function applyUrlFilters() {
  const tid = String(route.query.trace_id || '').trim()
  if (tid) traceId.value = tid
  const sc = String(route.query.status_class || '').trim()
  if (sc === 'error' || sc === 'success') statusClass.value = sc
  // ?log=123：直接展开那一条的详情（2026-10-11）。
  // 非法值当没传：地址栏里的东西是用户能改的，解析失败不该让页面出错
  const lid = Number(String(route.query.log || '').trim())
  if (Number.isInteger(lid) && lid > 0) logId.value = lid
  const rg = String(route.query.range || '').trim()
  if (rg && ranges.some((r) => r.key === rg)) range.value = rg
  const ch = String(route.query.channel || '').trim()
  if (ch) channelFilter.value = ch
  const md = String(route.query.model || '').trim()
  if (md) modelFilter.value = md
  const ky = String(route.query.key || '').trim()
  if (ky) keyFilter.value = ky
}

/**
 * 把「当前视角」写回地址栏（2026-10-11）。
 *
 * 只写三类东西：**排障深链**（trace_id / status_class / 打开的 log）。
 * 工具栏那几个筛选（时间 / 密钥 / 渠道 / 模型）仍然不进 URL ——
 * 它们记在本地、是「下次打开还是这个视角」的长期偏好，每变一次就写 URL
 * 会让历史记录变成一串筛选状态，返回键再也不是「上一个页面」。
 *
 * 用 replace 而不是 push：在抽屉里按 ↑ / ↓ 连看十条，不该在历史里留下十条。
 * 相同的 query 不重复导航（replace 同一条也会触发一次导航，白跑一遍守卫）。
 */
function syncUrl() {
  const q: Record<string, string> = {}
  if (traceId.value) q.trace_id = traceId.value
  if (statusClass.value) q.status_class = statusClass.value
  if (logId.value != null) q.log = String(logId.value)
  const cur = route.query
  const same =
    Object.keys(q).length === Object.keys(cur).length &&
    Object.entries(q).every(([k, v]) => cur[k] === v)
  if (same) return
  void router.replace({ query: q })
}

const activeFilterCount = computed(() => {
  let cnt = 0
  if (channelFilter.value !== ALL) cnt++
  if (modelFilter.value !== ALL) cnt++
  if (keyFilter.value !== ALL) cnt++
  if (traceId.value) cnt++
  if (statusClass.value) cnt++
  return cnt
})

function resetAllFilters() {
  channelFilter.value = ALL
  modelFilter.value = ALL
  keyFilter.value = ALL
  traceId.value = ''
  statusClass.value = ''
  writeStoredChoice(CHANNEL_KEY, ALL)
  writeStoredChoice(MODEL_KEY, ALL)
  writeStoredChoice(KEY_KEY, ALL)
  syncUrl()
  void load()
  logPanel.value?.reload()
}

// 注意：a-radio-group 的 change 给的是**事件对象**，不是值（a-select 给的是值，
// 两者不一样）。所以这里不接收参数、也不赋值 —— v-model 已经更新过 range。
// 之前写成 onRangeChange(v) { range.value = v }，range 就变成了一个事件对象：
// 查询串成了 ?range=[object Object]，后端认不出、退回「今天」，
// 存储里也写进 "[object Object]" —— 界面上筛选项看着是选中的，数据却是今天的。
//
// 这几个处理函数只重取**统计**（卡片）。列表吃同一组条件，
// 但它靠 props 的 watch 自己重取（面板内部回第一页），这里不该顺手调
// logPanel.reload()：那会让列表丢掉「回第一页」的语义，还会多发一次查询。
// 只有一个「刷新」按钮例外：它的语义就是两块都刷（见 reloadAll）。
function reloadStats() {
  load()
}

function onRangeChange() {
  persistFilters()
  reloadStats()
}

function onChannelChange(v: string) {
  channelFilter.value = v
  syncFilters()
  persistFilters()
  reloadStats()
}

function onKeyChange(v: string) {
  keyFilter.value = v
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

// 三类深链状态变化就同步地址栏（见 syncUrl）。
//
// 用 watch 而不是在每个赋值的旁边手写一次：赋值点有五个（URL 初始读取、
// 工具栏关标签、详情里的「只看这条链路」、空表上的清除条件、抽屉的开与关），
// 漏掉任何一处，地址栏讲的就是另一个故事。
watch([traceId, statusClass, logId], syncUrl)


// 一页只有一个「刷新」：卡片与列表一起刷。
// 拆成两个按钮的话，用户永远要猜「哪个按钮刷的是哪块」。
//
// 这里可以安全地直接叫面板重取（与上面四个处理函数不同）：
// 点刷新时没有任何筛选在变，不存在「面板拿着旧 props 发查询」的问题。
function reloadAll() {
  load()
  logPanel.value?.reload()
}

// 渠道 / 模型 / 密钥不选时不带参数（后端把「不传」当作不筛选）
function statsQuery() {
  let s = '?range=' + range.value
  if (keyFilter.value !== ALL) s += '&key_id=' + keyFilter.value
  if (channelFilter.value !== ALL) s += '&channel_id=' + channelFilter.value
  if (modelFilter.value !== ALL) s += '&model=' + encodeURIComponent(modelFilter.value)
  return s
}


// 卡片上的数字统一走千分位（与参考站一致）。
// 图表相关的那些取值入口（色板、分桶时间格式、日期键）随图表一起删掉了。
function n(v: number | undefined) {
  return (v ?? 0).toLocaleString('zh-CN')
}

// ---- 首屏副标：没数据时给「—」，不能拿 null 当 0 用（2026-10-11）----
//
// 原来这三行是 `'失败 ' + n(summary?.errors) + ' 次'` 这类写法：summary 还没到时
// `?? 0` 会拼出「失败 0 次」「命中率 0.0%」「平均首字耗时 0ms」——
// 三条都是**错误的结论**，而它们看起来和真实数据完全一样，用户会照着它下判断。
// 加载中显示「—」既诚实又和加载完成后的形状一致（不会因为文字变长而换行）。
const errorsHint = computed(() =>
  summary.value ? '失败 ' + n(summary.value.errors) + ' 次' : '—'
)
const cacheHint = computed(() =>
  summary.value ? '命中率 ' + ((summary.value.cache_hit_rate ?? 0) * 100).toFixed(1) + '%' : '—'
)
const firstByteHint = computed(() =>
  summary.value ? '平均首字耗时 ' + Math.round(summary.value.avg_first_byte_ms ?? 0) + 'ms' : '—'
)

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

// ---- 静默重取失败的留痕（2026-10-11）----
//
// 实时推送驱动的重取是静默的（不点加载态、失败不弹提示）—— 一次抖动确实不该
// 打断视线。但**连续**失败是另一回事：那时卡片会一直停在几分钟前的数字，
// 看起来却和「刚刷新过」一模一样，用户会据此判断「今天就这么点请求」。
// 连续失败达到阈值就把 loadError 写进去，复用 DataState 现成的
// 「顶部告警 + 保留数据 + 重试」那条分支，不新增组件、不清空用户正在看的内容。
// 弹窗另加 10 分钟限流（持续失败时每秒弹一次比不提示更烦）。
const SILENT_FAIL_LIMIT = 3
const SILENT_ALERT_MS = 10 * 60 * 1000
let silentFailures = 0
let lastSilentAlertAt = 0

async function load(opts: { silent?: boolean } = {}) {
  const silent = !!opts.silent
  const seq = ++loadSeq
  if (!silent) loading.value = true
  if (!silent) loadError.value = ''
  try {
    // 只剩概览这一个请求了：图表与热力图移除后，timeseries / models /
    // channels / heatmap 四个接口不再由前端调用（后端保留，见 UI-SPEC.md 第九节）。
    // 这也让卡片刷新变快 —— 原来一次刷新要打五个接口，任何一个慢都会拖住整排数字。
    const data = await api.get<Summary>('/stats/summary' + statsQuery())
    // 已经有更新的请求发出去了：这次的结果（以及它的错误、它的 loading）都作废
    if (seq !== loadSeq) return
    summary.value = data
    // 取到了数据：连续失败计数归零，并把静默失败留下的告警收掉
    // （原来 loadError 只在**非静默**请求开始时清，于是静默重取成功之后
    //  那条红色告警会一直挂在页面上 —— 粘住的告警）
    silentFailures = 0
    loadError.value = ''
  } catch (e: any) {
    // 已经有更新的请求发出去了：这次失败也作废
    if (seq !== loadSeq) return
    const text = e?.message || '加载失败'
    if (silent) {
      silentFailures++
      if (silentFailures >= SILENT_FAIL_LIMIT) {
        loadError.value = `数据刷新已连续失败 ${silentFailures} 次，以下是最后一次成功取到的数字`
        if (Date.now() - lastSilentAlertAt > SILENT_ALERT_MS) {
          lastSilentAlertAt = Date.now()
          message.error(text)
        }
      }
      return
    }
    loadError.value = text
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
// success_rate 必须是数字才交给补间：后端万一回了缺字段的 200（或旧版本
// 没有这个字段），undefined * 100 = NaN， AnimatedNumber 会把「NaN%」
// 当成真值滚给用户看 —— 那比空着更糟。缺了就回 null（显示 --）。
const rateValue = computed(() => {
  const r = summary.value?.success_rate
  return typeof r === 'number' && Number.isFinite(r) ? r * 100 : null
})

// 实时数值：服务端**有新日志就立刻**算一次今日汇总，变了才推；
// 没流量时另有一条 2 秒的兜底节拍（见 backend/internal/api/live.go）。
// 之所以强调「有新日志就立刻」：这条推送与下面列表的新行必须落在同一拍上，
// 否则会出现「入场动画播完了数字才开始滚」（2026-09-18 站主反馈，实测差 1064ms）。
//
// 推送来的永远是「今天 + 全站」那一份（见 live.go），所以：
// - 当前正好是「今天 + 全站」→ 直接合并（它不带 range 等本地查询字段，
//   整个替换会把页面依赖的其它字段抹掉，这里只做字段合并）；
// - 当前是别的视角（近 7 天 / 筛了渠道或模型）→ 合并不了，但**收到推送本身
//   就说明有新流量**（服务端只在数据真的变了才推），于是立刻静默重取一次
//   当前视角。以前这里是直接 return，选着「近 7 天」或筛了渠道时卡片就
//   再也不动了，只能靠手点「刷新」；后来改成 setTimeout(1500) 之后卡片会动了，
//   但那 1.5 秒人为延迟恰好把数字的变动推到入场动画（sweep 2s / glow 1.6s）
//   结束之后 —— 站主二次反馈「动画播完了数值才变动」指的就是它。
//   现在：首帧立即重取（数字与动画同时开始变动），只保留 1 秒最小间隔挡
//   持续流量下的请求风暴（节流器的取舍见 useLive.createThrottledLiveReloader）。
const statsReloader = createThrottledLiveReloader(() => load({ silent: true }))

onUnmounted(() => {
  statsReloader.dispose()
})

// ---- 读屏播报：把「数字在变」这件事也说给读屏用户（2026-10-10）----
//
// 为什么需要：四个概览卡由 WebSocket 实时推送驱动，视觉上是数字在滚，
// 而读屏用户拿到的是一个静止的页面 —— 「此刻还有人在用」这个信息
// 完全传达不到（这是页面上唯一一处纯视觉的实时反馈）。
//
// 为什么是「首帧立即 + 之后最多 30 秒一次」，而不是每次变化都播：
// aria-live 会把每一处文本变化都念出来，持续流量下就是每几秒念一长串
// 数字，比不播更烦。30 秒是一个「还在动」的心跳节拍；首帧那一次是让用户
// 确认数据已经到位（否则只能靠猜）。数值没变时不播 —— 服务端本来也只在
// 数据真的变了才推。
const liveText = ref('')
let liveAnnouncedAt = 0
const LIVE_ANNOUNCE_MS = 30_000
watch(
  () => [summary.value?.requests, summary.value?.total_tokens, summary.value?.success_rate],
  () => {
    const s = summary.value
    if (!s) return
    const now = Date.now()
    // 0 表示还没播过：首帧立即播，之后按节拍
    if (liveAnnouncedAt !== 0 && now - liveAnnouncedAt < LIVE_ANNOUNCE_MS) return
    liveAnnouncedAt = now
    liveText.value =
      '数据已更新：请求 ' + n(s.requests) + ' 次，词元 ' + n(s.total_tokens) +
      '，成功率 ' + ((s.success_rate ?? 0) * 100).toFixed(1) + '%'
  }
)

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

onLive('stats', (data: Record<string, unknown>) => {
  // 首屏还没加载完时忽略推送：那一份由 load() 负责，
  // 提前合并会得到一个缺字段的 summary
  if (!summary.value) return
  // 推送来的那份是「今天 + 全站」，只有当前正好是这个视角才能直接合并。
  // 渠道 / 模型 / 密钥筛选也算别的视角：不判它们的话，筛着某个维度时收到的
  // 全站数字会把卡片顶掉（比不刷新更糟 —— 它看起来像是刷新了）
  if (
    range.value !== 'today' ||
    channelFilter.value !== ALL ||
    modelFilter.value !== ALL ||
    keyFilter.value !== ALL
  ) {
    statsReloader.request()
    return
  }
  summary.value = { ...summary.value, ...(data as object) } as Summary
})

// 顺序不能反：先把渠道 / 密钥列表拿到、把存下来的筛选值校验过，再去取统计。
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
    <!-- 工具栏：时间范围 + 密钥 / 渠道 / 模型 + 刷新。
         卡片与下面的日志列表吃**同一组**条件（2026-10-26 站主要求列表纳入筛选，
         撤销 2026-09-16 的「列表恒定全量」决定；契约脚本已同步反转）。

         这里曾经放过一句说明筛选作用范围的提示，2026-09-17 应站主要求**移除**
         （提交 c56354b）。所以别再往这里加提示 ——
         `frontend/scripts/check-contracts.mjs` 已把当初的契约反转成禁止式，
         断言工具栏**不得**出现该文案。这个文件里连那句话的原文都不写 ——
         契约是整文件正则匹配，注释里带上同样会把检查弄红。 -->
    <PageToolbar label="时间范围">
      <!-- 用 a-radio-group 而不是手写 <button>：
           这里原来写的是 class="pill-btn"，但那个类在项目里从未定义过，
           于是按钮一直是浏览器默认样式（灰底、深色描边、字号偏小），
           和其余部分完全不像一套东西。
           换成 Ant Design 的组件还能自动跟随明暗主题与设计令牌。 -->
      <a-radio-group v-model:value="range" button-style="solid" @change="onRangeChange">
        <a-radio-button v-for="r in ranges" :key="r.key" :value="r.key">{{ r.label }}</a-radio-button>
      </a-radio-group>
      <!-- 密钥排在渠道前面：排查「某个客户端在干什么」时它是第一入口 -->
      <span class="filter-select">
        <span class="filter-sizer" aria-hidden="true">{{ keyFilterLabel }}</span>
        <a-select
          v-model:value="keyFilter"
          :options="keyOptions"
          :dropdown-match-select-width="false"
          popup-class-name="filter-dropdown"
          @change="onKeyChange"
        />
      </span>
      <!-- 渠道 / 模型紧跟在密钥右边：它们回答的是同一类问题
           （「下面这些数字算的是哪一部分」），放在一起才读得成一句话 -->
      <span class="filter-select">
        <!-- 渠道的选中值渲染成「图标 + 名字」（见 ChannelOption.vue），
             sizer 只量文字会窄出 22px（16px 图标 + 6px 间距），
             长渠道名因此会被提前截成「…」。加一个 is-with-icon 补上这一段。 -->
        <span class="filter-sizer is-with-icon" aria-hidden="true">{{ channelFilterLabel }}</span>
        <a-select
          v-model:value="channelFilter"
          :options="channelOptions"
          :dropdown-match-select-width="false"
          popup-class-name="filter-dropdown"
          @change="onChannelChange"
        >
          <!-- 下拉项与选中值是 antd 的两个插槽，内容交给同一个组件渲染：
               分开写迟早会出现「下拉里有图标、选完就没了」这种不一致 -->
          <template #option="opt"><ChannelOption :option="opt" /></template>
          <template #optionLabel="opt"><ChannelOption :option="opt" /></template>
        </a-select>
      </span>
      <!-- 模型候选按渠道白名单收窄（见 visibleModels） -->
      <span class="filter-select">
        <span class="filter-sizer" aria-hidden="true">{{ modelFilterLabel }}</span>
        <a-select
          v-model:value="modelFilter"
          :options="modelOptions"
          :dropdown-match-select-width="false"
          popup-class-name="filter-dropdown"
          @change="onModelChange"
        />
      </span>
      <!-- 排障深链带来的两个临时条件：只从 URL 或日志详情里进来，平时不占地方 -->
      <a-tag v-if="traceId" closable :title="traceId" @close="clearExtra('trace')">
        链路 {{ traceId.slice(0, 8) }}…
      </a-tag>
      <a-tag v-if="statusClass" closable @close="clearExtra('status')">
        {{ statusClass === 'error' ? '仅失败' : '仅成功' }}
      </a-tag>
      <!-- 多维滤波透镜聚焦指示与一键还原（方案 5） -->
      <span
        v-if="activeFilterCount > 0"
        class="filter-lens-badge"
        title="当前已激活多维过滤，点击一键恢复全局视角"
        @click="resetAllFilters"
      >
        <span class="lens-dot" />
        已聚焦 {{ activeFilterCount }} 维度
        <span class="lens-reset-icon" aria-hidden="true">✕</span>
      </span>
      <template #right>
        <!-- 主题切换按钮（全新极客形态）与跟随系统分段胶囊弹层 -->
        <a-popover
          v-model:open="themePanelOpen"
          trigger="click"
          placement="bottomRight"
          overlay-class-name="theme-popover-compact"
        >
          <button
            ref="themeBtn"
            type="button"
            class="theme-btn"
            :title="`界面主题：${themeStore.mode === 'auto' ? '跟随系统' : themeStore.current.name}`"
            :aria-label="`选择界面主题，当前 ${themeStore.mode === 'auto' ? '跟随系统' : themeStore.current.name}`"
            aria-haspopup="dialog"
            :aria-expanded="themePanelOpen"
          >
            <!-- 动态三态图标：跟随系统(电脑) / 暖白(太阳) / 深灰(月亮) -->
            <svg v-if="themeStore.mode === 'auto'" viewBox="0 0 16 16" class="tb-svg-icon" fill="none" stroke="currentColor" stroke-width="1.3">
              <rect x="2" y="2.5" width="12" height="8.5" rx="1.5" />
              <path d="M6 13.5h4M8 11v2.5" />
            </svg>
            <svg v-else-if="themeStore.mode === 'light'" viewBox="0 0 16 16" class="tb-svg-icon is-light" fill="none" stroke="currentColor" stroke-width="1.3">
              <circle cx="8" cy="8" r="3" />
              <path d="M8 1.5v1.5M8 13v1.5M1.5 8h1.5M13 8h1.5M3.4 3.4l1.1 1.1M11.5 11.5l1.1 1.1M3.4 12.6l1.1-1.1M11.5 4.5l1.1-1.1" />
            </svg>
            <svg v-else viewBox="0 0 16 16" class="tb-svg-icon is-dark" fill="none" stroke="currentColor" stroke-width="1.3">
              <path d="M13.5 9.5A5.5 5.5 0 1 1 6.5 2.5a4.5 4.5 0 0 0 7 7z" />
            </svg>
          </button>
          <template #content>
            <div ref="themePanel" class="theme-panel-compact" role="dialog" aria-label="界面主题">
              <ThemePicker dense />
            </div>
          </template>
        </a-popover>
        <!-- 一页一个「刷新」：卡片与列表一起刷（见 reloadAll）。
             图标化与另外三页的工具栏统一 —— 刷新是高频动作，位置固定后
             图标即可辨认；title 与 aria-label 兜住悬停提示与读屏 -->
        <a-button :loading="loading" title="刷新（快捷键 R）" aria-label="刷新" aria-keyshortcuts="r" @click="reloadAll()">
          <ReloadOutlined />
        </a-button>
      </template>
    </PageToolbar>

    <!-- 请求脉搏条：一个请求一道流光，失败闪红。它回答的是数字回答不了的
         「此刻还有没有人用」—— 光带安静了就是没流量。
         右侧那枚胶囊回答的是另一半：「光带安静」到底是因为没流量，
         还是因为实时连接断了（两者看起来一模一样，而处置完全不同）。
         带宽读数排在胶囊右边：它回答的是第三个问题 ——「这台机器还有多少
         余量」，包含备份同步、系统更新这些网关看不见的流量（见组件说明）。 -->
    <div class="dash-live-row">
      <PulseBar class="dash-pulse" />
      <BandwidthMeter class="dash-bandwidth" />
      <LiveStatus class="dash-live-status" />
    </div>

    <DataState
      :error="loadError"
      :has-data="hasStats"
      :loading="loading"
      title="看板数据加载失败"
      hint="看板数据来自后端统计接口，请确认后端服务是否正常，然后重试。"
      @retry="load()"
    >
    <!-- 首屏骨架（2026-10-11）：四张与加载完成态同形的卡片。
         原来首屏是 DataState 的「转圈面板」，而下面的日志面板是兄弟节点 ——
         它先取数成功，卡片随后整块长出，把表格往下推 90~150px 并触发一次
         列宽重测。用骨架卡占住位置，首屏形状 = 加载完成形状，位移消失。 -->
    <template #loading>
      <section class="summary-grid">
        <StatCard label="请求数量" loading tone="purple">
          <template #icon><ApiOutlined /></template>
        </StatCard>
        <StatCard label="消耗金额" loading tone="orange">
          <template #icon><DollarOutlined /></template>
        </StatCard>
        <StatCard label="词元数量" loading tone="blue">
          <template #icon><ThunderboltOutlined /></template>
        </StatCard>
        <StatCard label="成功率" loading tone="green">
          <template #icon><CheckCircleOutlined /></template>
        </StatCard>
      </section>
    </template>
    <!-- 概览四卡：一排四张（原来在左半边排成 2x2，右边的位置留给热力图；
         热力图与图表区移除后，四张卡占满整行，信息密度与参考站一致） -->
    <section class="summary-grid">
      <StatCard label="请求数量" :value="n(summary?.requests)" tone="purple" :hint="errorsHint">
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
    >
      <template #value>
        <AnimatedNumber :value="costValue" format="money" :currency="costCur" />
      </template>
      <template #icon><DollarOutlined /></template>
    </StatCard>
    <StatCard label="词元数量" :value="n(summary?.total_tokens)" tone="blue" :hint="cacheHint">
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
      :hint="firstByteHint"
    >
      <template #value>
        <AnimatedNumber :value="rateValue" format="percent" />
      </template>
      <template #icon><CheckCircleOutlined /></template>
    </StatCard>
    </section>

    <!-- 读屏播报（2026-10-10）：四个卡片是实时推送的，视觉上数字在滚、
         读屏用户却完全无感 —— 页面「看起来是活的」这件事对他们不存在。
         节流规则见 liveText：首帧立即播一次（确认数据到了），之后最多
         每 30 秒一次，且只在数值真的变了时才播。
         用 .sr-only 而不是 display:none：后者会让内容从无障碍树上消失，
         aria-live 也就无从播报。 -->
    <p class="sr-only" aria-live="polite">{{ liveText }}</p>
    </DataState>

    <!-- 请求日志列表：与上面的卡片吃**同一组**筛选（2026-10-26 站主要求，
         撤销 2026-09-16 的「列表恒定全量」决定）。两个排障深链
         （链路 / 仅失败）仍然生效：那是明确的排障动作，不是日常视角。
         面板自带外壳、表格、分页与详情抽屉（标题栏与导出按钮已去掉，见组件里的
         说明），失败时也有自己的错误态，不会把卡片那一排一起换成报错。 -->
    <RequestLogPanel
      ref="logPanel"
      :trace-id="traceId"
      :status-class="statusClass"
      :log-id="logId"
      :range="range"
      :channel-id="channelFilter"
      :model="modelFilter"
      :key-id="keyFilter"
      :channels="filterChannels"
      @update:trace-id="traceId = $event"
      @update:status-class="statusClass = $event"
      @update:log-id="logId = $event"
    />

    <!-- 昨日战报：每天第一次打开看板时自弹（含里程碑彩带判定），
         组件自包含 —— 拉不到数据就完全安静，页面不为它操心 -->
    <DailyReport />
  </div>
</template>

<style scoped>
/* 筛选下拉的宽度由内容决定（2026-10-27）。
 *
 * 结构是 sizer 技巧：一个隐藏的同文本 span + 下拉放进同一格 inline-grid，
 * 格子宽度取两者较宽者，于是下拉被撑到「刚好放下这段文字」。
 *
 * 为什么不用 width: fit-content：antd 把选中文字渲染在绝对定位的
 * .ant-select-selection-item 里，根节点量不到它的宽度，fit-content 会塌成
 * 一条窄缝。sizer 量的是我们自己写的文本，与 antd 内部结构无关。
 *
 * 上下限：min-width 兜住空内容（不挤成缝），max-width 防止超长模型名
 * 把工具栏撑爆 —— 超出部分 antd 自己省略，完整值在下拉列表里可见。 */
.filter-select {
  display: inline-grid;
  align-items: center;
  min-width: 108px;
  max-width: 260px;
  /* 不许被 flex 压扁：工具栏允许换行，被压扁的话下拉里的文字会先省略 */
  flex: 0 0 auto;
}

/* sizer 与下拉叠在同一个格子里：两者都显式落在 1 行 1 列，
   格子宽度由较宽者决定 —— 这就是「宽度跟着内容走」的来源。
   不加这条的话 grid 默认按列排布，两者会并排，宽度变成两者之和。 */
.filter-select > * {
  grid-row: 1;
  grid-column: 1;
}

/* 隐藏的测量用文本。不能用 display:none / visibility:hidden ——
   那两种都不产生盒子，量不出宽度；用 opacity + 超出裁剪让它不可见但可量。 */
.filter-sizer {
  opacity: 0;
  pointer-events: none;
  user-select: none;
  white-space: nowrap;
  overflow: hidden;
  /* 必须与 antd 选中项的排版一致，否则量出来的宽度会差一截：
     antd 中号控件（Select 默认 size）的字号是 14px，即 --font-size-panel；
     用 --font-size-base（16px）量会宽出约 2px，框就比需要的更大 ——
     而这一改动的要求正是「别留多余的宽度」。 */
  font-size: var(--font-size-panel);
  line-height: 1;
  /* 给右侧的下拉箭头与内边距留位置：antd 的 .ant-select-selector 有
     左右内边距 + 一个 12px 的箭头图标，直接用文字宽度会和内容打架 */
  padding-inline-end: 30px;
}

/* 带图标的选项（渠道）：选中值前面还有一个 16px 图标 + 6px 间距，
   补 22px 才能让下拉装得下完整的「图标 + 名字」。
   与 ChannelOption.vue 里的 :size="16" 和 gap: 6px 必须一致 ——
   那两处改了这里也要改，否则渠道名会被提前截断。 */
.filter-sizer.is-with-icon {
  padding-inline-start: 22px;
}

/* 下拉本身铺满格子 */
.filter-select :deep(.ant-select) {
  width: 100%;
}

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

/* 脉搏条与连接状态胶囊同占一行：脉搏条吃掉剩余宽度，胶囊贴右。
   两者回答的是同一个问题的两半（「有没有流量」与「这条线还通不通」），
   放在一行里才读得通。间距取 gap 的一半 —— 它们是仪器读数不是内容块，
   不该与卡片抢视觉重量。 */
.dash-live-row {
  display: flex;
  align-items: center;
  gap: var(--gap);
  margin: calc(var(--gap) / 2) 0;
}

.dash-pulse {
  flex: 1;
  min-width: 0;
}

/* 胶囊本身是 flex 项，别被压扁（它里面的文字不允许换行） */
.dash-live-status {
  flex: 0 0 auto;
}

/* 多维滤波透镜聚焦指示与一键还原（方案 5） */
.filter-lens-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 1px 8px;
  border-radius: var(--radius-pill);
  background: color-mix(in oklab, var(--color-primary) 14%, transparent);
  border: 1px solid color-mix(in oklab, var(--color-primary) 35%, transparent);
  color: var(--text-primary-ink);
  font-size: var(--font-size-xs);
  cursor: pointer;
  box-shadow: 0 0 8px color-mix(in oklab, var(--color-primary) 20%, transparent);
  transition: all 0.2s var(--ease-expo);
}
.filter-lens-badge:hover {
  background: color-mix(in oklab, var(--color-primary) 22%, transparent);
  transform: translateY(-1px);
}
.lens-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--color-primary);
  box-shadow: 0 0 4px var(--color-primary);
}
.lens-reset-icon {
  font-size: var(--font-size-2xs);
  opacity: 0.7;
}

/* 工具栏右上角的主题按钮（全新极客形态）：32px 方圆角微型控制台按钮 */
.theme-btn {
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-bg) 60%, var(--color-fg));
  color: var(--color-text);
  cursor: var(--cursor-hand);
  transition: all 0.2s var(--ease-expo);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.05);
}

.theme-btn:hover {
  background: var(--color-icon-hover-bg);
  border-color: color-mix(in oklab, var(--color-primary) 50%, var(--color-border));
  transform: translateY(-1px);
}

.theme-btn:active {
  transform: scale(0.92);
}

.tb-svg-icon {
  width: 15px;
  height: 15px;
  flex: none;
}

.tb-svg-icon.is-light {
  color: var(--text-amber);
  filter: drop-shadow(0 0 3px var(--color-orange));
}

.tb-svg-icon.is-dark {
  color: var(--text-primary-ink);
  filter: drop-shadow(0 0 3px var(--text-primary-ink));
}

.theme-panel-compact {
  padding: 2px;
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
  border: 1px solid transparent;
  border-radius: var(--radius-control);
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
  transition:
    background-color 0.2s ease,
    border-color 0.2s ease,
    color 0.2s ease,
    transform 0.35s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.fmt-toggle:hover {
  background: var(--tone-bg);
  color: var(--tone-ink);
  border-color: color-mix(in oklab, var(--tone-ink) 25%, transparent);
}
/* 按压反馈：转轮拨动回弹（方案 7） */
.fmt-toggle:active {
  transform: rotate(180deg) scale(0.9);
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
   退档是为了不把卡片压到读不出数字，而不是为了塞下更多卡。
   断点走全站基准（见 theme.css「响应式断点」一节）：1200 四张每张
   只剩 ~270px、数字开始换行，退两列；768（平板竖屏）以下退一列。
   原来是 1100/700，与基准刻度对齐后各档行为不变、只差半档余量。 */
@media (max-width: 1200px) {
  .summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 768px) {
  .summary-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>

<!-- 弹层本体的样式：overlay-class-name 挂在 antd teleport 到 body 的那层上，
     不在本组件的模板里，scoped 样式碰不到它（与 VersionBadge 的 vb-popover
     同一处理）。内容自带头部与滚动结构，antd 默认内边距只会添乱。 -->
<style>
.theme-popover .ant-popover-inner {
  padding: 10px;
}

/* 三个筛选下拉的浮层边界（popup-class-name="filter-dropdown"）。
   浮层是 teleport 到 body 的，scoped 样式碰不到它，所以写在这个非 scoped 块里
   （与上面 .theme-popover 同一处理）。

   为什么需要它：模板里设了 dropdown-match-select-width=false —— 浮层不再
   跟随触发器宽度。触发器由 sizer 压到「刚好放下选中项」，浮层若也跟着那么
   窄，选项就会被截断成 "deepseek-…"（2026-10-27 实测踩到）。
   解耦之后浮层按自己的内容排版，但必须有边界：模型名可以很长（带版本号），
   一路撑到屏幕外同样不可用。 */
.filter-dropdown {
  min-width: 140px;
  max-width: 360px;
}

/* 浮层宽度有上限，比 360px 更长的模型名在这里省略成 …。
   完整值选不回来（下拉里与触发器上都省略了），这是 360px 上限的代价 ——
   真要放宽改上面的 max-width 即可。 */
.filter-dropdown .ant-select-item-option-content {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
