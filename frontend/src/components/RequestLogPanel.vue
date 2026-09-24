<script setup lang="ts">
// 请求日志面板 —— 2026-09-16 从 views/LogsView.vue 整块搬迁而来（该页面与数据看板合并）。
//
// 为什么是组件而不是继续当页面：并进看板之后外壳（内边距、工具栏、卡片排）留在
// 看板那边，这里只输出「表格 + 分页 + 详情抽屉」这一块。
//
// 列表**不吃**工具栏的筛选条件（2026-09-16 站主要求）：它恒定显示全部最新请求，
// 不按时间范围 / 分组 / 渠道 / 模型取数。并进看板时这两者本来共用一套条件，
// 但列表的用处是「盯着最新发生了什么」，筛过之后反而看不到刚进来的请求 ——
// 而刚进来的请求恰恰是最该被看到的那几条。筛选留给上面的概览卡：
// 「这一部分用了多少」才是那些条件真正要回答的问题。
//
// 仍能约束列表的只有两个排障深链：?trace_id=… 与 ?status_class=error（「仅失败」）。
// 它们由详情抽屉或外链带进来，是明确的排障动作，不是日常筛选视角。
//
// 页面外壳（内边距、工具栏、卡片排、分页以上的留白）都在看板那边；
// 这里输出的是一个面板：表格 + 分页，外加详情抽屉。
//
// 面板标题栏与「导出」按钮已经去掉（2026-09-16）：标题「请求日志」只说了一遍
// 表头下面那排列名已经说清的事，却占掉 40px 高度 —— 去掉之后表体多出一整行；
// 导出按钮一并撤掉（接口 /logs/export 仍在，脚本与后端测试照旧用它，
// 只是界面上不再开这个口子）。面板本身（背景 / 边框 / 圆角 / 内边距）
// 仍用 PanelCard，不传 title 时它不会渲染标题栏。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { CopyOutlined, ProfileOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import PanelCard from '@/components/PanelCard.vue'
import GroupTag from '@/components/GroupTag.vue'
import ChannelIcon from '@/components/ChannelIcon.vue'
import { onLive, createThrottledLiveReloader, liveConnected } from '@/composables/useLive'
import NewLogEffect, { type FxTarget } from '@/components/NewLogEffect.vue'
import { useLogFxStore } from '@/stores/logFx'
import { costText, symbolOf } from '@/utils/money'
import { writeClipboard } from '@/utils/clipboard'
import { fmtTime, fmtTimeCompact, pad2 } from '@/utils/fmtTime'
import { readStoredChoice, writeStoredChoice } from '@/utils/persistedChoice'
import type { Channel, ChannelGroup, Paged, RequestLog } from '@/api/types'

const props = defineProps<{
  /** 从 URL 带进来的排障深链（?trace_id=… / ?status_class=error），可在工具栏上关掉。
   *  这是列表仅有的两个条件 —— 时间范围 / 分组 / 渠道 / 模型不再传给列表。 */
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

// ---- 列宽随内容自适应 ----
//
// 站主 2026-09-20 先要求「模型列按当前页最宽的内容调」，2026-09-23 推广到
// **每一列**：每列宽度由该列实际显示的内容决定，至少要把内容显示完整。
//
// 定宽的问题：原来那组常量是当初按「最坏情况」人工拍的（每个数的实测出处见
// 模板上方那段注释），数据一换长度就截断 —— 站里模型名都短（glm-5.3 一类）时
// 多出大半空白，换一批更长的渠道名/密钥名又重新出现截断。所以改成**量了再定**。
//
// 为什么「声明宽度 ≥ 内容所需宽度」就能保证内容完整：表格是
// table-layout: fixed，声明宽就是列宽的下界；而声明总宽小于容器宽时 antd 还会
// 把余量**按比例分给各列**（ui-spec 第 10 条有实测：声明 155/155/130…
// 渲染成 200/200/167…）。所以只要声明宽够，内容就不会被 ellipsis 截断 ——
// 这正是本机制要保证的那条不变量。
//
// 量的对象有硬性要求：必须是**不被列宽压缩**的元素，scrollWidth 才等于
// 「装下全部内容需要的宽度」；对普通块级元素它等于 clientWidth，量不到内容。
// 又因为不能把内容块改成 inline-block / inline-flex —— ui-spec 第 10 条实测过，
// inline 级原子盒会把单元格行高从 41px 顶到 49px —— 块级 flex/grid 的列一律量
// **内部已 nowrap 的子元素**，再把固定前缀（图标、竖条、gap）加回去。
const COL_KEYS = [
  'time', 'model', 'channel', 'tokens', 'elapsed', 'cost', 'speed', 'status', 'key', 'action'
] as const
type ColKey = (typeof COL_KEYS)[number]

/**
 * 每列的 [下限, 上限]。
 *
 * 下限 =「再窄就放不下表头、或被压得不像话」的值，也就是原来那组定宽常量。
 * 上限是**软上限**：超出部分交给 ellipsis + 悬停 title。不设上限的话，一个
 * 60 字符的内部模型代号能把整行撑到看不见别的列，而那种值在真实数据里几乎
 * 不存在 —— 为它牺牲整张表的可读性不划算。
 */
const COL_BOUNDS: Record<ColKey, [number, number]> = {
  // 时间列现在按内容量（见 fmtTimeCompact 的说明：今天只显示时分秒）。
  // 下限 92 = 「昨天 23:59:59」这类最长形态在 12px 下的宽度 + 内边距，
  // 上限 170 留给跨年的完整日期
  time: [92, 170],
  model: [120, 300],
  channel: [130, 240],
  tokens: [140, 200],
  elapsed: [120, 150],
  cost: [90, 140],
  speed: [118, 150],
  status: [64, 64], // 三位状态码 + a-tag，宽度固定
  key: [100, 200],
  action: [64, 64] // 一个 28px 图标按钮，宽度固定
}

/** 运行时列宽。初值取原来那组定宽常量（模型列的 190 是它的常规值），
 *  首屏先按它们渲染、测量结果随后覆盖 —— 避免「先窄后宽」闪一下。 */
const colW = ref<Record<ColKey, number>>({
  time: 96,
  model: 190,
  channel: 130,
  tokens: 150,
  elapsed: 120,
  cost: 90,
  speed: 118,
  status: 64,
  key: 100,
  action: 64
})

/**
 * 每列的内容锚点与固定前缀宽（px）。
 *
 * 没列出的（time / status / action）不参与测量：它们的内容宽度恒定，下限即
 * 所需宽度。pad 是内容之外必须一起算进去的部分：渠道的图标与间距、
 * 耗时的竖条与间距。
 *
 * 锚点的两条硬性要求：
 *  1. 选择器在表体里**只能匹配这一列**。测量是全局查询
 *     （`wrap.querySelectorAll('.ant-table-tbody ' + sel)`），跨列命中就会把
 *     别的列的内容算进本列。
 *  2. 元素要「不被压缩」：块级 flex/grid 量的是自己的盒子，行内级（inline-flex /
 *     inline-block）才是紧贴内容的 shrink-to-fit。
 *
 * 第 1 条是 2026-09-23 的实测教训：密钥列原来写的是 '.group-tag'，而模型名与
 * 密钥名都用 GroupTag 渲染 —— 模型列那枚更宽的胶囊（deepseek-v4.1-flash ≈ 123px）
 * 也被算了进来，密钥列因此常年 145px，而那一页的内容只需要 71px（多出 58px 空白）。
 * 现在密钥列用只属于它的 .key-tag。
 */
const CONTENT_MEASURE: Partial<Record<ColKey, { sel: string; pad: number }>> = {
  model: { sel: '.model-cell', pad: 0 }, // inline-flex + nowrap
  channel: { sel: '.chan-name', pad: 24 }, // 18 图标 + 6 间距
  // 词元的锚点是整格（两排共用一个网格，量它就是量最宽的那一排）。
  // 2026-09-24 之前锚点是 .tk-line，那时它是 display:flex 的定宽轨道 ——
  // 量到的是轨道（恒 128px）而不是内容，列宽因此锁死在初值；
  // 现在 .tk-line 是 display:contents（只为一排共用轨道），
  // 根本量不到盒子，必须换成 .token-cell（width: fit-content，紧贴内容）
  tokens: { sel: '.token-cell', pad: 0 },
  elapsed: { sel: '.dur-line', pad: 9 }, // 3 竖条 + 6 间距
  cost: { sel: '.txt-cell', pad: 0 },
  speed: { sel: '.spd-cell', pad: 0 }, // inline-flex + nowrap
  key: { sel: '.key-tag', pad: 0 } // inline-block + ellipsis，溢出量真实
}

/** 各列宽之和。模板里每列的 :width 与表格 scroll.x 都取这里 ——
 *  宽度只有这一份定义。此前模板写一遍、求和再抄一遍，漏改的表现是表格出现
 *  非预期的横向滚动，而且看不出是哪一列错。 */
const columnsTotal = computed(() => COL_KEYS.reduce((sum, k) => sum + colW.value[k], 0))

/** 单元格左右内边距（antd 小表格 8+8）与右侧呼吸余量（给省略号与边框） */
const CELL_PAD = 16
const CELL_BREATH = 6

/**
 * 按当前页内容重算各列宽度。
 *
 * 一次把所有锚点读完再统一写回：读一次写一次会让浏览器反复重排 ——
 * 100 行 × 7 列这个量级下，两种写法的差别是肉眼可见的卡顿。
 */
function remeasureColumns() {
  const wrap = tableWrap.value
  if (!wrap) return
  // 空表不量：加载中 / 筛选无结果时量不到内容，会把列宽收到下限白闪一下
  if (!wrap.querySelector('.ant-table-tbody tr[data-row-key]')) return

  const next: Partial<Record<ColKey, number>> = {}
  for (const key of COL_KEYS) {
    const spec = CONTENT_MEASURE[key]
    if (!spec) continue
    let max = 0
    wrap.querySelectorAll<HTMLElement>(`.ant-table-tbody ${spec.sel}`).forEach((el) => {
      // getBoundingClientRect 而不是 scrollWidth。两者对**块级/行内块**锚点
      // 是同一个数（实测 .model-cell 172.2 对 172、.key-tag 72.8 对 71，
      // 差的是亚像素舍入），但对 **display: inline** 的锚点只有前者有效：
      // inline 元素不生成盒子，scrollWidth / clientWidth 恒为 0。
      //
      // 费用列的锚点 .txt-cell 必须是 inline（inline-block 会参与行盒、
      // 把单元格行高从 44px 顶到 49px，见那里的注释），于是 2026-09-24
      // 之前的 `const w = el.scrollWidth` 在这列上永远读到 0，
      // 走进下面的 `max === 0 → continue` 分支 —— 费用列因此**从未**随内容
      // 变过宽，一直停在初值 90px。这是本次 UI 审评实测发现的：
      // 该列金额最长 72.3px，加内边距与呼吸位需要 94px，90px 是差一点的，
      // 而列宽自适应看起来「生效了」，所以一直没被发现。
      //
      // 亚像素：rect.width 带小数（72.3），取整后再进下面的 ≥2px 抖动判断
      const w = Math.ceil(el.getBoundingClientRect().width)
      if (w > max) max = w
    })
    if (max === 0) continue // 本页该列没有锚点（如全是空值），保持原宽
    const [lo, hi] = COL_BOUNDS[key]
    next[key] = Math.min(hi, Math.max(lo, max + spec.pad + CELL_PAD + CELL_BREATH))
  }

  let changed = false
  for (const key of COL_KEYS) {
    const want = next[key]
    if (want === undefined) continue
    // 只在 ≥2px 时写：1px 的抖动不值得让整张表重排一次
    // （列宽一抖，所有行的扫光位置都要跟着重算）
    if (Math.abs(want - colW.value[key]) >= 2) {
      colW.value[key] = want
      changed = true
    }
  }
  // 列宽变了 → 行宽也变了 → 正在飞的扫光亮带要重新定位
  if (changed) {
    nextTick(repositionBeams)
    // 列宽一变，「右边还有没有内容」也跟着变（列变窄可能就不再需要滚动）
    nextTick(syncScrollHints)
  }
}

// 列宽在 rows 变化后重算：翻页 / 换筛选 / 首屏的整批替换，以及实时推送插行
// （后者也走这里，阈值负责把抖动挡在外面）
watch(rows, () => nextTick(remeasureColumns))

// ---- 新日志的扫光（那条彩虹只为「刚插进来的行」而闪）----
//
// 存的是「正在做入场动画的行 id」，命中就给这一行加 is-new（一个纯标记的类，
// 不带任何样式，见文件末尾为什么不能给它挂样式）。
// 用 Set 而不是给行数据加字段：行数据来自接口，往里塞 UI 字段会跟着进详情抽屉、
// 进导出、进任何复制它的地方；id 集合只活在这一次动画里。
const freshIds = ref(new Set<number>())

// 2s 动画 + 0.4s 余量。到点把 id 与那条亮带一起撤掉。
const FRESH_MS = 2400
// 只留「还没到期的计时器」：到点后从集合里自摘。面板是常驻组件，
// 每条新日志都会 markFresh 一次，数组只增不减的话一个月能攒上万个
// 死 id（无害但不体面）。
const freshTimers = new Set<number>()

/**
 * 正在做入场动画的那些行的位置（三档动效共用这一份）。
 *
 * 为什么是「独立的一层」而不是画在 tr 的 ::after 上（第一版就是那么写的，
 * 在 1440 视口下一切正常，直到有人在更宽的窗口里看见右侧空出一片）：
 * tr 里一旦出现非单元格子元素（::after 就是一个），Chrome 在
 * `table-layout: fixed` 下就不再把它多出来的宽度分给各列 —— 整张表会从
 * 「铺满容器」的宽度塌回声明宽度（实测 1600 视口：每格 200/200/167/… →
 * 155/155/130/…，行右边界 1575 → 1277），而表头是另一张表、照旧铺满，
 * 于是右边空出一条，直到动画结束才恢复。隔离验证过：去掉伪元素、只留
 * position: relative，列宽全程正常；只把 position 改成 static、留着伪元素，
 * 照样塌 —— 起因就是那个伪元素。挂到单元格上也不行：固定列是 sticky，
 * 它里面的绝对定位伪元素会被放到行外（实测盒子 x 148→1482，而行是 241→1575）。
 *
 * 所以改成在表格外面套一层自己控制的容器，量出新行的位置再交给
 * components/NewLogEffect.vue 去画：表格内部 DOM 一个字节都不动，
 * 列宽、固定列、滚动都不受影响。
 */
const fxTargets = ref<FxTarget[]>([])
const tableWrap = ref<HTMLElement | null>(null)

/** 亮带厚度：分割线是单元格的 1px 下边框（separate 布局下算在行高内），盖住它再往上压 2px */
const BEAM_H = 3

/** 按 id 找回那一行，重新量位置。滚动或改窗口大小后要再调一次 */
function repositionBeams() {
  const wrap = tableWrap.value
  if (!wrap) return
  const base = wrap.getBoundingClientRect()
  fxTargets.value = fxTargets.value.map((b) => {
    const tr = wrap.querySelector(`tr[data-row-key="${b.key}"]`)
    if (!tr) return b
    const r = tr.getBoundingClientRect()
    return { ...b, top: r.bottom - base.top - BEAM_H, left: r.left - base.left, width: r.width }
  })
}

// 亮带飞行的那 2 秒里，用户可能滚动列表（新行被顶上去）或改窗口大小：
// 位置得跟着重算，否则亮带会停在旧位置 —— 扫光只有两秒，但「停错地方」比不闪更糟。
// scroll 用 capture：滚动事件不冒泡，而真正滚的是表格内部的 .ant-table-body。
let beamWatchAttached = false
function attachBeamWatch() {
  if (beamWatchAttached) return
  beamWatchAttached = true
  window.addEventListener('scroll', repositionBeams, true)
  window.addEventListener('resize', repositionBeams)
}
function detachBeamWatch() {
  if (!beamWatchAttached) return
  beamWatchAttached = false
  window.removeEventListener('scroll', repositionBeams, true)
  window.removeEventListener('resize', repositionBeams)
}
watch(
  fxTargets,
  (v) => {
    if (v.length) attachBeamWatch()
    else detachBeamWatch()
  },
  { deep: true },
)

// ---- 固定列的边界提示：只在真的藏了内容时才画 ----
//
// 两枚固定列（左侧「请求时间」、右侧「操作」）都盖在别的列上面。
// 横向滚动时被盖住的可能正好是数字 —— 1055 视口下「词元」列只剩
// `↓ 2.38K` 和 `22K 99.59%`（真实是 `↑ 131.22K ↓ 2.38K`），
// 用户读到的是一个看起来小一个数量级的残缺值，而屏幕上没有任何提示。
//
// 为什么不用 antd 自带的固定列阴影：它有这套机制（`.ant-table-ping-left/right`
// 挂在表根上，`.ant-table-cell-fix-left-last::after` 上加 inset box-shadow），
// 但**实测在这张表上不工作** —— 1055 视口滚到中段、末尾，表根的类名里
// 始终只有 `ping-right`、`::after` 的 boxShadow 恒为 `none`。
// 所以这里自己画一条渐隐，并且**自己判断什么时候该显示**。
//
// 判断依据不是「能不能滚」（那会让 1440 这种本来就不滚的宽度上也常亮一条
// 无意义的阴影 —— 本文件第一版就是这么错的），而是分别看两侧：
//   canLeft  = 已经向右滚了（左边有内容被盖住）
//   canRight = 还没滚到底（右边还有内容）
// 两侧各自独立，滚到最左时左侧不画、滚到最右时右侧不画。
const canScrollLeft = ref(false)
const canScrollRight = ref(false)

function syncScrollHints() {
  const body = tableWrap.value?.querySelector<HTMLElement>('.ant-table-body')
  if (!body) {
    canScrollLeft.value = false
    canScrollRight.value = false
    return
  }
  // 1px 容差：缩放比例非整数时 scrollLeft 会有零点几像素的余量，
  // 严格比较会让「已经滚到底」被判成「还能再滚」，右侧阴影因此不消失
  canScrollLeft.value = body.scrollLeft > 1
  canScrollRight.value = body.scrollLeft < body.scrollWidth - body.clientWidth - 1
}

// ---- 新日志入场动效：档位来自系统设置，立刻生效（store 是同一个实例）----
//
// 三档的机制与各自踩过的坑都写在 components/NewLogEffect.vue 里，这里只管一件事：
// 把量好的行位置传下去。
//
// 动效档位变了不需要重取数据，也不需要重挂表格：换的只是特效层怎么画（靠
// .log-table 上的 data-fx，见模板）。飞行中的位置数据不清 —— 切档时那条亮带/光晕
// 会按新档的样式接着画完，这比"切一下就全没了"更自然。
const logFx = useLogFxStore()

/**
 * 标记这些行「刚新增」，让它们做一次入场动画。
 *
 * 只有实时推送触发的两种更新会调它（见 onLive 与 load 的 silent 分支）：
 * 筛选变化、翻页、点刷新、重试都是用户主动重取，整屏都在换，闪一排彩虹没有信息量；
 * 而「凭空多出来一行」才需要提示，否则用户根本不会注意到列表变了。
 *
 * loadedOnce 是一道必须在的闸：首屏那次列表请求还没回来时，实时推送可能先到了，
 * 那一帧里的行会「先被当成新增」（此时 rows 还是空的，无从比对），接着首屏响应
 * 又把这些行整批画出来 —— 结果是刚打开/刚刷新页面就扫一次。实测在刷新页面的
 * 4.5 秒窗口里抓到了这一下：那期间恰好有一条真实请求入库。语义上也说得通
 * （它确实是新日志），但表现为「刷新就闪」，与「只有新来的才闪」这条约定不符，
 * 所以首屏落地之前一律不标。
 */
let loadedOnce = false

function markFresh(ids: number[]) {
  if (!loadedOnce) return
  const fresh = ids.filter((id) => !freshIds.value.has(id))
  if (!fresh.length) return
  for (const id of fresh) freshIds.value.add(id)
  // 效果层要等这一行渲染出来才量得到位置（扫光的亮带与光晕的光带用的是
  // 同一次测量结果）
  nextTick(() => {
    const wrap = tableWrap.value
    if (!wrap) return
    const base = wrap.getBoundingClientRect()
    for (const id of fresh) {
      const tr = wrap.querySelector(`tr[data-row-key="${id}"]`)
      if (!tr) continue
      const r = tr.getBoundingClientRect()
      fxTargets.value.push({ key: id, top: r.bottom - base.top - BEAM_H, left: r.left - base.left, width: r.width })
    }
    // 新行插入会把所有正在飞行中的老行整体顶下去（两次日志间隔 1 秒左右时
    // 必现）：老亮带没有跟着行走，就留在旧行的位置上 —— 正好是第二条新行
    // 的位置，与第二条的亮带叠在一起，看起来就是「两次动画播在了同一行」。
    // 按行 key 把全部飞行中的 target 重新量一遍，各归各行。
    // （repositionBeams 原本只挂在 scroll/resize 上，行数变化不触发它 ——
    //   这正是这条 bug 只在连发时出现的原因。）
    repositionBeams()
  })
  const timer = window.setTimeout(() => {
    freshTimers.delete(timer)
    const gone = new Set(fresh)
    for (const id of fresh) freshIds.value.delete(id)
    fxTargets.value = fxTargets.value.filter((b) => !gone.has(b.key))
  }, FRESH_MS)
  freshTimers.add(timer)
}

// ---- 「思考」列：入站思考参数的归一档位 → 色点颜色 ----
//
// 档位由后端 relay.ExtractThinkingLevel 归一（openai 的 reasoning_effort、
// GLM 的 thinking.type、anthropic 的 budget_tokens 粗分档、gemini 的
// thinkingConfig，四家归到同一套词表）。展示**不翻译**（站主要求统一英文）：
// 档位词本身就是各家协议词汇，翻译成中文反而和导出 CSV、详情抽屉对不上。
// 空 = 请求没带思考参数 —— 它不是「off」：一个是「没说」，一个是
// 「说了不要」，必须可区分。认不得的档位原样展示：诚实优于猜测。
//
// 胶囊形态与模型/密钥的 GroupTag 同款（描边胶囊、无动效 —— 2026-09-18
// 站主点名撤掉之前的整套分层动效），档位之间只靠颜色区分：
//   off/minimal 灰（关掉的、没说的）  low 青   medium 琥珀   high 品红
//   on 绿   auto 蓝   xhigh 暗琥珀   max 血红（effort 的最高档）
// 文字色的对比度处理（淡底上原色多数不达 4.5:1）在 .think-pill 的 CSS 里
// 做 oklab 混黑/混白，这里只管「哪个档位是哪个颜色」。
const THINKING_COLORS: Record<string, string> = {
  off: '#8c8c8c',
  minimal: '#bfbfbf',
  low: '#36cfc9',
  medium: '#faad14',
  high: '#eb2f96',
  on: '#52c41a',
  auto: '#1677ff',
  // xhigh / max 是 anthropic 4.6+ output_config.effort 的两档
  // （Claude Code 发的就是 max）。max 曾经只由客户端自定义档位用上。
  xhigh: '#d48806',
  // max 2026-09-23 由琥珀改成血红（站主要求）：它原本与 medium 同为 #faad14，
  // 两枚胶囊在列表里根本分不出来，而它恰恰是最高档、最该一眼认出的那个。
  // 取纯红 #a10000 而不是更暗的 #8b0000（darkred）：这一族的颜色还要当 13% 的
  // 淡底与 32% 的描边用，太暗的基色做出来的底发灰、看不出红调。
  // 与项目里另外两个红也拉得开 —— --color-red #ea4343、--text-red #c0392b
  // 都是偏砖的珊瑚红，这个是纯红；与 high 的品红 #eb2f96 差在色相上。
  max: '#a10000',
}

function thinkingColor(level?: string) {
  if (!level) return ''
  return THINKING_COLORS[level] ?? '#8c8c8c'
}

// 兜底胶囊的颜色。用紫色：它不在思考档位的九色里（那九色各自已有明确语义），
// 也不与「未定价」的橙色系撞车 —— 橙色在全站的含义是「这里缺东西、待处理」，
// 而兜底是用户主动配好的一项能力，不是待办。
// 与 .think-pill 共用胶囊形态，于是「模型列里第三个小胶囊」在扫列表时
// 能被认出是同一类状态标记，而不是模型名的一部分。
const FALLBACK_COLOR = '#722ed1'

/** 行的 class 由「是否刚新增」决定；antd 在它自己的渲染里调用它，读到的依赖归它 */
function rowClassName(record: RequestLog) {
  return freshIds.value.has(record.id) ? 'is-new' : ''
}

// 分页是本面板自己的状态：翻页不该惊动看板上方那些卡片
// （改深链条件要回第一页，见 search）
const page = ref(1)
// 每页条数默认 20，且记住用户的选择 —— 一页看 20 条还是 100 条是阅读
// 习惯，每次刷新都回 20 是重复劳动。读的时候只认下面的合法选项，
// 旧值/脏数据一律回落到默认（容错说明见 persistedChoice）。
const PAGE_SIZE_KEY = 'log-page-size'
const PAGE_SIZE_OPTIONS = ['20', '50', '100']
const pageSize = ref(Number(readStoredChoice(PAGE_SIZE_KEY, PAGE_SIZE_OPTIONS, '20')))

// 表体的高度上限，交给 antd 的 scroll.y。
//
// 它现在只剩一个作用：**让 antd 渲染出「表头 / 表体」分离的固定表头结构**
// （有没有 scroll.y 决定它走哪套布局），同时作为表体的 max-height。
//
// 高度本身不再在这里算。原来是 `calc(100vh - 286px)` —— 把工具栏 50、卡片排 83、
// 表头 39、分页 56、各处内外边距逐项加起来，再从视口里减掉：205 + 81 = 286。
// 这笔账记了六七项，其中卡片排的高度还会随文案换行在 83~98 之间变（实测），
// 于是常量与现实差一点，面板底边就差一点。站主 2026-09-17 反馈的
// 「主页左侧栏与右侧容器底部不在一条水平线上」，实测正是面板底边比左侧栏高 7.6px
// （那 8px 是面板自己的下外边距，它被算进了预算里、却又落在面板底边之下）。
//
// 现在改由弹性分配决定：内容区 → 看板 → 这块面板 → antd 那几层 div → 表体，
// 表体拿到的就是「面板里除表头与分页之外剩下的高度」（见样式里那一段）。
// 与逐项相减相比，它不依赖任何一项的具体高度：卡片换行变高、工具栏多一句提示、
// 顶上多出一条默认密钥告警，表体都自己让位。
//
// 传 100% 而不是某个像素：作为 max-height 它相对表格容器算，容器高度确定时
// 不会成为新的限制（实测各视口下表体都是「填满」，没有一处被它截断）；
// 万一弹性链断了，它退化成「按内容全长」—— 50 行 2062px，整页立刻出现大滚动条，
// 一眼就能看出来。这比一个悄悄差 8px、要拿尺子量才发现的魔法数字好。
//
// 为什么仍然锁死而不是让它按内容长：一页几十行（20/50/100 可选）、每行约 40px，
// 放开就是上千像素，筛选栏与分页要滚很久才够得着；参考站也是这个做法
// （实测 .ant-table-fixed-header，表体内部滚动、表头固定）。
// 不锁的话整个文档都在滚，左侧菜单还会被一起带走。
const TABLE_BODY_Y = '100%'

// 日志本身只存了渠道名与渠道 id，图标得回渠道表里取。
// 取不到（渠道事后被删）时 ChannelIcon 会退回「首字母 + 按名字派生的底色」——
// 与渠道页、渠道下拉是同一套规则，同一条渠道在哪儿看都长一样。
function channelIconOf(id: number) {
  return props.channels.find((c) => c.id === id)?.icon ?? ''
}

// buildParams 拼列表的查询串。
//
// 只有分页与那两个排障深链：时间范围 / 分组 / 渠道 / 模型一律不带 ——
// 列表恒定取全量最新（理由见文件头）。
// 这里原来还要传 range（四档关键字，后端拿它调与统计接口同一个 resolveRange，
// 以保证卡片与列表落在同一个窗口里）。列表不再跟着卡片走之后，
// 那段「两套算法会跨时区对不上」的顾虑也就一起消失了。
function buildParams(): URLSearchParams {
  const params = new URLSearchParams()
  params.set('page', String(page.value))
  params.set('page_size', String(pageSize.value))
  // URL 带来的额外条件（深链过来的 trace / 状态）也要进查询串
  if (props.traceId) params.set('trace_id', props.traceId)
  if (props.statusClass) params.set('status_class', props.statusClass)
  return params
}

// 这里原来还有四组东西：三个下拉的候选（分组 / 渠道 / 模型）、筛选条件的持久化、
// 从 URL 恢复额外条件、以及四个 @change 处理函数。它们全部搬去了
// views/DashboardView.vue —— 那边的工具栏现在只服务概览卡，列表不再吃它
// （原来两边共用一套条件，留在这里就会出现两份状态）。

// 深链条件变了就重新查，并且回到第一页。
//
// 回第一页是必须的：第 5 页的偏移量落在新条件的集合上可能已经越界，
// 表现出来是「改完条件列表空着」，而数据其实是有的。
// watch 的是 props 本身，所以无论是谁改的（详情里的「只看这条链路」、
// 工具栏上关掉那个小标签、还是一个带 query 参数的链接）都会重新取数，
// 不需要各处都记得手写一次 load()。
// 只盯这两个：工具栏那四个筛选已经不传进来了，它们不再影响列表。
watch(() => [props.traceId, props.statusClass], () => search())

// 详情里的「只看这条链路」：原来工具栏上有个 trace_id 输入框，
// 但 trace_id 是从日志详情里才看得到的东西 —— 入口放在看得见它的地方更顺手。
// 条件由看板持有（它还要显示那个可关闭的小标签），所以这里往上抛。
function onlyThisTrace() {
  if (!current.value) return
  emit('update:traceId', current.value.trace_id)
  detailOpen.value = false
}

// 「仅失败」切换（2026-09-24 UI 审评 P1-8）：面板一直支持 status_class=error，
// 但此前唯一的入口是手改 URL —— 而失败排查恰恰是这类工具的第一需求。
// 条件与深链、看板工具栏那个小标签共享同一个 ref（由看板持有），
// 所以这里只往上抛；watch 在 props 上，状态一变列表自动重取并回第一页。
// 只做 error 一档：success 深链仍可从 URL 进来（看板 applyUrlFilters 认它），
// 但不值得为「只看成功」做一个常驻开关 —— 排障找的是坏的，不是好的。
function toggleFailOnly() {
  emit('update:statusClass', props.statusClass === 'error' ? '' : 'error')
}

// ---- 实时连接状态（2026-09-24 UI 审评 P1-10）----
//
// useLive.ts 早就导出了 liveConnected，但全仓 0 引用：WebSocket 断了，界面照旧
// 显示旧数据、数字不再跳动，而用户分不清这是「没有流量」还是「连接死了」——
// 「实时」二字因此不可信。这里把它接出来，与「最后更新」时间戳一起显示：
//
//   链路正常 → 绿点 +「实时 · 最后更新 10:13:23」
//   断开重连 → 红点 +「已断开，正在重连 · 最后更新 10:13:23」
//
// 时间戳只在**数据或推送真的到达**时刷新（load 成功、收到 logs 帧），不是每秒
// 走的表：它要回答的正是「我看到的这屏有多旧」，所以静默时段停住不动才是对的。
// 断线时它同时说明了两件事 —— 界面上的数据停在哪个时刻，以及为什么不再动。
//
// everConnected 是为了首屏：订阅刚建立、握手还没完成的那几百毫秒里
// liveConnected 仍是 false，直接显示「已断开」会闪一下假警报。
const everConnected = ref(false)
watch(liveConnected, (v) => {
  if (v) everConnected.value = true
})
const lastLiveAt = ref('')
function touchLive() {
  lastLiveAt.value = fmtTimeCompact(new Date().toISOString())
}

// 复制 Trace ID：排障时它要被贴进日志搜索、聊天工具或上游工单，
// 24 位十六进制手动划选又慢又容易断行漏字符。
// 降级路径与密钥复制共用 utils/clipboard.ts（http 非 localhost 环境照常可用）。
async function copyTraceId() {
  if (!current.value) return
  if (await writeClipboard(current.value.trace_id)) message.success('已复制 Trace ID')
  else message.warning('复制失败，请手动选择复制')
}

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为这段时间本来就没有调用
const loadError = ref('')

/**
 * 取最新一页（第一页就是最新的那批）。
 *
 * silent 用于实时推送触发的重取：不显示加载态（否则表格每隔一两秒就变暗一次）、
 * 失败不弹提示也不清空列表 —— 一次网络抖动不该把用户正在看的日志抹掉。
 *
 * 请求序号（loadSeq）只让**最后一次**请求的结果落地。这不是防御性代码，
 * 是实测出来的：从前筛选从「deepseek（5562 条）」切到「glm（0 条）」时，两个查询
 * 会并发在途，大的那个更慢，返回时把新结果盖掉 —— 界面成了「卡片 0、列表 5562」，
 * 而且它会一直错到下一次操作。有了序号，谁先谁后都不影响最终显示。
 * （列表不再吃筛选之后这种并发少了一路，但翻页与深链切换仍会并发。）
 *
 * silent 这一路还会做一件事：比对重取前后多出来的是哪几行，交给 markFresh 扫光。
 * 带深链时新日志符不符合条件只有服务端知道（这里不重复实现筛选语义），
 * 所以只能整批重取；「哪几行是新的」用 id 差集算，不靠位置猜。
 */
let loadSeq = 0

async function load(opts: { silent?: boolean } = {}) {
  const silent = !!opts.silent
  const seq = ++loadSeq
  if (!silent) loading.value = true
  if (!silent) loadError.value = ''
  // 用户主动重取会把整屏内容换掉：这时候还没飞完的亮带下面已经不是原来那一行了，
  // 得赶紧收掉（否则它会停在某个「碰巧在这个位置」的行上闪完剩下的时间）。
  // silent 那条路不能清：那正是要标出新行的路径。
  if (!silent) {
    freshIds.value.clear()
    fxTargets.value = []
  }
  // silent 是实时推送独有的路径（useLive.createThrottledLiveReloader 是唯一调用点），
  // 所以「多出来的行」必然是刚入库的那几条，不会是筛选切换带来的整屏替换
  const before = silent ? new Set(rows.value.map((r) => r.id)) : null
  try {
    const res = await api.get<Paged<RequestLog>>('/logs?' + buildParams().toString())
    // 已经有更新的请求发出去了：这次的结果（以及它的错误、它的 loading）都作废
    if (seq !== loadSeq) return
    rows.value = res.items || []
    total.value = res.total || 0
    // 数据真的到了才刷新「最后更新」（P1-10）：它是「这屏有多旧」的判据，
    // 不能因为一次失败的静默重取而跳到当前时间
    touchLive()
    // 首屏落地：从这一刻起，实时推送标出来的行才真的是「新来的」
    if (!silent) loadedOnce = true
    if (before) markFresh(rows.value.filter((r) => !before.has(r.id)).map((r) => r.id))
  } catch (e: any) {
    if (silent || seq !== loadSeq) return
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    // 熄灯的责任跟着「谁点的灯」走，不跟着「谁最新」走（第三轮审查 F-中2）：
    // 非静默的这次请求可能已经被随后的静默重取顶掉（seq 不再最新），数据
    // 该作废，但 loading 是它点亮的 —— 它要是也放手，就没有人再熄这盏灯，
    // 按钮会一直转下去（antd 的 loading 按钮还会把点击吃掉，刷新都点不动）。
    // 静默那条路则完全不碰 loading：它本来就不该让屏幕闪骨架。
    if (!silent) loading.value = false
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

// 空列表的文案分两种：带深链时是被那两个条件滤空的（要告诉用户怎么退回去），
// 不带条件时就是真的一条日志都没有。原来只有前一种，因为列表总在筛选之下；
// 现在「全量最新」是默认视角，一进来就空着的情况必须说清是「还没有调用」，
// 否则看起来像坏了。
const emptyText = computed(() =>
  props.traceId || props.statusClass
    ? '当前排障条件下没有日志：关掉工具栏上的「链路」或「仅失败」标签即可回到全部最新请求'
    : '还没有请求日志；发起一次调用后，这里会实时出现记录',
)

// 详情直接用列表行数据：列表接口返回的字段已经完整，
// 报文相关展示移除后，也不必再为它请求 /logs/:id。
function openDetail(row: RequestLog) {
  current.value = row
  detailOpen.value = true
}

// 耗时分级（2026-09-16 站主重定阈值，两行各用一套）：
//   首字：≤10s 绿、10-30s 橙、>30s 红
//   耗时：≤20s 绿、20-60s 橙、>60s 红
// 首字更严，因为它才是「用户感觉卡不卡」的那一下；总耗时把上游生成的时间也算进去，
// 长回答本来就要几十秒，用同一把尺子会把正常请求染红。
//
// 边界取 <=10000 / <=30000 这种「闭区间、下一档从 10001 起」的写法，
// 不在两档之间留缝：中间的毫秒必须落进某一档，否则会出现「不着色」的空档。
//
// fmtMs 对 0 与空值都返回 '-'，那种情况不着色，避免把「没有数据」显示成「很快」。
function latencyClass(ms: number | null | undefined, kind: 'first' | 'total' = 'total') {
  if (!ms) return 'lat-none'
  const [fast, mid] = kind === 'first' ? [10000, 30000] : [20000, 60000]
  if (ms <= fast) return 'lat-fast'
  if (ms <= mid) return 'lat-mid'
  return 'lat-slow'
}

/** 悬停说明这一档的判据：颜色本身不该是唯一的信息来源 */
function latencyTitle(ms: number | null | undefined, kind: 'first' | 'total' = 'total') {
  if (!ms) return '没有记录到耗时'
  if (kind === 'first') {
    if (ms <= 10000) return '10 秒内'
    if (ms <= 30000) return '10-30 秒'
    return '超过 30 秒'
  }
  if (ms <= 20000) return '20 秒内'
  if (ms <= 60000) return '20-60 秒'
  return '超过 60 秒'
}

/** 「任务耗时」列里两行数值的悬停说明：先说这行是哪个数，再说这一档的判据 */
function durTitle(name: string, ms: number | null | undefined, kind: 'first' | 'total' = 'total') {
  return name + '：' + latencyTitle(ms, kind)
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

// 输出速度（词元/秒）。分母按输出方式分两套口径：
//   - 流式：首字之后才是生成时段，用「总耗时 − 首字」；
//   - 非流式：响应头与完整正文一起到达（后端非流式的 first_byte_ms 记的
//     是响应头到达时刻，那时生成已经完成），生成时长只能用总耗时近似。
// 口径不同的分叉必须让用户看得见 —— 列表里流式行带「流」胶囊就是这里
// 的可视标记，悬停说明写明用的是哪个分母。
// 返回空串表示算不出（没有输出词元、没量到耗时、流式缺首字），单元格显示 —，
// 不猜数 —— 估出来的速度比没有速度更误导。
function tokPerSec(row: RequestLog): string {
  const out = row.completion_tokens
  if (!out || out <= 0 || !row.total_ms) return ''
  const ms = row.stream ? row.total_ms - (row.first_byte_ms || 0) : row.total_ms
  if (ms <= 0) return ''
  const v = out / (ms / 1000)
  // 一律取整（站主 2026-09-21 要求）：速度只是个量级参考，小数位是假精度，
  // 取整后同列位数也天然一致
  return v.toFixed(0)
}

// 速度的悬停说明：把分子分母摊开，速成的数怎么来的一眼可查。
// 分母带「（流式）/（非流式）」后缀 —— 两种口径的差别就藏在这个词里。
function speedTitle(row: RequestLog) {
  const ms = row.stream ? row.total_ms - (row.first_byte_ms || 0) : row.total_ms
  const denom = row.stream ? '首字后生成 ' + fmtMs(ms) : '总耗时 ' + fmtMs(ms)
  return '输出 ' + fmtTokens(row.completion_tokens) + ' 词元 ÷ ' + denom
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

/* 耗时文本。秒**一律补齐两位小数**（8.70s / 14.00s，而不是 8.7s / 14.0s）：
   列表是竖着扫的，位数一致时小数点在同一列上，扫一列数字不用重新找基准。
   不足 1 秒仍按毫秒显示（170ms）—— 抖成「0.17s」不如毫秒直观，
   而且库里最小的耗时是 2ms，两位小数会把它抹成「0.00s」，
   等于把「很快」显示成「没有耗时」。 */
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
// 不做任何换算 —— 人民币渠道的钱就是人民币。
//
// 小数位交给 utils/money.ts 的 costText：它按量级分档（<0.01 六位、否则四位），
// 并且是全站唯一一份规则。这里原来是自己的 toFixed(6)，而同一笔钱在详情里
// 是 toFixed(8)、在分组预算是 toFixed(2)、在日报是 toFixed(4) ——
// 用户核对「这一单到底多少钱」时四个地方对不上（2026-09-24 UI 审评）。
function fmtCost(v: string, currency?: string) {
  return costText(v, currency)
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
  pageSizeOptions: PAGE_SIZE_OPTIONS,
  showTotal: (t: number) => '共 ' + t + ' 条',
  onChange: (p: number, ps: number) => {
    page.value = p
    pageSize.value = ps
    // 选了新的每页条数就记住，下次打开页面直接用它
    writeStoredChoice(PAGE_SIZE_KEY, String(ps))
    load()
  }
}))

// 实时插入：服务端每秒查一次新日志（id 增量），有就推过来。
//
// 分三种情况：
// 1. 没有深链条件且在第一页 —— 直接插到第一行（保留滚动动画，不重绘整页）。
//    列表不带筛选之后这是常态路径：推到什么就插什么，连「符不符合条件」都不用问；
// 2. 带 trace / 仅失败深链且在第一页 —— 立刻（受 1 秒节流）安静地重取一次当前查询。
//    新日志符不符合条件只有服务端知道，客户端不重复实现一遍筛选语义
//    （以后加一个条件就会漏一处，而且错得很安静）。
//    原来这里也是 setTimeout(1500)：与看板卡片那份同款的人为延迟，重取回来
//    新增行才做入场动画 —— 数字/行的变动因此落在动画结束之后（2026-09-18
//    站主二次反馈的同一根因，见 useLive.createThrottledLiveReloader 的说明）；
// 3. 翻了页 —— 什么都不做：重取会让用户正在看的第二页变成另外一批行。
const liveReloader = createThrottledLiveReloader(() => load({ silent: true }))

// ---- 日志队列水位（服务端 health 推送）----
//
// 看板上的所有数字都出自日志这条异步写入队列；队列满时服务端会静默丢日志
// （只有后端日志里一条 warn），统计因此「看起来正常」地少算。
// 服务端把丢弃计数与队列水位做成 health 帧推过来：平时低调显示水位，
// 一旦真的丢了，这里必须第一个喊出来 —— 在你怀疑「数字怎么对不上」之前。
type LogQueueInfo = { queued: number; capacity: number; dropped: number }
const queueInfo = ref<LogQueueInfo | null>(null)
let lastDroppedAlert = 0

onLive('health', (data: LogQueueInfo) => {
  // 与 logs 帧同理：health 帧是服务端统计循环的固定节拍，
  // 它到了就说明这条链路还在（P1-10 的「最后更新」用它兜底：没有日志流量时
  // 时间戳仍会跳，于是「链路活着」与「没有新请求」两件事分得开）
  touchLive()
  const prev = queueInfo.value
  queueInfo.value = data
  // 丢弃新增：弹一条 error。持续丢弃时不刷屏 —— 同一场告警 10 秒内只弹一条，
  // 但状态条保持红色，肉眼不会漏
  if (prev && data.dropped > prev.dropped && Date.now() - lastDroppedAlert > 10000) {
    lastDroppedAlert = Date.now()
    message.error(`日志队列已满：新增 ${data.dropped - prev.dropped} 条统计被丢弃，看板数字暂时不完整`)
  }
})

onUnmounted(() => {
  liveReloader.dispose()
  // 入场动效的定时器也要清：它们回调里会写 freshIds，
  // 卸载后再写是在动一个已经不在屏幕上的组件的状态
  for (const t of freshTimers) window.clearTimeout(t)
  freshTimers.clear()
  fxTargets.value = []
  detachBeamWatch()
  // 横向滚动边界提示的两个监听（滚动走捕获、改窗口）
  window.removeEventListener('scroll', syncScrollHints, true)
  window.removeEventListener('resize', syncScrollHints)
})

onLive('logs', (items: RequestLog[]) => {
  // 收到任何一帧都说明链路还活着（空帧也算）——「最后更新」因此是
  // 「最后收到实时推送的时刻」，断线时它停住的那一刻正是界面开始失效的时刻
  touchLive()
  if (!Array.isArray(items) || !items.length) return
  if (page.value !== 1) return
  const filtered = !!props.traceId || !!props.statusClass
  if (filtered) {
    liveReloader.request()
    return
  }
  // 列表不带时间范围，新日志必然属于「全部最新」，直接插即可
  const fresh = items.filter((it) => !rows.value.some((r) => r.id === it.id))
  if (!fresh.length) return
  rows.value = [...fresh.reverse(), ...rows.value].slice(0, pageSize.value)
  total.value += fresh.length
  // 刚插到最上面的这几行做一次入场动画（用户正在看第一页，新行就在眼前）
  markFresh(fresh.map((r) => r.id))
})

onMounted(() => {
  // 分组表与渠道图标由看板取好传下来，这里只负责取列表。
  // 首次挂载不经过 watch（它只在 props 变化时触发），所以这一次必须显式取。
  load()
  // 列表字体（Harding-Regular.ttf）就绪前，量到的是回退字体的宽度 ——
  // 两者字宽不同，按回退字体算出的列宽会偏窄。字体到了要重量一次。
  document.fonts?.ready.then(() => nextTick(remeasureColumns))

  // 横向滚动的边界提示。列宽是量出来的、行数还会随实时推送变，
  // 所以「能不能往右滚」在每个可能改变它的时机都要重算：
  // 列宽重算后（remeasureColumns 里调）、改窗口（resize）、滚动（scroll）。
  //
  // scroll 用 capture 挂在 window 上就够了，**不需要**再去
  // .ant-table-body 上单独挂一个：scroll 事件虽然不冒泡，但**捕获阶段
  // 会经过所有祖先** —— window 上的捕获监听因此收得到表体发出来的滚动。
  // 本文件上面的扫光亮带（repositionBeams）用的也是同一条，早已验证可行。
  //
  // 这里原本还写了一个「轮询 20 次 × 100ms 等 .ant-table-body 渲染出来再挂监听」
  // 的兜底 —— 那是多余的：表体是 antd 挂载后才渲染的，首帧确实拿不到它，
  // 但 window 捕获监听不依赖拿到它。而轮询版有个真实风险：表体若在 2 秒后才
  // 出现（后端慢、首屏加载失败重试），监听就永远挂不上，阴影从此不再更新。
  // 删掉。
  window.addEventListener('scroll', syncScrollHints, true)
  window.addEventListener('resize', syncScrollHints)
  nextTick(syncScrollHints)
})
</script>

<template>
  <!-- 面板外壳用全站统一的 PanelCard（背景 / 边框 / 圆角 / 内边距），
       但不传 title、也不传 extra：标题栏会说一遍表头已经说清的事，还占 40px；
       去掉之后表体刚好能多放一整行。 -->
  <PanelCard>
    <!-- 列表自己的小工具条。上面看板的工具栏只服务概览卡（列表不吃那四个筛选），
         而「仅失败」是**列表自己的**条件 —— 入口长在列表旁边，看着列表点它，
         眼睛不用跑（2026-09-24 UI 审评 P1-8：此前唯一入口是手改 URL 深链）。
         这一行刻意做薄（26px）：面板标题栏当年就是为省 40px 被拿掉的，
         这里不能再吃回去。左边留给将来的实时状态（P1-10），现在先空着。 -->
    <div class="list-bar">
      <!-- 实时状态（P1-10）：链路是否活着 + 界面数据最后刷新的时刻。
           放左侧是因为它是这一屏所有数字的前提 —— 先可信，再读数。 -->
      <div
        class="live-status"
        :class="{ off: everConnected && !liveConnected }"
        role="status"
        aria-live="polite"
        :title="
          liveConnected
            ? '实时推送链路正常；这个时间随服务端推送刷新，断线时它会停住'
            : '与后端的实时连接已断开，正在自动重连；下面的数据停在上面的时刻'
        "
      >
        <span class="live-dot" aria-hidden="true"></span>
        <template v-if="!everConnected">连接中…</template>
        <template v-else>
          {{ liveConnected ? '实时' : '已断开，正在重连' }}
          <span v-if="lastLiveAt" class="live-at">· 最后更新 {{ lastLiveAt }}</span>
        </template>
      </div>
      <button
        type="button"
        class="fail-toggle"
        :class="{ on: statusClass === 'error' }"
        :aria-pressed="statusClass === 'error'"
        title="只显示失败的请求；再次点击恢复全量"
        @click="toggleFailOnly()"
      >
        <span class="fail-dot" aria-hidden="true"></span>
        仅失败
      </button>
    </div>
    <DataState
      :error="loadError"
      :has-data="rows.length > 0"
      :loading="loading"
      title="请求日志加载失败"
      @retry="load()"
    >
      <!-- 列宽全部由脚本按当前页内容量出来（见 remeasureColumns 与 COL_BOUNDS）：
           每列取「下限」与「本页内容所需宽度」的较大者，上限是软上限 ——
           超出部分交给 ellipsis + 悬停 title。
           下限就是原来那组人工实测的定宽值（实测出处：渠道格 114、
           任务耗时格 104、费用格 82、密钥格 71、状态格 36，2026-09-16 量于
           12px 列表字体），它们同时是「表头文字 + 内边距」的下界，
           所以列再窄也挤不到表头。
           词元那一列 2026-09-16 改成上下两排后重新量过：表头只剩「词元」两个字
           （28px），列宽改由内容决定 —— 12px 字体下最宽的一排是
           「↓ 300.48K ↑ 32.76K」124px（近 7 天输入词元的最大值 304483），
           下排「▣ 479.23K 99.86%」107px。
           操作列 72 -> 64 是「详情」文字链接改成图标按钮那一次
           （见模板里那一列上方的注释）。
           思考等级 2026-09-18 并入模型列（独立 80px 列删除）：胶囊贴在模型名
           后面，「用什么模型、什么强度思考」一行读完。
           2026-09-20 模型列先改成按内容自适应；2026-09-23 推广到每一列
           （站主要求「每列至少把内容显示完整」），scroll.x 改为各列宽之和。
           横向滚动仍可能发生（内容比容器宽时），固定列的遮挡情况由
           scripts/check-log-columns.mjs 盯着。 -->
      <!-- 外面这层只为扫光存在：亮带是这一层里的绝对定位元素，表格内部
           一个字节都不动（原因见脚本里 fxTargets 的注释 —— 往 tr 里加伪元素会让
           列宽塌回声明宽度）。overflow: hidden 是兜底：亮带永远不该撑出滚动条。
           data-fx 是当前档位：动效层里那几条纯 CSS 的规则靠 .log-table[data-fx='x']
           选中 tr.is-new 的单元格（自上滑入与光晕脉动两档），换档时不用重挂表格。 -->
      <div
        ref="tableWrap"
        class="log-table"
        :data-fx="logFx.fx"
        :data-hint-l="canScrollLeft ? '1' : '0'"
        :data-hint-r="canScrollRight ? '1' : '0'"
      >
        <NewLogEffect :mode="logFx.fx" :targets="fxTargets" />
        <a-table
          :data-source="rows"
          :loading="loading"
          :pagination="pagination"
          :row-class-name="rowClassName"
          row-key="id"
          size="small"
          :scroll="{ x: columnsTotal, y: TABLE_BODY_Y }"
        >
        <template #emptyText>
          <a-empty :description="emptyText" />
        </template>
        <!-- 请求时间：这一列昨天还占 155px（「2026-09-24 11:01:22」19 个字符），
             是横向滚动的最大单一贡献者，而「今天」这一档下 50 行的日期部分
             逐行完全相同。现在按 fmtTimeCompact 分档显示（今天 = 时分秒），
             完整时间挂在 title 上。省下来的 ~60px 直接来自这一列。 -->
        <a-table-column title="请求时间" :width="colW.time" fixed="left">
          <template #default="{ record }">
            <span :title="fmtTime(record.created_at)">{{ fmtTimeCompact(record.created_at) }}</span>
          </template>
        </a-table-column>
        <!-- 模型名带 ellipsis：不加的话长模型名会在这里折成两三行，
             把整行从 40px 顶到 98px（50 行就是 5000px 的页面）；
             完整名字悬停可见，详情里也有 -->
        <a-table-column title="模型" :width="colW.model" ellipsis>
          <template #default="{ record }">
            <!-- 模型、密钥两处用的是同一个组件与同一个颜色：
                 它们描述的是「这次请求属于哪个分组」，颜色因此必须一致。
                 原来还有第三个「分组」列，后来删掉了：它写的就是这两个
                 胶囊颜色所指的那件事，却占着 110px；分组名在详情抽屉里。
                 （当初还有一条理由「按分组看整批请求时，工具栏的分组筛选更好用」，
                 它随列表不再吃筛选而失效 —— 现在列表只回答「最新发生了什么」。） -->
            <span class="model-cell">
              <GroupTag :name="record.model_requested" v-bind="tagColorOf(record.group_id)" />
              <!-- 思考胶囊：档位色由 CSS 变量 --pill-color 注入（一套底/字/边框
                   规则覆盖全部档位），分层样式与动效见 .think-pill 的注释。
                   没带思考参数的请求不渲染胶囊 —— 「没有」不需要占位。 -->
              <span
                v-if="record.thinking_level"
                class="think-pill"
                :style="{ '--pill-color': thinkingColor(record.thinking_level) }"
              >{{ record.thinking_level }}</span>
              <!-- 兜底胶囊：这条请求的模型名没命中白名单，是渠道的默认模型映射
                   接下的（真正发给上游的是 model_upstream）。
                   必须显眼 —— 开了兜底之后客户端写错模型名也不再报错，
                   这个标记是发现「其实没命中」的唯一途径。
                   颜色借用思考档位里最低调的那一档同款（灰），不抢主信息，
                   但形状与位置让它在扫列表时能被一眼扫到。 -->
              <span
                v-if="record.fallback_mapped"
                class="think-pill"
                :style="{ '--pill-color': FALLBACK_COLOR }"
                :title="'模型名没命中白名单，已改用 ' + (record.model_upstream || '默认模型') + ' 请求上游'"
              >兜底</span>
            </span>
          </template>
        </a-table-column>
        <!-- 渠道列当初是随「按渠道筛选」一起加的，那个筛选现在不再作用于列表；
             这一列留着是因为它本身就回答「这条走的是哪条渠道」——
             排障时正是要看这个，而且一屏里的渠道名往往只差几个字。
             名称前带渠道图标（与渠道页那张表同一个组件、同一套规则）。
             失败请求没走到渠道（channel_id=0）、渠道事后被删都会是空值，显示 — -->
        <a-table-column title="渠道" :width="colW.channel" ellipsis>
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
        <a-table-column title="词元" :width="colW.tokens">
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
                <span class="tk tk-cache tk-rate">{{ cacheRate(record) }}</span>
              </div>
            </div>
          </template>
        </a-table-column>
        <!-- 首字与总耗时合并成一列：它们回答的是同一个问题「这次调用等了多久」。
             分成两列时，扫列表要在两处之间来回看才能拼出一次调用的耗时，
             而这两列加起来 200px 换来的信息量只有两个数。
             单元格里按截图的样式竖排两行（左侧竖条 + 首字 / 耗时）：竖条**断成两段**，
             上段跟首字、下段跟耗时，各自按自己那一行的档位上色 ——
             一整条单色只能表达「这次调用慢」，断成两段才看得出是首字慢还是生成慢。
             标签只留两个字（截图就是这样）：`总耗时` 三个字在 36px 的标签轨里
             会把数值列推远，而这一格的宽度是按最窄列倒推出来的，一寸都不富余。
             数值按同一档位着色，悬停说明这一行是什么、以及这一档的判据。 -->
        <a-table-column title="任务耗时" :width="colW.elapsed">
          <template #default="{ record }">
            <div class="dur">
              <!-- 一条竖条、两段。两段各挂自己那一行的档位类，
                   段色由 .dur-bar i 的 currentColor 继承而来 -->
              <span class="dur-bar">
                <i :class="latencyClass(record.first_byte_ms, 'first')" />
                <i :class="latencyClass(record.total_ms, 'total')" />
              </span>
              <div class="dur-line">
                <span class="dur-label">首字</span>
                <span
                  class="dur-value"
                  :class="latencyClass(record.first_byte_ms, 'first')"
                  :title="durTitle('首字', record.first_byte_ms, 'first')"
                >{{ fmtMs(record.first_byte_ms) }}</span>
              </div>
              <div class="dur-line">
                <span class="dur-label">耗时</span>
                <span
                  class="dur-value"
                  :class="latencyClass(record.total_ms, 'total')"
                  :title="durTitle('耗时', record.total_ms, 'total')"
                >{{ fmtMs(record.total_ms) }}</span>
              </div>
            </div>
          </template>
        </a-table-column>
        <a-table-column title="费用" :width="colW.cost">
          <template #default="{ record }">
            <!-- 包一层 .txt-cell：它是这一列的测量锚点（见 CONTENT_MEASURE）。
                 必须是普通 inline —— inline-block 会改变单元格行高
                 （ui-spec 第 10 条实测 41px → 49px），而 inline 盒子的
                 rect 宽度恰好就是文本自身的宽度。 -->
            <span class="txt-cell">{{ fmtCost(record.estimated_cost, record.cost_currency) }}</span>
          </template>
        </a-table-column>
        <!-- 速度列：每秒词元输出速度。流式请求在数值前带「流」胶囊（同一行）——
             输出速度必须结合输出方式才读得懂：流式的分母是首字之后的生成时段，
             非流式只能用总耗时近似（口径见 tokPerSec 注释），胶囊就是那个分叉的
             可视标记。失败请求通常没有输出词元，显示 — -->
        <a-table-column title="速度" :width="colW.speed">
          <template #default="{ record }">
            <span v-if="tokPerSec(record)" class="spd-cell" :title="speedTitle(record)">
              <span v-if="record.stream" class="stream-pill">流</span>
              <span class="spd">{{ tokPerSec(record) }} tok/s</span>
            </span>
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <!-- 状态列移到费用之后（站主 2026-09-20 要求）：数字区（词元/耗时/费用）
             读完后，「成没成」与「哪把密钥」两个结果性信息收尾 -->
        <a-table-column title="状态" :width="colW.status">
          <template #default="{ record }">
            <a-tag :color="statusColor(record.status_code)">{{ record.status_code }}</a-tag>
          </template>
        </a-table-column>
        <!-- 密钥跟着状态收尾：它们本来就是一问一答（哪把密钥、结果如何） -->
        <a-table-column title="密钥" :width="colW.key" ellipsis>
          <template #default="{ record }">
            <GroupTag
              v-if="record.api_key_name"
              class="key-tag"
              :name="record.api_key_name"
              v-bind="tagColorOf(record.group_id)"
            />
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <!-- 操作列与其它列表页同一个写法：28px 图标按钮（.table-icon-btn）。
             这一列只有一个「详情」，原来是一行文字链接，占 72px 里的大半；
             换成图标后列收到 64px，表格总宽跟着从 1036 降到 1028。
             图标按钮没有可见文字，tooltip 与 aria-label 是它的动作名 ——
             少了这两样，读屏用户只会听到一个没有名字的按钮。 -->
        <a-table-column title="操作" :width="colW.action" fixed="right">
          <template #default="{ record }">
            <a-tooltip title="调用详情：报文、错误原文与链路">
              <a-button
                class="table-icon-btn"
                type="text"
                size="small"
                aria-label="查看这次调用的详情"
                @click="openDetail(record)"
              >
                <ProfileOutlined />
              </a-button>
            </a-tooltip>
          </template>
        </a-table-column>
        </a-table>
      </div>

      <!-- 日志队列水位：服务端每次统计醒来时把丢弃计数与队列占用量推过来
           （见 live.go 的 health 帧）。放在表格之下、面板最底部 ——
           它是这一屏的「仪表地基」状态，不是每天要看的内容；
           丢弃发生过就保持红色，悬停可见累计数与含义 -->
      <div v-if="queueInfo" class="queue-status" :class="{ 'queue-alert': queueInfo.dropped > 0 }">
        <span
          class="queue-dot"
          :title="
            queueInfo.dropped > 0
              ? '已累计丢弃 ' + queueInfo.dropped + ' 条日志（队列满或服务退出），看板统计因此偏小'
              : '日志写入队列健康，无丢弃'
          "
        />
        日志队列 {{ queueInfo.queued }}/{{ queueInfo.capacity }}
        <span v-if="queueInfo.dropped > 0" class="queue-dropped">已丢 {{ queueInfo.dropped }}</span>
      </div>
    </DataState>

    <a-drawer v-model:open="detailOpen" title="调用详情" :width="'min(720px, 94vw)'">
      <a-descriptions v-if="current" :column="1" bordered size="small">
        <a-descriptions-item label="Trace ID">
          {{ current.trace_id }}
          <a-button type="link" size="small" class="trace-link" @click="copyTraceId">
            <CopyOutlined /> 复制
          </a-button>
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
          <!-- 两个模型名不一样时必须解释「为什么」，否则看着像串了数据。
               兜底是其中最常见的原因：客户端请求的模型名没命中白名单，
               渠道用它的默认模型接单（见渠道管理的「默认模型映射」）。
               另一处提示放在这里而不是单独列一项：它修饰的正是上面这个
               「上游模型」值，分开写会让人以为是两件无关的事 -->
          <div v-if="current.fallback_mapped" class="fallback-note">
            模型名没命中白名单，经渠道的「默认模型映射」改用
            {{ current.model_upstream || '默认模型' }} 请求上游；费用按它的单价计算
          </div>
        </a-descriptions-item>
        <a-descriptions-item label="思考等级">
          <span v-if="current.thinking_level" class="think-pill" :style="{ '--pill-color': thinkingColor(current.thinking_level) }">{{ current.thinking_level }}</span>
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
        <!-- 故障转移链路：每次失败尝试的渠道、状态码与用量。
             失败尝试的 token 上游可能照收（context-length-exceeded 的 400 就是
             典型），但主词元只记最终那次成功/最终应答 —— 账面对不上上游账单
             时先看这里，差额多半是这些失败尝试的消耗 -->
        <a-descriptions-item v-if="(current.retry_trail?.steps?.length || 0) > 0" label="失败链路">
          <div class="trail-list">
            <div v-for="(s, i) in current.retry_trail!.steps" :key="i" class="trail-row">
              <span class="trail-idx">{{ i + 1 }}</span>
              <span class="trail-ch">{{ s.channel_name || '#' + s.channel_id }}</span>
              <span v-if="s.status_code" class="trail-code">{{ s.status_code }}</span>
              <span class="trail-err" :title="s.error">{{ s.error }}</span>
              <span v-if="s.usage && s.usage.total_tokens" class="trail-tokens">
                另耗 {{ fmtTokens(s.usage.total_tokens) }} tokens
              </span>
              <!-- 换渠道前的等待：只有「下一个候选打在同一台上游」才会有。
                   显示出来是为了让「有没有对同一上游连打」这件事可查 ——
                   那正是上游会判我们攻击的形态 -->
              <span v-if="s.wait_before_ms" class="trail-wait" title="换到下一个渠道前的等待，避免对同一台上游连打">
                等 {{ s.wait_before_ms }}ms 后换渠道
              </span>
            </div>
          </div>
        </a-descriptions-item>
        <a-descriptions-item label="耗时">
          首字 {{ fmtMs(current.first_byte_ms) }} · 上游握手 {{ fmtMs(current.upstream_ms) }} ·
          总共 {{ fmtMs(current.total_ms) }}
          <!-- 速度口径与列表一致（见 tokPerSec）：算不出就不显示这一段 -->
          <span v-if="tokPerSec(current)">
            · 速度 <span :title="speedTitle(current)">{{ tokPerSec(current) }} tok/s</span>
          </span>
        </a-descriptions-item>
        <a-descriptions-item label="费用">
          <!-- 与列表同一个规则（utils/money.ts 的 costText）。
               这里原来是 toFixed(8)：同一个数字，列表显示 ¥0.037510、
               详情显示 ¥0.03751000 —— 多出来的两位既不是精度也不是信息 -->
          {{ costText(current.estimated_cost, current.cost_currency) }}
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
   同一手法，行高因此与改版前完全一样（实测 44/45px 两档）。
   行高一变，固定表体的可视行数与滚动位置都会跟着变（见 TABLE_BODY_Y 的说明）。

   ---- 2026-09-24 改版：两排共用一个网格，数字按列右对齐 ----
   改版前每一排是各自独立的 flex，四个数左对齐，于是「↓」的位置跟着「↑」的
   位数左右跑（`↑ 1.33K ↓ 3.66K` 与 `↑ 139 ↓ 42` 的 ↓ 差约 20px）——
   竖着扫这一列时找不到基准。现在两排合成一个 grid，四个格子按列对齐：
     第 1 列 = 输入 / 缓存命中   第 2 列 = 输出 / 命中率
   两排共用同一组轨道（这是关键：改版前是「每排一个网格」，
   auto 轨道跟着各排内容伸缩，整块宽度与左缘因此逐行漂移，
   实测 50 行里 5～6 个位置、左右漂 9.38px）。同一组轨道下，
   右对齐的数字右缘在整列里落在同一条竖线上。

   必须是「块级 + 定宽 = 内容宽」而不是撑满单元格：全局规则里单元格是
   text-align: center（theme.css），撑满的块级盒子会让两排从左起排
   （实测右空隙比左空隙大 42px），而这个盒子同时是**列宽的测量锚点**
   （见 CONTENT_MEASURE），撑满时 scrollWidth 量到的是单元格宽度而不是内容，
   列宽就再也不随内容变了 —— 这正是「费用列锚点失效」的同一个坑。
   width: fit-content 两头都顾上：盒子贴着内容，margin-inline auto 负责居中，
   锚点量到的也就是内容本身。

   ---- 轨道 min 的取法（这里错过一次，记下来）----
   第一版写 minmax(58px, auto) / minmax(52px, auto)，是从「最窄窗口的可用宽度」
   倒推的。结果：**同一列里不同行的右缘仍差 2.78px** —— 因为轨道是每行各自
   计算的，内容不满 58px 的行轨道就是 58，内容 63.56px 的行轨道是 63.56，
   而整块又居中，于是右缘跟着走。
   正确取法是「按真实内容的实测最大值」：把库里所有行的四个槽位逐一量出来
   （见审评取证的 measure-tk.js），得
     第 1 列 max(输入 63.56, 缓存 63.56) = 63.56
     第 2 列 max(输出 49.16, 命中率 40.81) = 49.16
   min 取 66 / 54（留一点余量），两列合计 66 + 8 + 54 = **128px** ——
   与改版前那条单轨道的定宽正好相等，所以列宽行为与之前完全一致，
   只是从「一根轨道」变成「两根对齐的轨道」。
   min 一旦覆盖了真实最大值，auto 就永远不会被触发，
   所有行的轨道因而恒等 —— 这才是右缘能对齐的原因。

   万一真出现八位词元（fmtTokens 写成 1234.56K，约 88px），
   minmax 的 auto 会让那一行的第 1 列让开，不会把数字压到相邻格上 ——
   代价只是那一行的右缘偏一点，而这种事在 25152 行里没有发生过。 */
.token-cell {
  display: grid;
  grid-template-columns: minmax(66px, auto) minmax(54px, auto);
  justify-content: center;
  column-gap: 8px;
  width: fit-content;
  margin: -4px 0;
  margin-inline: auto;
  white-space: nowrap;
  font-size: 12px;
  line-height: 15px;
  font-variant-numeric: tabular-nums;
}
/* display: contents 让两排的四个格子直接成为上面那个网格的子项 ——
   两排因此共用同一组轨道。颜色仍然按 DOM 继承，不受影响 */
.tk-line { display: contents; }
/* 图标与它后面那个数是「一组」：组内 4px。
   整组靠右（justify-self: end）：数字的右缘因此对齐，
   图标会随之左右移动 —— 但图标只是个字形，数字对齐才是扫列时要的。
   两个组之间靠网格的 column-gap 拉开。 */
.tk-pair {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  justify-self: end;
}
/* 命中率与缓存那个数配对，落在第 2 列、右对齐 */
.tk-rate { justify-self: end; }
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

/* 耗时分级：文字色。竖条（.dur-bar）的段色继承同一个 currentColor，
   所以文字与竖条永远不会对不上 */
.lat-fast { color: var(--latency-fast); }
.lat-mid { color: var(--latency-mid); }
.lat-slow { color: var(--latency-slow); font-weight: 500; }
.lat-none { color: var(--color-text-secondary); }

/* 「任务耗时」列：两行数值共用左侧一条竖条。
   竖条**不再固定是绿色**，按截图断成两段：上段跟「首字」、下段跟「耗时」，
   段色各自继承该行数值的 currentColor。一整条单色只能表达「这次调用慢」，
   断成两段才看得出是首字慢还是生成慢。
   数值是正文，走上面那一组 --text-* 分级色（对比度依据见 theme.css）。

   字体比正文小一档、行高压到 15px：两行合计 30px，只比一行正文（22px）高一点。
   再用 -4px 的上下负边距把这两行挤进单元格的 8px 内边距里，
   行高因此与合并前完全一样（实测都是 40.6px，没有被这两行顶高）——
   行高一变，固定表体的可视行数与滚动位置都会跟着变（见 TABLE_BODY_Y 的说明）。

   justify-content: center 而不是 inline-grid：单元格是 text-align: center
   （theme.css 的全局规则），块级 grid 会撑满单元格、内容从左边起排，看着就是
   「这一列没居中」（实测右空隙比左空隙大 27px）；而 inline-grid 会参与行盒，
   把行高从 41px 顶到 49px。块级 grid 居中轨道，行高与居中同时保住。

   **第二列必须是固定宽度，不能是 auto**：auto 轨道跟着数值长短伸缩
   （实测 45.7px「-」→ 87.05px「466.50s」），整块宽度随之变化，而它又是居中的
   —— 于是每行的竖条落在不同的 x 上（实测 50 行里有 1029.42 / 1033.02 /
   1036.63 三个位置，左右漂 7.2px）。站主的原话是「强迫症受不了」。
   定宽之后整块宽度恒定，竖条每行都在同一条竖直线上，整体依旧是居中的。

   94px 是按「列最窄时的可用宽度」倒推的：窗口窄到出现横向滚动时，这一列回到
   声明的 120px，减去左右各 8px 内边距只剩 104px —— 整块 = 3px 竖条 + 6px 间隔
   + 文本轨道，所以文本轨道最多 95px。取 94px 留 1px 余量，装得下库里真实最长值
   466.50s（标签 36 + 组内 gap 6 + 数值 45.05 = 87.05px）。
   真出现四位秒数（1234.57s，需 94.25px）时轨道按 minmax 让开几像素，
   超出单元格的部分按原来的约定溢出，不改变行高（见 .dur-line 的说明）。 */
.dur {
  display: grid;
  justify-content: center;
  /* 3px 竖条 + 6px 间隔 + ≥94px 文本轨道：正常数据下整块恒定 103px，
     最窄窗口（可用 104px）里也不会顶破单元格 */
  grid-template-columns: 3px minmax(94px, auto);
  column-gap: 6px;
  align-items: center;
  margin: -4px 0;
  font-size: 12px;
  line-height: 15px;
  font-variant-numeric: tabular-nums;
}
/* 竖条本身也是一个两行的 grid，两段各占一行 —— 分段因此天然与两行文字齐平，
   不用写死像素高度（字号或行高改了，两段跟着走）。

   ---- 2026-09-24 改版：两段由「连成一整条」改成「两枚独立的短标」----
   改版前是 grid-template-rows: 1fr 1fr + 整条 border-radius + overflow: hidden：
   两段等高、紧贴、共用一个圆角外壳 —— 视觉上就是一根**被分成两截的比例条**。
   但它不是比例条：段高恒为 1fr，与首字/耗时的数值大小毫无关系，
   它表达的只是「这两行各自属于哪个耗时档位」。
   实测代价：站主与审评者都把它读成了「首字占这次调用的比例」，
   而 3px 宽的色块本来也分辨不出档位色（橙 #a16207 与红 #c0392b 在 3px 上
   几乎一样）。**画成比例条就得是比例条** —— 要么改成真比例，要么别像比例条。
   选了后者：两段各留 3px 高、分开成两枚圆角短标，与右侧两行文字一一对位，
   读法变成「上面那行的档位色 / 下面那行的档位色」，不再暗示比例。
   段色仍然继承各自那一行的 currentColor，与数值色同源。 */
.dur-bar {
  grid-column: 1;
  grid-row: 1 / span 2;
  display: grid;
  grid-template-rows: 1fr 1fr;
  align-self: stretch;
  /* 两段之间的缝：没有它两枚短标又会连成一条 */
  row-gap: 3px;
  padding: 1px 0;
}
/* 两段各自继承自己那一行的 currentColor：段色与数值色永远同源，
   不会出现「文字橙、竖条绿」这种对不上的情况 */
.dur-bar i {
  background: currentColor;
  border-radius: 2px;
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
/* 标签固定宽度：两行的数值因此从同一个位置起排，扫一列数时不会左右跳。
   两行都只用两个字（截图就是这样）：三个字的「总耗时」会把数值列推远，
   而这一格的宽度是按最窄列倒推的，一寸都不富余 */
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

/* 费用格里的文本包一层，作为该列的测量锚点（见 CONTENT_MEASURE）。
   必须是普通 inline：inline-block 会参与行盒并改变单元格行高
   （ui-spec 第 10 条实测 41px → 49px），而 inline 盒子的 rect 宽度
   就是文本自身的宽度，正好是要量的那个数。nowrap 保证金额这类
   定长文本不会被折行拆开。 */
.txt-cell { white-space: nowrap; }

/* 思考胶囊（并入模型列，2026-09-18 起替代独立的「思考」列与色点）。
   形态与模型/密钥的 GroupTag 完全同款（padding、圆角、字号、字重、
   淡底与描边的混合比例全部一致）—— 同一行的两个胶囊是同一族的东西，
   只是颜色不同：档位色由模板注入 CSS 变量 --pill-color（THINKING_COLORS
   的值），新增档位只改映射表。
   文字色不能直接用档位原色：那些预设色是给「色点」挑的，当正文写在
   13% 淡底上多数不达 4.5:1（#faad14 只有 1.74:1）——沿用 GroupTag 的
   结论，浅色主题 oklab 混黑、深色主题混白，档位之间仍保有区分度。
   2026-09-18 二次调整：**胶囊不做任何动效**（站主点名）——之前的
   灵光迸发/脉动/呼吸整套撤掉，档位只靠颜色区分。 */
.model-cell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  white-space: nowrap;
}
.think-pill {
  --tp: var(--pill-color, var(--color-text-secondary));
  display: inline-block;
  padding: 1px 8px;
  border-radius: var(--radius-control);
  border: 1px solid color-mix(in oklab, var(--tp) 32%, transparent);
  background: color-mix(in oklab, var(--tp) 13%, transparent);
  color: color-mix(in oklab, var(--tp) 60%, black);
  font-size: 12px;
  font-weight: 500;
  line-height: 20px;
  white-space: nowrap;
  vertical-align: middle;
}
:root[data-theme='dark'] .think-pill {
  color: color-mix(in oklab, var(--tp) 55%, white);
}

/* 速度列（2026-09-21）：每秒词元输出速度，流式行在数值前带「流」胶囊。
   胶囊与 think-pill 同一套视觉语言（描边 + 淡底 + 圆角 + 12px），
   颜色取输出紫 var(--token-output) —— 与词元格「↓ 输出」同色同族：
   速度本来就是输出词元的导数，颜色跟着语义走。深浅主题的混黑/混白
   比例沿用 think-pill 验证过的对比度处理。
   数值用等宽数字（tabular-nums）：同列上下扫的时候数位对齐。 */
.spd-cell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
.stream-pill {
  --tp: var(--token-output);
  display: inline-block;
  padding: 1px 8px;
  border-radius: var(--radius-control);
  border: 1px solid color-mix(in oklab, var(--tp) 32%, transparent);
  background: color-mix(in oklab, var(--tp) 13%, transparent);
  color: color-mix(in oklab, var(--tp) 60%, black);
  font-size: 12px;
  font-weight: 500;
  line-height: 20px;
  white-space: nowrap;
}
:root[data-theme='dark'] .stream-pill {
  color: color-mix(in oklab, var(--tp) 55%, white);
}
/* 数值轨道**定宽**（2026-09-23 修，站主反馈「速度数量不同时左边的『流』样式
   不在同一纵向水平线上」）：速度是「输出词元 ÷ 耗时」，位数随请求变
   （73 / 162 / 2800），而这一块在单元格里是居中的 —— 轨道跟着内容伸缩时，
   整块宽度逐行不同，居中所处的位置自然也不同，左侧那枚胶囊就左右跳。
   这与「任务耗时」是同一个成因（ui-spec 第 10 条记了那次的实测：竖条 x 有两个
   位置、词元内容块左缘漂 9.38px），处理方式也照它 —— .dur 用
   minmax(94px, auto) 把文本轨道托住，这里给数值一个最小宽度。
   66px 按「列最窄时的可用宽度」倒推：速度列下限 118px 减左右内边距 16px 得
   102px，再减去胶囊（12px 字 + 左右各 8px 内边距 + 1px 边框 ≈ 30px）与间距 6px。
   库里实测最大 7000 tok/s（p99 是 268），四位数放得下；真出现五位数时
   min-width 会按内容让开，不会压到相邻单元格。 */
.spd {
  font-variant-numeric: tabular-nums;
  min-width: 66px;
}

/* 兜底说明：跟在上游模型那一项下面的一行解释。
   用次要色且不加底色 —— 它是「为什么这两个值不一样」的解题过程，
   不是告警（请求本身成功了），抢眼会让人以为出了问题 */
.fallback-note {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-secondary);
}

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

/* 失败链路（retry_trail）：每次失败尝试一行 —— 渠道、状态码、错误摘要、
   该次消耗。序号与状态码用次要色的小标签形态：它们是索引信息不是正文 */
.trail-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 100%;
}
.trail-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 12px;
  line-height: 20px;
  min-width: 0;
}
.trail-idx {
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
  flex: 0 0 auto;
}
.trail-ch {
  font-weight: 500;
  flex: 0 0 auto;
}
.trail-code {
  color: var(--text-red);
  font-variant-numeric: tabular-nums;
  flex: 0 0 auto;
}
.trail-err {
  color: var(--color-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
  flex: 1 1 auto;
}
.trail-tokens {
  color: var(--color-text-secondary);
  flex: 0 0 auto;
  font-variant-numeric: tabular-nums;
}

/* 换渠道前的等待。用橙色而不是次要色：它出现就说明这次请求打在了
   与上一次相同的上游上 —— 值得看一眼，而不是被当作常规信息划过。
   橙色与同文件里「用量为估算值」等提示标签同一套语义色 */
.trail-wait {
  color: var(--color-orange);
  flex: 0 0 auto;
  font-variant-numeric: tabular-nums;
}

/* ---- 入场动效所在的那一层 ----
   （效果本身的样式全部搬去了 components/NewLogEffect.vue，这里只留这一层的
   定位与弹性语义 —— 它是动效层的坐标原点，也是「表头与分页之外的剩余高度」的
   传递起点。）

   为什么效果不画在 tr 的 ::after 上（第一版的做法，也是踩过的坑），
   以及三档各自怎么实现，见 NewLogEffect.vue 顶部那段；位置测量见脚本里
   fxTargets 的注释。这一层要说的只有三件事：

   1. 它是 position: relative + overflow: hidden：absolute 的动效层以它为原点，
      超出容器的部分被裁掉，不会撑出滚动条。

   2. 它**不会**影响固定列的吸附：那些 sticky 单元格最近的滚动祖先是
      .ant-table-body（它自己有 overflow: auto），比这一层更近。

   3. is-new 这个类**不带任何样式**，只是「这一行正在做入场动画」的标记 ——
      效果样式挂在效果层（扫光 / 光晕带）或单元格上（滑入 / 呼吸底），
      挂回这个类就等于把伪元素塞回 tr。 */
.log-table {
  position: relative;
  overflow: hidden;
  /* 面板里除表头与分页之外的高度都归表格（见下面那条弹性链）。
     这几行只在「面板是弹性容器」时才起作用 —— 那是看板给的（DashboardView 的
     .dashboard > .panel:last-child），面板不在看板里时它们没有副作用。 */
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

/* ---- 把面板剩下的高度一路传到表体 ----
   看板整页锁高，面板是那块吃掉剩余高度的元素，而 antd 的表格从外层到表体之间
   隔着五六层 div —— 每一层都要把弹性传下去，断在哪一层，高度就停在哪一层：
   表体会退回「按内容全长」（50 行 2062px），整页立刻出现大滚动条。
   回归脚本：.shots/verify-bottom-align.mjs。

   表头与分页是固定高度（表头 39px；分页 24px 加上下各 16px 外边距），
   只让表体伸缩 —— 分页因此被压到面板底部，跟着面板底边走。 */
.log-table :deep(.ant-table-wrapper),
.log-table :deep(.ant-spin-nested-loading),
.log-table :deep(.ant-spin-container),
.log-table :deep(.ant-table),
.log-table :deep(.ant-table-container) {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.log-table :deep(.ant-table-header),
.log-table :deep(.ant-pagination) {
  flex: 0 0 auto;
}

.log-table :deep(.ant-table-body) {
  flex: 1 1 auto;
  min-height: 0;
}

/* 面板自己的占位态（加载 / 首次加载失败）也跟着撑满：否则错误提示会贴在
   一块空面板的最上面。DataState 在别处仍然是普通块，不受影响。 */
.panel :deep(.ds-panel) {
  flex: 1 1 auto;
}

/* ---- 日志队列水位 ----
   12px 小字，与分页组件同一行（左侧）—— 它和「共 N 条 / 页码」都是表格的
   地基信息。原来靠 `margin: -28px` 把它硬拉上分页那一行，那个 28 是按
   「分页块高 32」算的，而实测分页块只有 24px —— 于是它并没有落到分页那一行，
   而是压在分页**下方**并与它重叠：390px 视口下「共 25152 条」被这一行盖住，
   两块文字直接叠在一起（2026-09-24 UI 审评实测，gapY = -28.1px，
   1440/1055/390 三个宽度都重叠）。

   现在不猜高度了：给分页组件加一条下外边距，把这一行**推到分页下面**
   （见下面的 .ant-pagination 规则），负边距整个删掉。
   同一行放不下时的表现也从「重叠」变成「换行」—— 这是布局该有的行为。 */
.queue-status {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: flex-start;
  width: fit-content;
  gap: 6px;
  font-size: 12px;
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}

/* 分页与队列水位之间的间距。用 margin-bottom 而不是水位上的负 margin：
   间距是分页「下方」的属性，写在下游元素上就会在分页高度变化时失准
   （上面那个 -28px 就是这么坏的）。 */
.log-table :deep(.ant-pagination) {
  margin-bottom: 6px;
}

.queue-dot {
  width: 8px;
  height: 8px;
  flex: none;
  border-radius: 50%;
  /* 正常态：低调的绿。跟渠道列表「可用」同一个语义色 */
  background: var(--color-green);
}

/* 丢过日志就整条转红并保持：红色是「统计曾经不完整」的持续提醒，
   不是瞬时闪烁 —— 这个状态只能靠服务重启清零（计数在内存里） */
.queue-status.queue-alert {
  color: var(--color-red);
  font-weight: 500;
}
.queue-status.queue-alert .queue-dot {
  background: var(--color-red);
}
.queue-dropped {
  font-weight: 600;
}

/* ---- 列表小工具条（实时状态 P1-10 +「仅失败」开关 P1-8）----
   一行 flex、26px 高：左边实时状态，右边「仅失败」开关。
   面板外壳（PanelCard → DataState → .panel-body）是纵向 flex，
   这一行 flex:none，表体的 100% 弹性高度自动让位 —— 与「顶上多出一条
   默认密钥告警」同一机制，不需要重算任何高度。 */
.list-bar {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--gap);
  min-height: 26px;
  margin-bottom: 4px;
}

/* 实时状态：绿点 +「实时 · 最后更新 HH:mm:ss」。断线时整行转红并换文案。
   文字色用 --text-red（白底 5.44:1 / 暗色 5.42:1）而不是 --color-red ——
   后者当正文不达 AA，这条恰恰是最需要看清的一句。
   圆点是第二个线索（正常绿 / 断线红），文案是第三个：色觉障碍下同样分得清。 */
.live-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--color-text-secondary);
  font-variant-numeric: tabular-nums;
}
.live-status.off {
  color: var(--text-red);
  font-weight: 500;
}
.live-dot {
  width: 8px;
  height: 8px;
  flex: none;
  border-radius: 50%;
  background: var(--color-green);
}
.live-status.off .live-dot {
  background: var(--color-red);
}
/* 「最后更新」比前面那半句再淡一档：它是佐证不是结论 */
.live-at {
  color: var(--color-text-secondary);
  font-weight: 400;
}

/* 「仅失败」切换：原生 button（不是 a-button）—— 它是切换器不是命令按钮，
   要的是 pill + aria-pressed 的形态；antd 的 checked 态（a-check-tag）
   配色不走主题令牌。
   颜色策略：文字恒用正文色（#303030 / 暗 #e8e6e3，两套主题下都远超 4.5:1），
   开关态由**边框 + 圆点**承载 —— 亮色主题的 --color-red (#ea4343) 在白底
   只有约 3.9:1，当正文不达 AA，但当非文本图形（3:1 即可）是达标的；
   把态交给边框与圆点，文字就永远不用为对比度操心。 */
.fail-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 12px;
  border-radius: 13px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text);
  font-size: 12px;
  line-height: 1;
  cursor: pointer;
  transition:
    border-color 0.15s ease,
    background-color 0.15s ease;
}
.fail-toggle:hover {
  border-color: color-mix(in oklab, var(--color-red) 45%, var(--color-border));
  background: color-mix(in oklab, var(--color-red) 6%, transparent);
}
.fail-toggle:active {
  transform: scale(0.96);
}
/* 激活：红边 + 淡红底 + 实心红点。红点从空心变实心是第二个视觉线索
   （色觉之外），aria-pressed 是第三个（读屏） */
.fail-toggle.on {
  border-color: color-mix(in oklab, var(--color-red) 55%, var(--color-border));
  background: color-mix(in oklab, var(--color-red) 10%, transparent);
  font-weight: 500;
}
/* 焦点环不在这里写：theme.css 有一条全站 :focus-visible（P1-7） */

/* 圆点：未激活空心（只描边），激活实心。空心时边框色 3:1 于白底达标
   （#ea4343 约 3.9:1）；暗色主题的 --color-red 是提亮版 #f08a7a（5.42:1） */
.fail-dot {
  width: 8px;
  height: 8px;
  flex: none;
  border-radius: 50%;
  border: 1.5px solid var(--color-red);
  background: transparent;
  transition: background-color 0.15s ease;
}
.fail-toggle.on .fail-dot {
  background: var(--color-red);
}
</style>
