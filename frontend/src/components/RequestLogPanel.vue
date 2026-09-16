<script setup lang="ts">
// 请求日志面板 —— 2026-09-16 从 views/LogsView.vue 整块搬迁而来（该页面与数据看板合并）。
//
// 为什么是组件而不是继续当页面：并进看板之后，上面的概览卡与这里的列表必须共用
// 同一套筛选条件（时间范围 / 分组 / 渠道 / 模型）。那些条件由看板持有、以 props
// 传进来 —— 筛选状态只有一处，才不会出现「卡片按 A 算、列表按 B 查」这种
// 从界面上完全看不出来的偏差。这个组件只负责「按给定条件把列表画出来」。
//
// 页面外壳（内边距、工具栏、卡片排、分页以上的留白）都在看板那边；
// 这里输出的是一个面板：表格 + 分页，外加详情抽屉。
//
// 面板标题栏与「导出」按钮已经去掉（2026-09-16）：标题「请求日志」只说了一遍
// 表头下面那排列名已经说清的事，却占掉 40px 高度 —— 去掉之后表体多出一整行；
// 导出按钮一并撤掉（接口 /logs/export 仍在，脚本与后端测试照旧用它，
// 只是界面上不再开这个口子）。面板本身（背景 / 边框 / 圆角 / 内边距）
// 仍用 PanelCard，不传 title 时它不会渲染标题栏。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import PanelCard from '@/components/PanelCard.vue'
import GroupTag from '@/components/GroupTag.vue'
import ChannelIcon from '@/components/ChannelIcon.vue'
import { onLive } from '@/composables/useLive'
import { symbolOf } from '@/utils/money'
import type { Channel, ChannelGroup, Paged, RequestLog } from '@/api/types'

const props = defineProps<{
  /** 时间范围：today | 3d | 7d | 30d（后端与统计接口共用同一段代码换算成窗口） */
  range: string
  /** 'all' 或分组 id 的字符串形式（'all' 是本项目「全部」的哨兵值，与看板同一套） */
  groupId: string
  channelId: string
  /** 'all' 或模型名 */
  model: string
  /** 从 URL 带进来的排障深链（?trace_id=… / ?status_class=error），可在工具栏上关掉 */
  traceId: string
  statusClass: string
  /** 低频配置：标签配色与渠道图标取自它们，由看板取一次传下来 */
  groups: ChannelGroup[]
  channels: Channel[]
}>()

// 「只看这条链路」是详情抽屉里的入口，改的是看板持有的那个条件，所以只能往上抛
const emit = defineEmits<{
  (e: 'update:traceId', v: string): void
  (e: 'update:statusClass', v: string): void
}>()

// 分组表：日志里的模型、密钥、分组三处标签共用该请求所属分组的颜色。
//
// 为什么日志要按分组着色而不是按模型名（原来是后者）：
// 分组是用户自己配的边界（哪个密钥能走哪批渠道），日志里要一眼看出
// 「这条请求走的是哪个分组」；模型名着色对排障没有帮助，反而多一套颜色规则。
function groupOf(id: number) {
  return props.groups.find((g) => g.id === id)
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

// 分页是本面板自己的状态：它与筛选条件不是一回事 —— 改筛选要回第一页
// （见 search），而翻页不该惊动看板上方那些卡片
const page = ref(1)
const pageSize = ref(50)

// 表格体的高度上限：把视口减掉表头、分页与各处内边距，剩下的都给行。
//
// 这笔账在去掉面板标题栏之后重算过（2026-09-16 实测，逐项量的是 getBoundingClientRect）：
//   表体以上 205 = 内容区上内边距 8 + 工具栏面板 50 + 它的下边距 8
//                + 卡片排 83 + 它的下边距 8 + 面板上边框 1 + 面板上内边距 8
//                + 表头 39
//   表体以下 81  = 分页上外边距 16 + 分页 24 + 分页下外边距 16
//                + 面板下内边距 8 + 面板下边框 1 + 面板下边距 8
// 205 + 81 = 286 → calc(100vh - 286px)。（内容区自身还有 8px 下内边距，
// 它一并落在上面这个「100vh 之内」的预算里。）
// 原来是 326：面板标题栏（32px + 8px 下边距）占掉 40px，去掉之后这 40px
// 全给了行 —— 可视行数从 13 行回到 14 行。
//
// 为什么是 286 而不是 284：`.console-content` 是 overflow-y: auto，
// 内容只要比视口高一点点，就会出现「整页能滚 2px」的隐形滚动条。
// 实测：原来 324 时内容高 902.38（视口 900）→ 能滚 2px；326 时 900.38 → 滚不动。
// 卡片排的高度会随文案换行变（实测 83~98），所以这笔账本来就是近似值 ——
// 但余量要留在「不超过视口」这一侧：超出去是看得见的（滚动条），少几像素看不出来。
//
// 为什么锁死而不是让它按内容长：一页 50 行、每行约 40px，放开就是 2000px 的表格，
// 筛选栏与分页要滚很久才够得着；参考站也是这个做法（实测 .ant-table-fixed-header，
// 表体 1074px 内部滚动、表头固定）。不锁的话整个文档都在滚，左侧菜单还会被一起带走。
//
// 代价要说清楚：合并进看板之后概览卡与工具栏也占这 100vh 里的位置，
// 可视行数比独立的日志页少几行 —— 实测 1440x900 下完整可见 14 行
// （第 15 行露出大部分），原来的日志页是 17 行。换来的是「改筛选 → 看总数 →
// 翻明细」在同一屏里完成，不必来回滚动。
// 用 calc 而不是写死的像素：窗口高度不同、以后调整卡片或工具栏高度都不用跟着改。
const TABLE_BODY_Y = 'calc(100vh - 286px)'

// 日志本身只存了渠道名与渠道 id，图标得回渠道表里取。
// 取不到（渠道事后被删）时 ChannelIcon 会退回「首字母 + 按名字派生的底色」——
// 与渠道页、渠道下拉是同一套规则，同一条渠道在哪儿看都长一样。
function channelIconOf(id: number) {
  return props.channels.find((c) => c.id === id)?.icon ?? ''
}

// buildParams 拼列表的查询串。
//
// 时间范围传 range（四档关键字）而不是自己换算成的绝对时刻：后端拿它去调
// 与统计接口同一个 resolveRange，卡片与列表因此必然落在同一个窗口里。
// 原来这里按浏览器本地零点算 since，与看板的「今天」是两套算法，
// 跨时区访问时会出现「卡片说 200 次、列表 180 条」这种没人能解释的偏差。
function buildParams(): URLSearchParams {
  const params = new URLSearchParams()
  params.set('page', String(page.value))
  params.set('page_size', String(pageSize.value))
  // 三个下拉：'all' 就是不传（后端「不传参数＝不筛选」）
  if (props.groupId !== ALL) params.set('group_id', props.groupId)
  if (props.channelId !== ALL) params.set('channel_id', props.channelId)
  if (props.model !== ALL) params.set('model', props.model)
  // URL 带来的额外条件（深链过来的 trace / 状态）也要进查询串
  if (props.traceId) params.set('trace_id', props.traceId)
  if (props.statusClass) params.set('status_class', props.statusClass)
  if (props.range) params.set('range', props.range)
  return params
}

// 这里原来还有四组东西：三个下拉的候选（分组 / 渠道 / 模型）、筛选条件的持久化、
// 从 URL 恢复额外条件、以及四个 @change 处理函数。它们全部搬去了
// views/DashboardView.vue —— 那边的工具栏是这一页唯一的筛选入口，
// 筛选状态由它持有，改完通过 props 传下来（见下方 watch）。
// 留在这里就会出现两份状态：一份决定卡片怎么算，一份决定列表怎么查。

// 筛选条件变了就重新查，并且回到第一页。
//
// 回第一页是必须的：第 5 页的偏移量落在新条件的集合上可能已经越界，
// 表现出来是「改完筛选列表空着」，而数据其实是有的。
// watch 的是 props 本身，所以无论是谁改的（工具栏下拉、详情里的「只看这条链路」、
// 还是一个带 query 参数的链接）都会重新取数，不需要各处都记得手写一次 load()。
watch(
  () => [props.range, props.groupId, props.channelId, props.model, props.traceId, props.statusClass],
  () => search()
)

// 详情里的「只看这条链路」：原来工具栏上有个 trace_id 输入框，
// 但 trace_id 是从日志详情里才看得到的东西 —— 入口放在看得见它的地方更顺手。
// 条件由看板持有（它还要显示那个可关闭的小标签），所以这里往上抛。
function onlyThisTrace() {
  if (!current.value) return
  emit('update:traceId', current.value.trace_id)
  detailOpen.value = false
}

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为这段时间本来就没有调用
const loadError = ref('')

/**
 * 取当前筛选条件下的第一页。
 *
 * silent 用于实时推送触发的重取：不显示加载态（否则表格每隔一两秒就变暗一次）、
 * 失败不弹提示也不清空列表 —— 一次网络抖动不该把用户正在看的日志抹掉。
 *
 * 请求序号（loadSeq）只让**最后一次**请求的结果落地。这不是防御性代码，
 * 是实测出来的：筛选从「deepseek（5562 条）」切到「glm（0 条）」时，两个查询
 * 会并发在途，大的那个更慢，返回时把新结果盖掉 —— 界面成了「卡片 0、列表 5562」，
 * 而且它会一直错到下一次操作。有了序号，谁先谁后都不影响最终显示。
 */
let loadSeq = 0

async function load(opts: { silent?: boolean } = {}) {
  const silent = !!opts.silent
  const seq = ++loadSeq
  if (!silent) loading.value = true
  if (!silent) loadError.value = ''
  try {
    const res = await api.get<Paged<RequestLog>>('/logs?' + buildParams().toString())
    // 已经有更新的请求发出去了：这次的结果（以及它的错误、它的 loading）都作废
    if (seq !== loadSeq) return
    rows.value = res.items || []
    total.value = res.total || 0
  } catch (e: any) {
    if (silent || seq !== loadSeq) return
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    if (!silent && seq === loadSeq) loading.value = false
  }
}

function search() {
  page.value = 1
  load()
}

// 看板工具栏上的「刷新」要连列表一起刷（一页一个刷新按钮，
// 而不是「上面那个刷卡片、下面这个刷列表」两个都叫刷新）。
// 用 defineExpose 而不是再加一个 refreshToken prop：这里就是「叫它重取一次」，
// 传一个计数器反而让人以为它是个状态。
defineExpose({ reload: () => load() })

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

/** 「任务耗时」列里两行数值的悬停说明：先说这行是哪个数，再说这一档的判据 */
function durTitle(name: string, ms: number | null | undefined) {
  return name + '：' + latencyTitle(ms)
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

// 词元格的悬停说明：四个数挤在一格里，图标只能表达「这是哪一类」，
// 具体是多少、命中率怎么算出来的，都在这里说清（表头里原来那行括号
// 「（输入/输出/缓存）」就是干这个的，改版后挪到这里）。
// 缓存写入也列出来：命中率的分母是「未命中 + 命中 + 缓存写入」，
// 少了这一项，用户拿输入词元去除会算不拢。
function tokenTitle(row: RequestLog) {
  return (
    '输入 ' + fmtTokens(row.prompt_tokens) +
    ' · 输出 ' + fmtTokens(row.completion_tokens) +
    ' · 缓存命中 ' + fmtTokens(row.cached_tokens) +
    ' · 缓存写入 ' + fmtTokens(row.cache_creation_tokens) +
    ' · 命中率 ' + cacheRate(row)
  )
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

const pagination = computed(() => ({
  current: page.value,
  pageSize: pageSize.value,
  total: total.value,
  showSizeChanger: true,
  pageSizeOptions: ['20', '50', '100'],
  showTotal: (t: number) => '共 ' + t + ' 条',
  onChange: (p: number, ps: number) => {
    page.value = p
    pageSize.value = ps
    load()
  }
}))

// 实时插入：服务端每秒查一次新日志（id 增量），有就推过来。
//
// 分三种情况：
// 1. 没有任何筛选且在第一页 —— 直接插到第一行（保留滚动动画，不重绘整页）；
// 2. 有筛选（分组/渠道/模型，或 trace / 仅失败深链）且在第一页 —— 隔一小段
//    安静地重取一次当前查询。新日志符不符合筛选只有服务端知道，客户端不重复
//    实现一遍筛选语义（以后加一个筛选条件就会漏一处，而且错得很安静）；
// 3. 翻了页 —— 什么都不做：重取会让用户正在看的第二页变成另外一批行。
const liveReloadDelay = 1500
let liveTimer: number | null = null
let liveReloading = false

function scheduleSilentReload() {
  if (liveTimer !== null) return
  liveTimer = window.setTimeout(async () => {
    liveTimer = null
    // 上一次还没回来就跳过这一轮：慢查询堆起来只会让列表更晚更新
    if (liveReloading) return
    liveReloading = true
    try {
      await load({ silent: true })
    } finally {
      liveReloading = false
    }
  }, liveReloadDelay)
}

onUnmounted(() => {
  if (liveTimer !== null) window.clearTimeout(liveTimer)
})

onLive('logs', (items: RequestLog[]) => {
  if (!Array.isArray(items) || !items.length) return
  if (page.value !== 1) return
  const filtered =
    props.groupId !== ALL ||
    props.channelId !== ALL ||
    props.model !== ALL ||
    !!props.traceId ||
    !!props.statusClass
  if (filtered) {
    scheduleSilentReload()
    return
  }
  // 新日志的时间一定落在当前时间范围里（今天/近3天/近7天/近30天都含「现在」），
  // 所以这里不必再按时间过滤一次
  const fresh = items.filter((it) => !rows.value.some((r) => r.id === it.id))
  if (!fresh.length) return
  rows.value = [...fresh.reverse(), ...rows.value].slice(0, pageSize.value)
  total.value += fresh.length
})

onMounted(() => {
  // 分组 / 渠道的候选与渠道图标由看板取好传下来，这里只负责按条件取列表。
  // 首次挂载不经过 watch（它只在 props 变化时触发），所以这一次必须显式取。
  load()
})
</script>

<template>
  <!-- 面板外壳用全站统一的 PanelCard（背景 / 边框 / 圆角 / 内边距），
       但不传 title、也不传 extra：标题栏会说一遍表头已经说清的事，还占 40px；
       去掉之后表体刚好能多放一整行（TABLE_BODY_Y 因此从 326 收到 286）。 -->
  <PanelCard>
    <DataState
      :error="loadError"
      :has-data="rows.length > 0"
      :loading="loading"
      title="请求日志加载失败"
      @retry="load()"
    >
      <!-- 列宽按实测取值：scroll.x 必须装得进表体容器（1440 视口下是 1182），
           否则横向滚动时固定在右侧的「操作」列会把最后一列切掉 ——
           实测过一次：密钥胶囊被切掉小半个字。
           每列取「表头文字宽」与「本页内容最宽」的较大者 + 16px 内边距
           （2026-09-16 实测：渠道格 114、任务耗时格 104、费用格 82、
           密钥格 71、状态格 36），再留几像素余量。
           词元那一列 2026-09-16 改成上下两排后重新量过：表头只剩「词元」两个字
           （28px），不再撑宽列，列宽改由内容决定 —— 12px 字体下最宽的一排是
           「↓ 300.48K ↑ 32.76K」124px（近 7 天输入词元的最大值 304483），
           下排「▣ 479.23K 99.86%」107px，加 16px 内边距 = 140，取 150 留余量。
           改动列宽时这张表的总宽要一起看，scripts/check-table-widths.mjs
           会盯着声明值与各列宽度之和是否一致 -->
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="pagination"
        row-key="id"
        size="small"
        :scroll="{ x: 1036, y: TABLE_BODY_Y }"
      >
        <template #emptyText>
          <a-empty description="当前筛选条件下没有日志，可放宽筛选条件：把时间范围改成「近 7 天」，或把分组 / 渠道 / 模型改回「全部」" />
        </template>
        <a-table-column title="请求时间" :width="155" fixed="left">
          <template #default="{ record }">{{ fmtTime(record.created_at) }}</template>
        </a-table-column>
        <!-- 模型名带 ellipsis：不加的话长模型名会在这里折成两三行，
             把整行从 40px 顶到 98px（50 行就是 5000px 的页面）；
             完整名字悬停可见，详情里也有 -->
        <a-table-column title="模型" :width="155" ellipsis>
          <template #default="{ record }">
            <!-- 模型、密钥两处用的是同一个组件与同一个颜色：
                 它们描述的是「这次请求属于哪个分组」，颜色因此必须一致。
                 原来还有第三个「分组」列，后来删掉了：它写的就是这两个
                 胶囊颜色所指的那件事，却占着 110px；分组名在详情抽屉里，
                 要按分组看整批请求，工具栏上的分组筛选比这一列好用 -->
            <GroupTag :name="record.model_requested" v-bind="tagColorOf(record.group_id)" />
          </template>
        </a-table-column>
        <!-- 渠道列是随「按渠道筛选」一起加的：筛了渠道却在列表里看不出
             每行走的是哪条渠道，这个筛选等于只生效一半。
             名称前带渠道图标（与渠道页那张表同一个组件、同一套规则）：
             排障时要一眼认出「这条走的是哪条渠道」，而一屏里的渠道名
             往往只差几个字。
             失败请求没走到渠道（channel_id=0）、渠道事后被删都会是空值，显示 — -->
        <a-table-column title="渠道" :width="130" ellipsis>
          <template #default="{ record }">
            <span v-if="record.channel_name" class="chan-cell">
              <ChannelIcon :name="record.channel_name" :icon="channelIconOf(record.channel_id)" :size="18" />
              <!-- 名字带 title：多了图标之后这一格更挤，长渠道名会被省略号
                   截掉，截掉的部分要能悬停看到 -->
              <span class="chan-name" :title="record.channel_name">{{ record.channel_name }}</span>
            </span>
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <!-- 词元那一格：上下两排，上排「输入 / 输出」、下排「缓存 / 命中率」。
             四个数放一格，是因为它们回答的是同一个问题「这次调用用了多少词元」：
             原来是「词元（输入/输出/缓存）」+「缓存命中」两列（合计 260px），
             扫一行要在两处之间来回看。
             箭头朝向按语义定：↑ 是送到模型（输入）、↓ 是模型返回（输出）——
             与参考站截图里「↓ 在前、↑ 在后」的字形顺序是刻意相反的，
             这里以「向上=发给模型、向下=模型发回」这条更好懂的语义为准。
             箭头与缓存盒用内联 SVG 而不是 ↓ ↑ 字符：这两个字形既不是西文也不是
             中文，会落到字体链尾的系统符号字体，线宽与大小随机器变（本项目在字体
             上踩过这个坑，见 ui-spec 第 8 条）；自绘之后三个字形的线宽、圆角、
             跟随字色的方式都是同一套。
             颜色沿用 theme.css 那套语义色（输入陶土 / 输出紫 / 缓存绿），
             命中率与缓存同色 —— 它就是缓存那个数的比值。
             悬停给出一行汇总：图标只表达「这是哪一类词元」，具体数字看悬停。 -->
        <a-table-column title="词元" :width="150">
          <template #default="{ record }">
            <div class="token-cell" :title="tokenTitle(record)">
              <div class="tk-line">
                <span class="tk-pair tk-in">
                  <svg class="tk-ico" viewBox="0 0 12 12" aria-hidden="true">
                    <path d="M6 9.8V2.6M2.9 5.4 6 2.3l3.1 3.1" />
                  </svg>
                  <span class="tk">{{ fmtTokens(record.prompt_tokens) }}</span>
                </span>
                <span class="tk-pair tk-out">
                  <svg class="tk-ico" viewBox="0 0 12 12" aria-hidden="true">
                    <path d="M6 2.2V9.4M2.9 6.6 6 9.7l3.1-3.1" />
                  </svg>
                  <span class="tk">{{ fmtTokens(record.completion_tokens) }}</span>
                </span>
              </div>
              <div class="tk-line">
                <span class="tk-pair tk-cache">
                  <svg class="tk-ico" viewBox="0 0 12 12" aria-hidden="true">
                    <rect x="1.6" y="2.4" width="8.8" height="7.2" rx="1.6" />
                    <path d="M3.8 4.9h2.6" />
                  </svg>
                  <span class="tk">{{ fmtTokens(record.cached_tokens) }}</span>
                </span>
                <span class="tk tk-cache">{{ cacheRate(record) }}</span>
              </div>
            </div>
          </template>
        </a-table-column>
        <!-- 首字与总耗时合并成一列：它们回答的是同一个问题「这次调用等了多久」。
             分成两列时，扫列表要在两处之间来回看才能拼出一次调用的耗时，
             而这两列加起来 200px 换来的信息量只有两个数。
             单元格里按截图的样子竖排两行（左侧一条绿色竖条 + 首字 / 总耗时），
             数值仍按耗时分级着色，悬停会说明这一行是什么、以及这一档的判据。 -->
        <a-table-column title="任务耗时" :width="120">
          <template #default="{ record }">
            <div class="dur">
              <div class="dur-line">
                <span class="dur-label">首字</span>
                <span
                  class="dur-value"
                  :class="latencyClass(record.first_byte_ms)"
                  :title="durTitle('首字', record.first_byte_ms)"
                >{{ fmtMs(record.first_byte_ms) }}</span>
              </div>
              <div class="dur-line">
                <span class="dur-label">总耗时</span>
                <span
                  class="dur-value"
                  :class="latencyClass(record.total_ms)"
                  :title="durTitle('总耗时', record.total_ms)"
                >{{ fmtMs(record.total_ms) }}</span>
              </div>
            </div>
          </template>
        </a-table-column>
        <a-table-column title="费用" :width="90">
          <template #default="{ record }">{{ fmtCost(record.estimated_cost, record.cost_currency) }}</template>
        </a-table-column>
        <!-- 状态与密钥排在最后：列表自左向右读下来是
             「什么时候 → 哪个模型 → 哪条渠道 → 花了多少 → 结果如何」，
             一个「200」夹在模型和渠道中间会打断这条线。
             密钥特意跟着状态一起挪：它俩本来就是一问一答（哪把密钥、结果如何），
             拆开放到两处反而要来回找 -->
        <a-table-column title="状态" :width="64">
          <template #default="{ record }">
            <a-tag :color="statusColor(record.status_code)">{{ record.status_code }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="密钥" :width="100" ellipsis>
          <template #default="{ record }">
            <GroupTag v-if="record.api_key_name" :name="record.api_key_name" v-bind="tagColorOf(record.group_id)" />
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="72" fixed="right">
          <template #default="{ record }">
            <a-button type="link" size="small" @click="openDetail(record)">详情</a-button>
          </template>
        </a-table-column>
      </a-table>
    </DataState>

    <a-drawer v-model:open="detailOpen" title="调用详情" width="720">
      <a-descriptions v-if="current" :column="1" bordered size="small">
        <a-descriptions-item label="Trace ID">
          {{ current.trace_id }}
          <a-button type="link" size="small" class="trace-link" @click="onlyThisTrace">
            只看这条链路
          </a-button>
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
  </PanelCard>
</template>

<style scoped>
/* 「词元」那一格：上下两排、小一档字，上排输入/输出、下排缓存/命中率。
   两排 15px 合计 30px，再用 -4px 的上下负边距挤成 22px —— 与「任务耗时」列
   同一手法，行高因此与改版前完全一样（实测 40.25/41.25 两档）。
   行高一变，固定表体的可视行数与滚动位置都会跟着变（见 TABLE_BODY_Y 的说明）。

   必须是「块级 + justify-content: center」，不能用 inline-block：
   全局规则里单元格是 text-align: center（theme.css），块级盒子会撑满单元格、
   里面的两排从左边起排 —— 表现就是「整格数字偏左」，实测右空隙比左空隙大 42px。
   但也不能改成 inline-block：inline 级盒子要参与行盒，单元格那 14px 的行高
   （strut）会跟它叠起来，行高从 41px 涨到 49px，可视行数与滚动位置全变。
   块级 grid 加 justify-content: center 两头都顾上：轨道按内容收缩再整体居中，
   两排共用同一条左基线（较宽的那排决定轨道宽度），行高一点不动。 */
.token-cell {
  display: grid;
  justify-content: center;
  margin: -4px 0;
  font-size: 12px;
  line-height: 15px;
  font-variant-numeric: tabular-nums;
}
.tk-line { display: flex; align-items: center; gap: 8px; white-space: nowrap; }
/* 图标与它后面那个数是「一组」：组内 4px，两组之间 8px。
   这样「输入 300.48K ↑ 32.76K」读起来是两个词元种类，而不是四个数。 */
.tk-pair { display: inline-flex; align-items: center; gap: 4px; }
/* 三个字形（下箭头 / 上箭头 / 缓存盒）共用一条描边规则：
   线宽、圆角、颜色来源都一致，换字形时不必逐个调。
   颜色走 currentColor —— 挂色的地方是外面那一组（.tk-in/.tk-out/.tk-cache），
   图标与数字因此必然同色，不会出现「图标忘了上色」 */
.tk-ico {
  width: 12px;
  height: 12px;
  flex: none;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.5;
  stroke-linecap: round;
  stroke-linejoin: round;
}
/* 详情里的「只看这条链路」：贴着 trace_id 放，弱化成次要操作，
   别让人以为它是个必须点的按钮 */
.trace-link { margin-left: 8px; font-size: 12px; }

/* 模型、密钥、分组三列各是一个胶囊，用的是同一个组件（components/GroupTag.vue）
   与同一个颜色 —— 该请求所属分组的颜色。

   原来的分工是「模型按模型名着色、密钥用中性色」：那套规则在排障时没用，
   日志里真正要回答的是「这条请求走的是哪个分组」。三者同色之后，
   扫一眼就能按颜色把同一分组的请求归到一起，也不必再记住两套配色规则。
   颜色不是唯一线索：三列里都写着名字。 */
/* 词元三段各自的颜色（变量定义见 theme.css，深色主题自动换档）。
   挂在「图标 + 数值」这一组上：图标靠 currentColor 继承，数字直接继承，
   一组两处必然同色 */
.tk-in { color: var(--token-input); }
.tk-out { color: var(--token-output); }
.tk-cache { color: var(--token-cache); }

/* 耗时分级 */
.lat-fast { color: var(--latency-fast); }
.lat-mid { color: var(--latency-mid); }
.lat-slow { color: var(--latency-slow); font-weight: 500; }
.lat-none { color: var(--color-text-secondary); }

/* 「任务耗时」列：两行数值共用左侧一条绿色竖条。
   竖条是装饰（信息全在文字里），所以用填充色 --color-green；
   数值是正文，走上面那一组 --text-* 分级色（对比度依据见 theme.css）。

   字体比正文小一档、行高压到 15px：两行合计 30px，只比一行正文（22px）高一点。
   再用 -4px 的上下负边距把这两行挤进单元格的 8px 内边距里，
   行高因此与合并前完全一样（实测都是 40.6px，没有被这两行顶高）——
   行高一变，固定表体的可视行数与滚动位置都会跟着变（见 TABLE_BODY_Y 的说明）。

   justify-content: center 而不是 inline-grid：单元格是 text-align: center
   （theme.css 的全局规则），块级 grid 会撑满单元格、内容从左边起排，看着就是
   「这一列没居中」（实测右空隙比左空隙大 27px）；而 inline-grid 会参与行盒，
   把行高从 41px 顶到 49px。块级 grid 居中轨道，行高与居中同时保住。 */
.dur {
  display: grid;
  justify-content: center;
  grid-template-columns: 4px auto;
  column-gap: 6px;
  align-items: center;
  margin: -4px 0;
  font-size: 12px;
  line-height: 15px;
  font-variant-numeric: tabular-nums;
}
/* 竖条跨两行，高度由内容决定：写死高度会在字号或行高变化时对不上 */
.dur::before {
  content: '';
  grid-column: 1;
  grid-row: 1 / span 2;
  align-self: stretch;
  border-radius: 2px;
  background: var(--color-green);
}
.dur-line {
  grid-column: 2;
  display: flex;
  gap: 6px;
  /* 两行是这个单元格的全部：数值再长也不许折到第三行 ——
     折了行高就从 39px 涨上去，整张表的可视行数与滚动位置都会变。
     极端长的耗时（如 36000.00s，10 小时）宁可溢出到相邻单元格的内边距上，
     也不改变行高。实测库里最大值是 466.50s，离列宽还差得远。 */
  white-space: nowrap;
}
/* 标签固定宽度：两行的数值因此从同一个位置起排，扫一列数时不会左右跳 */
.dur-label {
  flex: none;
  width: 36px;
  color: var(--color-text-secondary);
}

/* 渠道那一格：图标 + 名字。
   与「词元」「任务耗时」同理用 justify-content: center 居中：单元格是
   text-align: center，而块级 flex 会撑满单元格、图标从最左边起排
   （实测右空隙比左空隙大 14px）。名字比列宽长时，chan-name 自己会被压缩
   （min-width: 0 + overflow: hidden），所以这里的居中不会把内容顶出去。
   chan-name 要 min-width: 0 + 省略号：flex 子项默认不肯缩到内容宽度以下，
   长渠道名会顶破单元格，而不是像其它列那样出现「…」。 */
.chan-cell { display: flex; justify-content: center; align-items: center; gap: 6px; min-width: 0; }
.chan-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 空值占位符（渠道 / 密钥没有值时显示 —）。
   其它四个视图都有这条，只有这里漏了 —— 漏掉的表现是占位符用了正文色，
   比旁边的真实值还显眼，而它本该是「这里什么都没有」 */
.muted { color: var(--color-text-secondary); }

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
  /* 错误详情是正文，用 --text-red（白底 5.44:1）而不是 --color-red（3.90:1） */
  color: var(--text-red);
}
</style>
