<script setup lang="ts">
import { computed, h, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import Sortable from 'sortablejs'
import {
  PlusOutlined,
  ReloadOutlined,
  DeleteOutlined,
  EditOutlined,
  LinkOutlined,
  ThunderboltOutlined,
  CloudDownloadOutlined,
  HolderOutlined,
  WarningOutlined,
  InfoCircleOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import ModelWhitelistEditor, { type WhitelistRow } from '@/components/ModelWhitelistEditor.vue'
// Proxy 只用于代理下拉的选项类型
import type { Proxy } from '@/api/types'
import GroupTag from '@/components/GroupTag.vue'
import ChannelIcon from '@/components/ChannelIcon.vue'
import { PROTOCOLS, type Channel, type ChannelGroup, type ChannelBinding } from '@/api/types'
import { symbolOf } from '@/utils/money'
import { fmtTime, pad2 } from '@/utils/fmtTime'
import { emptyPrice, pickPrice } from '@/components/ModelPricingEditor.vue'
import { readStoredChoice, writeStoredChoice } from '@/utils/persistedChoice'

// 记账币种：新增渠道默认人民币 —— 现在接的上游都按人民币开账单。
// 数据列的默认值是美元（加列之前的历史行本就是美元口径），两者不一致是刻意的：
// 一个管「老数据怎么读」，一个管「新渠道怎么填」，界面里会显式带着这个值提交。
const CURRENCY_OPTIONS = [
  { value: 'CNY', label: '¥ 人民币（CNY）' },
  { value: 'USD', label: '$ 美元（USD）' }
]

type ChannelRow = Channel & {
  models?: string[]
  model_count?: number
  /** 白名单里还没配价的条数（由服务端按与计价引擎相同的判据算出） */
  unpriced_count?: number
}

const loading = ref(false)
const rows = ref<ChannelRow[]>([])
const groups = ref<ChannelGroup[]>([])

// ---- 按分组筛选 ----
//
// 值是**字符串**而不是数字：'all' 要能与分组 id 共处一个字段，
// 而 '' 又和「未选择」的语义纠缠不清。统一成字符串后，落到 localStorage 里
// 也天然是原样存取，不需要在读写两头来回转换。
const GROUP_FILTER_KEY = 'channels-group-filter'
const ALL_GROUPS = 'all'

// 初值先给 'all'，等分组表拉回来后再用持久化的值覆盖（见 load）：
// 此刻还不知道有哪些分组，没法校验存下来的 id 是否仍然合法
const groupFilter = ref<string>(ALL_GROUPS)

const groupFilterOptions = computed(() => [
  { value: ALL_GROUPS, label: '全部分组' },
  ...groups.value.map((g) => ({ value: String(g.id), label: g.name }))
])

// 分组可能被删除，而存下来的筛选值还指着它。这时的表现会是「列表永远是空的」，
// 从界面上完全看不出原因 —— 所以在分组表到达后做一次校验，不合法就退回「全部」。
function applyStoredGroupFilter() {
  const allowed = [ALL_GROUPS, ...groups.value.map((g) => String(g.id))]
  groupFilter.value = readStoredChoice(GROUP_FILTER_KEY, allowed, ALL_GROUPS)
}

function onGroupFilterChange(v: string) {
  groupFilter.value = v
  writeStoredChoice(GROUP_FILTER_KEY, v)
}

// 筛选只作用于**显示**：rows 仍然是完整列表，右上的「共 N 个渠道」
// 因此可以说清「筛出来几个 / 一共几个」，不必让用户怀疑是不是渠道丢了。
// 拖拽排序也要用到完整列表（提交的是整个分组的顺序），所以不能就地过滤掉。
//
// 显示顺序 = 优先级顺序：组内按 weight 升序（weight 是组内优先级序号，
// 越小越优先，也就是故障转移时第几个被尝试）。
//
// 排序键里带上 group_id 是为了让「全部分组」视图下同一个分组的渠道连成一块：
// 序号是**组内**编号，只按 weight 排会让两个分组的 1、2、3 交错在一起，
// 看不出任何一条链的顺序。id 只是同序号时的兜底。
const visibleRows = computed(() => {
  const list = groupFilter.value === ALL_GROUPS
    ? rows.value
    : rows.value.filter((r) => String(r.group_id) === groupFilter.value)
  return [...list].sort(
    (a, b) =>
      a.group_id - b.group_id ||
      (a.weight ?? 0) - (b.weight ?? 0) ||
      a.id - b.id
  )
})

const modalOpen = ref(false)
const editing = ref<Channel | null>(null)
const saving = ref(false)

const bindOpen = ref(false)
const bindChannel = ref<ChannelRow | null>(null)
// 抽屉里编辑的是整张白名单，点保存时整表提交
const bindItems = ref<WhitelistRow[]>([])
const bindSaving = ref(false)

const form = reactive({
  name: '',
  protocol: 'openai-chat',
  base_url: '',
  currency: 'CNY',
  api_key: '',
  group_id: 0,
  enabled: true,
  // 出站代理：0 = 直连。模型条目上还能单独覆盖（见白名单里的「代理」列）
  proxy_id: 0,
  // 渠道图标：data URI / 图片地址 / 一两个字符，空表示用默认图标
  icon: '',
  // 单渠道并发上限。新建时给 10：不限并发会让一条渠道把上游打满，
  // 而用户多半没意识到「不限」就是当前的行为
  max_concurrency: 10,
  // 白名单随渠道一起提交：新建渠道时就把「能跑哪些模型」填完
  models: [] as WhitelistRow[]
})

// 代理列表：渠道表单与白名单里的「代理」列共用
const proxies = ref<Proxy[]>([])

const title = computed(() => (editing.value ? '编辑渠道' : '新建渠道'))

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为本来就没有渠道
const loadError = ref('')

/** 按分组 ID 取分组对象，用于渲染分组胶囊（颜色随分组配置） */
function groupOf(id: number) {
  return groups.value.find((g) => g.id === id)
}

/** 代理名；直连（0）或代理已被删掉时返回空，列表里就不显示这一行 */
function proxyName(id: number) {
  if (!id) return ''
  return proxies.value.find((p) => p.id === id)?.name || '已删除的代理 #' + id
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const [c, g, px] = await Promise.all([
      api.get<{ items: ChannelRow[] }>('/channels'),
      api.get<{ items: ChannelGroup[] }>('/groups'),
      api.get<{ items: Proxy[] }>('/proxies')
    ])
    rows.value = c.items || []
    groups.value = g.items || []
    proxies.value = px.items || []
    // 分组表到手后才能校验存下来的筛选值是否还指向一个存在的分组
    applyStoredGroupFilter()
    if (!form.group_id && groups.value.length) {
      // groups.value[0] 要带 ?.：默认分组是用户可以删掉的（见后端 store.Seed），
      // 而「一个分组都没有」时这一行会直接 TypeError
      form.group_id = groups.value.find((x) => x.is_default)?.id ?? groups.value[0]?.id ?? 0
    }
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    protocol: 'openai-chat',
    base_url: '',
    currency: 'CNY',
    api_key: '',
    group_id: groups.value.find((x) => x.is_default)?.id ?? groups.value[0]?.id ?? 0,
    proxy_id: 0,
    icon: '',
    max_concurrency: 10,
    enabled: true,
    models: [{ public_name: '', upstream_name: '', enabled: true, ...emptyPrice() }]
  })
  modalOpen.value = true
}

async function openEdit(row: ChannelRow) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    protocol: row.protocol,
    base_url: row.base_url,
    // 老渠道（加列前建的）读回来是 USD：那正是它们价格数字的口径
    currency: row.currency || 'USD',
    api_key: '',
    group_id: row.group_id,
    enabled: row.enabled,
    proxy_id: row.proxy_id || 0,
    icon: row.icon || '',
    // 没配过并发上限的渠道读出来是 0（不限制），如实显示 ——
    // 强行显示成 10 会让用户以为它一直是 10
    max_concurrency: Number((row.extra_config as any)?.max_concurrency) || 0,
    models: [] as WhitelistRow[]
  })
  modalOpen.value = true
  // 编辑时把已有白名单读出来一起改：白名单是渠道的一部分，
  // 分开两个入口改很容易出现「改了渠道没改模型」的错觉
  try {
    const res = await api.get<{ items: ChannelBinding[] }>('/channels/' + row.id + '/models')
    form.models = (res.items || []).map((b) => ({
      public_name: b.public_name,
      upstream_name: b.upstream_name === b.public_name ? '' : b.upstream_name,
      enabled: b.enabled,
      proxy_id: b.proxy_id || 0,
      // 价格必须一起带上：编辑一次渠道再保存，提交的就是这张表，
      // 漏掉价格等于把用户配好的价全部清零
      ...pickPrice(b)
    }))
  } catch (e: any) {
    message.error('读取模型白名单失败：' + e.message)
  }
}

// 白名单在提交前先自查一遍：后端也会校验，但等一个来回再报错体验差得多
function whitelistPayload(): WhitelistRow[] | null {
  const rows = form.models
    .map((r) => ({
      public_name: r.public_name.trim(),
      upstream_name: r.upstream_name.trim(),
      enabled: r.enabled,
      proxy_id: r.proxy_id || 0,
      ...pickPrice(r)
    }))
    .filter((r) => r.public_name || r.upstream_name)
  const seen = new Set<string>()
  for (const r of rows) {
    if (!r.public_name) {
      message.warning('有白名单条目只填了上游模型名，请补上对外模型名')
      return null
    }
    if (seen.has(r.public_name)) {
      message.warning('模型白名单里对外名重复：' + r.public_name)
      return null
    }
    seen.add(r.public_name)
  }
  return rows
}

// nextExtraConfig 在原有扩展配置上只改并发这一项。
//
// 单独提交一个 { max_concurrency } 会把 headers 等键整体覆盖掉 ——
// 「改个并发把自定义请求头弄没了」是那种当场看不出、过几天才发作的问题。
function nextExtraConfig(): Record<string, unknown> {
  const extra: Record<string, unknown> = { ...((editing.value?.extra_config as any) || {}) }
  if (form.max_concurrency > 0) {
    extra.max_concurrency = form.max_concurrency
  } else {
    delete extra.max_concurrency
  }
  return extra
}

async function save() {
  if (!form.name.trim() || !form.base_url.trim()) {
    message.warning('渠道名称与地址必填')
    return
  }
  const models = whitelistPayload()
  if (!models) return
  if (!models.length) {
    message.warning('请至少填一个模型：没有白名单的渠道不会参与任何路由')
    return
  }
  saving.value = true
  try {
    const payload: Record<string, unknown> = {
      name: form.name.trim(),
      protocol: form.protocol,
      base_url: form.base_url.trim(),
      // 币种总是跟着提交：漏了它，改一次渠道就会把币种打回默认值
      currency: form.currency,
      group_id: form.group_id,
      enabled: form.enabled,
      proxy_id: form.proxy_id || 0,
      // 总是带上：空字符串的语义是「清空图标，回到默认」，
      // 不传的话用户就没法把自定义图标去掉
      icon: form.icon.trim(),
      // extra_config 里有别的键（自定义请求头等），必须整个带着走，
      // 否则改一次并发就把它们抹掉了
      extra_config: nextExtraConfig(),
      models
    }
    if (form.api_key) payload.api_key = form.api_key
    if (editing.value) {
      await api.put('/channels/' + editing.value.id, payload)
      message.success('更新成功')
    } else {
      await api.post('/channels', payload)
      message.success('创建成功')
    }
    modalOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

function confirmDelete(row: Channel) {
  Modal.confirm({
    centered: true,
    title: '确认删除渠道',
    content: '将同时解除该渠道下的所有模型绑定，此操作不可撤销。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/channels/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

// 从上游抓图标。只能在编辑已有渠道时用 —— 触发方式是把当前表单里的地址
// 交给后端的抓取接口，所以它不依赖「先保存」。
const iconFetching = ref(false)

async function fetchIcon() {
  if (!editing.value) {
    message.info('先保存渠道，再抓取图标')
    return
  }
  if (!form.base_url.trim()) {
    message.warning('先填上游地址')
    return
  }
  iconFetching.value = true
  try {
    // 用当前表单里的地址去抓（而不是库里那份）：用户刚改完地址就点抓取，
    // 期望的是从新地址抓
    const res = await api.post<{ ok: boolean; icon: string; error?: string }>(
      '/channels/' + editing.value.id + '/icon',
      { icon: '', base_url: form.base_url.trim() }
    )
    if (res.ok) {
      form.icon = res.icon
      message.success('已获取图标，保存后生效')
    } else {
      Modal.warning({ title: '没能从上游取到图标', content: res.error || '未知原因', width: 520 })
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    iconFetching.value = false
  }
}

// 往渠道发一句 "hi"：后端用真实转发链路（协议转换、鉴权、代理）发一次最小请求。
// 结果的展示方式跟着结果走 —— 成功一条 message 就够，
// 失败要用 Modal：上游的错误信息往往有几十个字，一闪而过读不完
const testingId = ref(0)

interface ChannelTestResult {
  ok: boolean
  status_code?: number
  latency_ms: number
  model?: string
  upstream_model?: string
  reply?: string
  error?: string
}

async function testChannel(row: ChannelRow) {
  // 只挡同一行：原来用 if (testingId.value) return 会让「A 行在测时点 B 行」
  // 被静默吞掉 —— 按钮看起来点了没反应。现在 B 行按钮是 disabled 的，
  // 这里再兜一层只防同一行的重复提交。
  if (testingId.value === row.id) return
  if (testingId.value) {
    message.info('上一项测试还在进行中，请稍候')
    return
  }
  testingId.value = row.id
  try {
    const res = await api.post<ChannelTestResult>('/channels/' + row.id + '/test', {})
    // 停用的渠道也允许测：排查与「先调好再启用」都要用到。
    // 但必须说清楚这次成功不等于已经生效，否则会以为改完就能用了
    const disabledHint = row.enabled ? null : '这条渠道当前是停用状态，测通也不会参与路由；要让它生效请点「启用」'
    if (res.ok) {
      Modal.success({
        title: row.name + ' 连通正常（' + res.latency_ms + ' ms）',
        content: h('div', [
          h('div', '模型：' + (res.model || '-') + (res.upstream_model && res.upstream_model !== res.model ? ' → ' + res.upstream_model : '')),
          h('div', res.reply ? '回复：' + res.reply : '上游返回 ' + (res.status_code || 200) + '，但没有正文（推理型模型可能把内容放在 reasoning 里）'),
          // 颜色走令牌（h() 渲染进 portal 的元素仍继承 :root 变量）：
          // 写死 #d46b08 在白底上只有 3.55:1，不达 AA
          disabledHint ? h('div', { style: 'margin-top:8px;color:var(--text-amber)' }, disabledHint) : null
        ])
      })
    } else {
      Modal.error({
        title: row.name + ' 连通失败' + (res.status_code ? '（HTTP ' + res.status_code + '）' : ''),
        content: res.error || '未知错误',
        width: 560
      })
    }
    // 后端会把这次结果写进 health_status，列表要跟着刷新
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    testingId.value = 0
  }
}

// 启用 / 禁用渠道：与编辑弹窗里那个开关是同一个字段。
//
// 这一列同时承担了两件事：能不能改（是否启用）、以及改完是不是真的有用
// （最近一次探测的结果）。原来这两件事分在两处 —— 状态列一个标签、
// 操作列一个「启用/禁用」，同一个字段两个入口，操作列还被占去一格。
//
// 健康状态没有丢，只是挪进了开关的 tooltip：正常/未探测是常态，不值得每行
// 都摊开文字；而「异常」必须一眼看见，所以额外给一个图标（形状 + 颜色，
// 不靠颜色单独表意）。点开图表就能看到最近一次的错误原文。
type HealthInfo = { text: string; degraded: boolean }

function healthInfo(row: Channel): HealthInfo {
  if (row.health_status === 'degraded') return { text: '最近一次探测异常', degraded: true }
  if (row.health_status === 'healthy') return { text: '最近一次探测正常', degraded: false }
  return { text: '还没探测过', degraded: false }
}

function fmtCheckedAt(v: string | null | undefined) {
  // 悬停提示里的精确时刻，与全站统一口径（原来 toLocaleString 是斜杠分隔）
  return v ? fmtTime(v) : ''
}

// 最近调用显示成「多久以前」而不是时刻：扫一眼列表要判断的是
// 「这条渠道还在不在干活」，相对时间一眼就能比出哪条是活的、哪条是陈的，
// 而一串时刻得先在脑子里做减法。精确时刻放在悬停提示里（见模板）。
// 超过一周就不再说「几天前」了，那时候「具体哪天」比「大概多久」有用。
//
// 这个文案是「加载时刻」的快照：页面开着不动，它不会自己变旧为新的。
// 列表本来就靠「刷新」拉取，没有为这一列单独挂定时器 —— 那会让表格
// 每隔一段时间重渲染一次，而拖拽排序正依赖着 DOM 的稳定。
function fmtAgo(v: string | null | undefined) {
  if (!v) return '—'
  const d = new Date(v)
  if (isNaN(d.getTime())) return '—'
  const sec = Math.floor((Date.now() - d.getTime()) / 1000)
  // 时钟回拨或服务端时间略快时会出现负数，按「刚刚」处理，不显示「-3 秒前」
  if (sec < 60) return '刚刚'
  if (sec < 3600) return Math.floor(sec / 60) + ' 分钟前'
  if (sec < 86400) return Math.floor(sec / 3600) + ' 小时前'
  if (sec < 7 * 86400) return Math.floor(sec / 86400) + ' 天前'
  return pad2(d.getMonth() + 1) + '-' + pad2(d.getDate()) + ' ' + pad2(d.getHours()) + ':' + pad2(d.getMinutes())
}

/** 开关的悬停说明：说清当前状态、以及这个状态意味着什么。 */
function enableTip(row: Channel) {
  if (!row.enabled) return ['已停用：不参与任何路由', '打开开关即可重新接回流量']
  const h = healthInfo(row)
  const lines = ['已启用 · ' + h.text]
  if (row.last_error) lines.push('最近错误：' + row.last_error)
  const at = fmtCheckedAt(row.last_checked_at)
  if (at) lines.push('探测时间：' + at)
  if (!row.last_checked_at) lines.push('点「测试」可以验证它是否真的能连上上游')
  return lines
}

// 正在切换的渠道 id。切换期间开关显示 loading：这是要走一次网络请求的写操作，
// 没有反馈的话用户会以为没点上而反复点。
const togglingId = ref(0)

// 切完立刻改本地值（乐观更新），再落库。
//
// 不等接口返回再刷新：开关是纯视觉的即时控件，先转过去再回滚才符合直觉；
// 反过来（先不动、等接口回来再跳）会让开关看起来「点了没反应」。
// 失败时回滚成原值 —— 界面绝不能停在一个与库里不一致的位置上。
async function toggleChannel(row: ChannelRow, next: boolean) {
  // 与 testChannel 同理：只挡同一行，点别的行时给出提示而不是静默丢弃
  if (togglingId.value === row.id) return
  if (togglingId.value) {
    message.info('上一项操作还在进行中，请稍候')
    return
  }
  togglingId.value = row.id
  row.enabled = next
  try {
    await api.put('/channels/' + row.id, { enabled: next })
    message.success(next ? '已启用' : '已禁用，不再路由到这条渠道')
    // 后端会顺带更新 health_status 等字段，拉一次保持两边一致
    await load()
  } catch (e: any) {
    row.enabled = !next
    message.error(e.message)
  } finally {
    togglingId.value = 0
  }
}

async function openBindings(row: ChannelRow) {
  bindChannel.value = row
  bindOpen.value = true
  await loadBindings()
}

async function loadBindings() {
  if (!bindChannel.value) return
  try {
    const res = await api.get<{ items: ChannelBinding[] }>('/channels/' + bindChannel.value.id + '/models')
    bindItems.value = (res.items || []).map((b) => ({
      public_name: b.public_name,
      // 上游名与对外名相同时留空显示，避免满屏重复的模型名
      upstream_name: b.upstream_name === b.public_name ? '' : b.upstream_name,
      enabled: b.enabled,
      proxy_id: b.proxy_id || 0,
      ...pickPrice(b)
    }))
  } catch (e: any) {
    message.error(e.message)
  }
}

// 整表提交而不是逐条增删：一张表改完一次保存，
// 不会出现「删了两条加了一条只生效一半」的中间状态
async function saveBindings() {
  if (!bindChannel.value) return
  const items = bindItems.value
    .map((r) => ({
      public_name: r.public_name.trim(),
      upstream_name: r.upstream_name.trim() || r.public_name.trim(),
      enabled: r.enabled,
      proxy_id: r.proxy_id || 0,
      ...pickPrice(r)
    }))
    .filter((r) => r.public_name)
  if (!items.length) {
    message.warning('白名单不能为空：没有模型的渠道不会参与任何路由')
    return
  }
  const names = items.map((r) => r.public_name)
  if (new Set(names).size !== names.length) {
    message.warning('模型白名单里有重复的对外名')
    return
  }
  bindSaving.value = true
  try {
    await api.put('/channels/' + bindChannel.value.id + '/models', { items })
    message.success('白名单已保存')
    await loadBindings()
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    bindSaving.value = false
  }
}

// protocolLabel 把协议常量显示成选项里的中文/英文名，
// 列表里直接摊开 anthropic-messages 这种常量对用户没有意义
function protocolLabel(value: string) {
  return PROTOCOLS.find((p) => p.value === value)?.label ?? value
}

// ---- 拖动调整优先级 ----
//
// 列表顺序就是故障转移顺序，所以拖动一行等于改它的优先级序号。
// 序号由后端统一重排（整组全量提交），前端只负责算出「新的顺序是什么」。
const tableWrap = ref<HTMLElement | null>(null)
let sortable: Sortable | null = null

// 读当前 DOM 里的行顺序，取渠道 id。
//
// 以 data-row-key（antd 写在 tr 上的渠道 id）为准，**不**用 sortablejs 给的
// oldIndex/newIndex：那两个下标是按 DOM 子元素个数数的，而 antd 会在 tbody 里
// 多渲染一个 .ant-table-measure-row（列宽测量用，没有 data-row-key），
// 于是下标整体偏一位 —— 按下标去查列表会查到隔壁那条，
// 跨组判断和提交的顺序都会错。实测确认过这个测量行的存在。
function tbodyEl(): HTMLElement | null {
  return tableWrap.value?.querySelector('.ant-table-tbody') ?? null
}

function domOrderedIds(): number[] {
  const tbody = tbodyEl()
  if (!tbody) return []
  return [...tbody.querySelectorAll('tr[data-row-key]')]
    .map((tr) => Number(tr.getAttribute('data-row-key')))
    .filter((n) => Number.isFinite(n) && n > 0)
}

// 拖动开始时的行元素顺序，仅用于「跨组拖动」时把 DOM 摆回去。
let preDragRows: HTMLElement[] = []

function onDragStart() {
  const tbody = tbodyEl()
  preDragRows = tbody
    ? Array.from(tbody.querySelectorAll<HTMLElement>('tr[data-row-key]'))
    : []
}

// 把行按拖动前的顺序摆回 DOM。
//
// 不能靠「把 rows 换成新数组」来触发回弹：数据顺序和拖动前**完全一样**，
// Vue 的 diff 会认为没有变化、一个 DOM 节点都不动，而 sortablejs 已经把行
// 挪走了 —— 结果是后端没改（对），页面上却停在一个错误的顺序上（实测踩过：
// 跨组拖动后页面显示 33,358,359，而后端仍是 359,33）。所以这里直接把
// 节点搬回去，让 DOM 与数据重新一致。
function restoreDomOrder() {
  const tbody = tbodyEl()
  if (!tbody || !preDragRows.length) return
  // 数据行要插在测量行之后：以它为锚点逐条插入，插入顺序即拖动前的顺序
  const measure = tbody.querySelector('tr.ant-table-measure-row')
  let anchor: Node | null = measure ? measure.nextSibling : tbody.firstChild
  for (const tr of preDragRows) {
    tbody.insertBefore(tr, anchor)
    anchor = tr.nextSibling
  }
}

// 某个分组的渠道在新顺序里是否连成一块。
//
// 用「连不连续」判断有没有跨组拖动，而不是「落点邻居是不是同组」：
// 后者会误杀「把本组第一条拖到本组末尾」—— 那种情况下它的下一个邻居
// 正是邻组的第一条，但这次拖动完全合法。
function isContiguousBlock(order: number[], gid: number): boolean {
  const idxs: number[] = []
  order.forEach((id, i) => {
    if (rows.value.find((r) => r.id === id)?.group_id === gid) idxs.push(i)
  })
  if (idxs.length <= 1) return true
  return idxs[idxs.length - 1] - idxs[0] === idxs.length - 1
}

// 提交新的组内顺序。传整个分组的 id 列表：接口是整组全量提交的，
// 幂等且不需要前端算差值。
async function submitOrder(groupID: number, orderedIds: number[]) {
  try {
    await api.put('/channels/order', { group_id: groupID, ids: orderedIds })
    message.success('顺序已更新')
  } catch (e: any) {
    message.error(e.message)
  } finally {
    // 无论成败都重新拉一次：成功要拿到后端重排后的真实序号，
    // 失败要把界面上那份已经和服务端不一致的顺序纠正回来 ——
    // 停在错误的顺序上比报错更糟，用户会以为改成功了
    await load()
  }
}

function onDragEnd(evt: { item?: HTMLElement }) {
  const movedId = Number(evt.item?.getAttribute('data-row-key'))
  if (!movedId) return
  const moved = rows.value.find((r) => r.id === movedId)
  if (!moved) return

  const order = domOrderedIds()
  if (order.length < 2) return

  if (!isContiguousBlock(order, moved.group_id)) {
    message.warning('只能在同一分组内调整顺序')
    // 跨组拖动：后端一点不动，页面也要回到原样
    restoreDomOrder()
    return
  }

  const group = (id: number) => rows.value.find((r) => r.id === id)?.group_id
  const orderedIds = order.filter((id) => group(id) === moved.group_id)

  // 乐观更新：先把本地序号改掉，界面立刻反映新顺序；submitOrder 会再拉一次校准
  orderedIds.forEach((id, i) => {
    const row = rows.value.find((r) => r.id === id)
    if (row) row.weight = i + 1
  })
  void submitOrder(moved.group_id, orderedIds)
}

onMounted(async () => {
  await load()
  // 绑在 tbody 上，不是外层容器：sortablejs 移动的是行本身。
  // 初始化放在 load() 之后，此时行已经渲染出来
  const tbody = tableWrap.value?.querySelector('.ant-table-tbody')
  if (tbody) {
    sortable = Sortable.create(tbody as HTMLElement, {
      // 只认手柄：整行可拖会让「点测试/编辑」变成一次误拖
      handle: '.drag-handle',
      animation: 150,
      ghostClass: 'row-ghost',
      chosenClass: 'row-chosen',
      onStart: onDragStart,
      onEnd: onDragEnd
    })
  }
})

onBeforeUnmount(() => {
  sortable?.destroy()
  sortable = null
})
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <!-- 分组筛选放在「新建渠道」左边：它描述的是「下面这张表显示什么」，
               与新建动作无关，但比新建更常用（渠道一多就是常态视角） -->
          <a-select
            v-model:value="groupFilter"
            :options="groupFilterOptions"
            style="width: 170px"
            @change="onGroupFilterChange"
          />
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建渠道</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">
          共 {{ rows.length }} 个渠道<template v-if="groupFilter !== ALL_GROUPS">
            ，当前显示 {{ visibleRows.length }} 个</template>
        </span>
      </div>

      <DataState
        :error="loadError"
        :has-data="visibleRows.length > 0"
        :loading="loading"
        title="渠道列表加载失败"
        @retry="load"
      >
      <!-- 表格外层：sortablejs 需要拿到 tbody 元素（见 onMounted），
           而 a-table 自身不提供这个 ref -->
      <div ref="tableWrap">
      <!-- scroll.x 必须不小于各列宽度之和：声明偏小时，固定在右侧的
           「操作」列会盖住左边最后一列，表现为表头被截断、内容被压住。
           反过来偏大也不行 —— antd 会把多出来的宽度摊到各列上，
           于是「声明值」和实际渲染宽度对不上，量出来的数就没法用来核对。
           1267 = 各列宽度之和（顺序 44 + 名称 210 + 模型 200 + 上游协议 125
           + 最近调用 174 + 分组 110 + 币种 86 + 启用 78 + 操作 240）。
           名称 170 -> 210 是因为名称后面加了「代理」胶囊：胶囊约 42px，
           原来那 170 减去图标与两处间距只剩 126px，长一点的渠道名会被挤成省略号。
           「最近调用」占的就是原来「地址」那 174。
           「权重」列已去掉（顺序由列表本身表达，不再显示数字），
           换成 44px 的拖拽手柄列；操作列 292 -> 240 是更早那次改动。
           改完实测（scripts/measure-tables.mjs，1440 视口）：容器 1182、表格 1267 -->
      <a-table
        :data-source="visibleRows"
        :loading="loading"
        :pagination="false"
        row-key="id"
        size="small"
        :scroll="{ x: 1267 }"
      >
        <template #emptyText>
          <a-empty
            :description="groupFilter === ALL_GROUPS
              ? '还没有渠道，点「新建渠道」添加第一个'
              : '这个分组下还没有渠道，可以切换分组筛选看看'"
          />
        </template>
        <!-- 拖拽手柄列。这一列没有数字：顺序本身就是优先级，
             也就是故障转移时第几个被尝试 —— 数字是多余的，
             而且要让人工维护的序号和显示的数字始终一致，本身就容易出错 -->
        <a-table-column title="顺序" :width="44">
          <template #default>
            <span class="drag-handle" title="拖动调整优先级（越靠上越优先）">
              <HolderOutlined />
            </span>
          </template>
        </a-table-column>
        <a-table-column title="名称" :width="210">
          <template #default="{ record }">
            <div class="chan-title">
              <ChannelIcon :name="record.name" :icon="record.icon" :size="20" />
              <!-- 名称带 title：加了「代理」胶囊之后这一格更挤，长名字会被
                   省略号截掉，截掉的部分要能悬停看到 -->
              <span class="chan-name" :title="record.name">{{ record.name }}</span>
              <!-- 走了代理的渠道要能一眼看出来：排查「为什么这条渠道的错误
                   和别的渠道不一样」时，第一件事就是确认它的出口。
                   只挂一个「代理」胶囊，不再写「经 XXX 代理」：代理名一长
                   就把名称挤成两行，而这里要回答的只是「有没有走代理」，
                   具体是哪条代理放 title 里，悬停可见。 -->
              <span
                v-if="proxyName(record.proxy_id)"
                class="proxy-tag"
                :title="'经 ' + proxyName(record.proxy_id) + ' 代理'"
              >代理</span>
            </div>
          </template>
        </a-table-column>
        <a-table-column title="模型" :width="200" ellipsis>
          <template #default="{ record }">
            <!-- 白名单是模型存在的唯一依据，「一条都没有」必须显眼：
                 这种渠道看着一切正常，实际任何请求都不会路由到它 -->
            <span v-if="!record.model_count" class="unassigned">未配置模型</span>
            <!-- 列宽有限，装不下的模型名走省略号，完整清单放 title（悬停可见）：
                 模型目录现在只存在于这些白名单里，列表是最常用的查看入口 -->
            <span v-else class="model-names" :title="(record.models || []).join('、')">
              {{ (record.models || []).join('、') }}
              <!-- 漏配价的后果是这笔调用被记成 0 元，而账面上完全看不出异常，
                   所以这里必须点名，而不是等用户自己去核对 -->
              <span v-if="record.unpriced_count" class="unpriced-hint">
                {{ record.unpriced_count }} 个未定价
              </span>
            </span>
          </template>
        </a-table-column>
        <a-table-column title="上游协议" :width="125">
          <template #default="{ record }">
            <!-- 这一列装不下完整名字：最长的「OpenAI Chat Completions」单行要 171px，
                 而列实际只有 129px，不处理就折成两行、把每一行都从 40 顶到 60
                 （其它页的行高都是 40）。用省略号截住，完整名字放 title 悬停可见。
                 用行内块自己截，而不是给列加 ellipsis：单元格是居中的，
                 直接在居中文本上截会在左右两边各切一刀，看不出哪里被截了。 -->
            <span class="proto-name" :title="protocolLabel(record.protocol)">
              {{ protocolLabel(record.protocol) }}
            </span>
          </template>
        </a-table-column>
        <a-table-column title="最近调用" :width="174">
          <template #default="{ record }">
            <!-- 最近调用 = 这条渠道最近一次真的承接了请求（故障转移跳过的
                 尝试不算，它们没落 channel_id）。这一列回答的是「这条渠道
                 是不是还在干活」：备份渠道、被停用的渠道、白名单配错的渠道
                 都会长时间停在同一时刻或干脆没有记录 -->
            <a-tooltip v-if="record.last_used_at">
              <template #title>
                {{ fmtTime(record.last_used_at) }}
                <br />
                这条渠道最近一次承接请求的时间
              </template>
              <span class="last-used">{{ fmtAgo(record.last_used_at) }}</span>
            </a-tooltip>
            <!-- 没有记录不给「从未调用」这种断言：日志过了保留期会被自动清理，
                 那时这里也是空的，说成「从未」就是在编事实 -->
            <a-tooltip v-else>
              <template #title>
                请求日志里没有这条渠道的记录。
                <br />
                可能是一直没被用上，也可能是调用早于日志保留期已被清理
              </template>
              <span class="last-used none">无记录</span>
            </a-tooltip>
          </template>
        </a-table-column>
        <a-table-column title="分组" :width="110">
          <template #default="{ record }">
            <!-- 分组名用全站统一的胶囊：颜色与分组管理里配的一致，
                 这样「渠道属于哪个分组」在列表里一眼能认出来 -->
            <GroupTag
              :name="groupOf(record.group_id)?.name ?? String(record.group_id)"
              :color="groupOf(record.group_id)?.color"
            />
          </template>
        </a-table-column>
        <a-table-column title="币种" :width="86">
          <template #default="{ record }">
            <!-- 单价的单位就挂在这条渠道上：只看到「0.15」看不出是 ¥ 还是 $ -->
            {{ symbolOf(record.currency) }}{{ record.currency }}
          </template>
        </a-table-column>
        <a-table-column title="启用" :width="78">
          <template #default="{ record }">
            <a-tooltip>
              <template #title>
                <div v-for="(line, i) in enableTip(record)" :key="i">{{ line }}</div>
              </template>
              <span class="enable-cell">
                <a-switch
                  size="small"
                  :checked="record.enabled"
                  :loading="togglingId === record.id"
                  :disabled="togglingId !== 0 && togglingId !== record.id"
                  @change="(v: any) => toggleChannel(record, !!v)"
                />
                <!-- 异常才额外给一个图标：开关本身只有「开/关」两态，
                     表示不了「开着但连不上」。用图标而不是只换颜色，
                     色觉障碍下同样能看见。
                     图标不再挂自己的 title：它就在外层 tooltip 里，
                     再挂一个原生 title 会同时冒出两个提示、内容还重复 -->
                <WarningOutlined
                  v-if="record.enabled && healthInfo(record).degraded"
                  class="health-warn"
                />
              </span>
            </a-tooltip>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="240" fixed="right">
          <template #default="{ record }">
            <!-- 操作项统一用 a-button type="link"：裸 <a> 没有 href 就没有
                 隐式 tabindex，键盘用户 Tab 不到、回车也点不动。
                 禁用态交给 :disabled，它会带上 aria-disabled 并阻止点击。 -->
            <a-space>
              <a-button
                type="link"
                size="small"
                :loading="testingId === record.id"
                :disabled="testingId !== 0 && testingId !== record.id"
                @click="testChannel(record)"
              >
                <ThunderboltOutlined />
                {{ testingId === record.id ? '测试中…' : '测试' }}
              </a-button>
              <a-button type="link" size="small" @click="openBindings(record)">
                <LinkOutlined /> 模型
              </a-button>
              <a-button type="link" size="small" @click="openEdit(record)">
                <EditOutlined /> 编辑
              </a-button>
              <a-button type="link" size="small" danger @click="confirmDelete(record)">
                <DeleteOutlined /> 删除
              </a-button>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
      </div>
      </DataState>
    </section>

    <!-- centered + theme.css 里的 .ant-modal-body 上限：弹窗始终完整居中，
         上下留白固定，内容长了滚内容区而不是把弹窗顶出屏幕。
         760 与白名单抽屉同宽：白名单有六个格子，再窄模型名就被截了 -->
    <a-modal
      v-model:open="modalOpen"
      :title="title"
      :confirm-loading="saving"
      :width="'min(760px, 94vw)'"
      centered
      ok-text="保存"
      @ok="save"
    >
      <!-- 表单排布：短字段两列并排、长字段整行，说明文字移到标签旁的 ⓘ 里。
           纵向一列排下来时每个字段要占掉「标签 + 控件 + 两三行说明」，
           十段表单就是一千多像素高，找字段得一路滚 -->
      <a-form layout="vertical" class="channel-form">
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="渠道名称" required>
              <a-input v-model:value="form.name" placeholder="例如 ohub-deepseek" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item required>
              <template #label>
                上游协议
                <a-tooltip title="客户端用哪种协议请求都行：会先归一成 OpenAI Chat，再按这里选的协议转成上游格式。">
                  <InfoCircleOutlined class="label-hint" />
                </a-tooltip>
              </template>
              <a-select v-model:value="form.protocol" :options="PROTOCOLS" />
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item required>
          <template #label>
            上游地址
            <a-tooltip title="填到 /v1 或只填域名都行，转发时会按所选协议补全路径。">
              <InfoCircleOutlined class="label-hint" />
            </a-tooltip>
          </template>
          <a-input v-model:value="form.base_url" placeholder="https://api.example.com/v1" />
        </a-form-item>

        <a-row :gutter="12">
          <a-col :span="10">
            <a-form-item required>
              <template #label>
                记账币种
                <a-tooltip title="这家上游按什么币种给你开账单就选哪个：下面的单价按它录入，日志与看板也按它统计。">
                  <InfoCircleOutlined class="label-hint" />
                </a-tooltip>
              </template>
              <a-select v-model:value="form.currency" :options="CURRENCY_OPTIONS" />
            </a-form-item>
          </a-col>
          <a-col :span="14">
            <a-form-item :label="editing ? 'API Key（留空表示不修改）' : 'API Key'">
              <a-input-password v-model:value="form.api_key" placeholder="sk-..." />
            </a-form-item>
          </a-col>
        </a-row>
        <!-- 币种不换算这条留在页面上而不是收进 ⓘ：它正是「改币种之后单价要重填」
             的原因，而用户改币种的那一刻不会去悬停一个图标 -->
        <div class="form-note">不同币种之间不做任何换算、也不相加；改了币种之后，已填的单价需要自己重填。</div>

        <a-row :gutter="12">
          <a-col :span="10">
            <a-form-item>
              <template #label>
                所属分组
                <a-tooltip title="分组决定路由与权限范围；这条渠道能跑哪些模型由下面的白名单决定。">
                  <InfoCircleOutlined class="label-hint" />
                </a-tooltip>
              </template>
              <a-select v-model:value="form.group_id">
                <a-select-option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</a-select-option>
              </a-select>
            </a-form-item>
          </a-col>
          <a-col :span="14">
            <a-form-item label="启用">
              <div class="switch-row">
                <a-switch v-model:checked="form.enabled" />
                <span class="switch-hint">停用的渠道不参与路由</span>
              </div>
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item>
          <template #label>
            渠道图标
            <a-tooltip title="「从上游获取」会去渠道的上游站点抓一次 favicon；抓不到就用默认图标（渠道名首字母）。也可以直接填一个 emoji。">
              <InfoCircleOutlined class="label-hint" />
            </a-tooltip>
          </template>
          <div class="icon-row">
            <ChannelIcon :name="form.name" :icon="form.icon" :size="28" />
            <a-input
              v-model:value="form.icon"
              placeholder="图片地址 / data URI，或者直接写一个 emoji"
              allow-clear
            />
            <a-button :loading="iconFetching" @click="fetchIcon">
              <CloudDownloadOutlined /> 从上游获取
            </a-button>
          </div>
        </a-form-item>

        <a-row :gutter="12">
          <a-col :span="14">
            <a-form-item>
              <template #label>
                出站代理
                <a-tooltip title="代理不可用时请求直接失败，不会悄悄改成直连。">
                  <InfoCircleOutlined class="label-hint" />
                </a-tooltip>
              </template>
              <a-select v-model:value="form.proxy_id">
                <a-select-option :value="0">直连（不使用代理）</a-select-option>
                <a-select-option v-for="p in proxies" :key="p.id" :value="p.id">
                  {{ p.name }}（{{ p.protocol }}://{{ p.host }}:{{ p.port }}）{{ p.enabled ? '' : ' · 已停用' }}
                </a-select-option>
              </a-select>
            </a-form-item>
          </a-col>
          <a-col :span="10">
            <a-form-item>
              <template #label>
                并发上限
                <a-tooltip title="同时发往这条上游的请求数上限；0 表示不限制。">
                  <InfoCircleOutlined class="label-hint" />
                </a-tooltip>
              </template>
              <a-input-number v-model:value="form.max_concurrency" :min="0" style="width: 100%" />
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item required>
          <template #label>
            模型白名单与映射
            <a-tooltip title="「模型映射」把客户端请求的模型名换成上游真正认识的模型名，留空表示同名；需要单独出口的模型可以在「代理」列覆盖渠道设置。">
              <InfoCircleOutlined class="label-hint" />
            </a-tooltip>
          </template>
          <ModelWhitelistEditor v-model:items="form.models" :proxies="proxies" :currency="form.currency" />
        </a-form-item>

        <div class="form-note">
          优先级：{{ editing ? '在列表里拖动这一行即可调整，越靠上越优先。' : '新渠道会排在所属分组的最后。' }}
        </div>
      </a-form>
    </a-modal>

    <!-- 抽屉宽度 760：白名单一行有六个格子，620 时两个模型名输入框只剩
         90 多像素，模型名和占位符都被截成「deepsee」「deeps...」。
         用 min(...) 而不是写死：窄窗口下不至于把抽屉顶出屏幕 -->
    <a-drawer
      v-model:open="bindOpen"
      :title="'模型白名单 · ' + (bindChannel?.name ?? '')"
      :width="'min(760px, 94vw)'"
    >
      <!-- 这里原来套了一层 a-card + 「这条渠道能跑哪些模型」的标题：
          抽屉标题已经说了这是哪条渠道的白名单，卡片只是多一层边框和一句重复的话 -->
      <div class="bind-intro">
        只有写在这里的模型才会被路由到这条渠道。<b>对外名</b>是客户端请求时用的名字，
        <b>上游名</b>是转发时替换成的名字（留空即同名）。写错对外名是最常见的 502 原因。
      </div>
      <ModelWhitelistEditor v-model:items="bindItems" :proxies="proxies" :currency="bindChannel?.currency" />

      <!-- 保存放到抽屉底部：原来是一个通栏大按钮杵在内容中间，
           既像块砖又把「添加一行」和它挤在一起 -->
      <template #footer>
        <div class="bind-footer">
          <span class="bind-hint">改动要点「保存白名单」才会写回渠道</span>
          <a-button @click="bindOpen = false">取消</a-button>
          <a-button type="primary" :loading="bindSaving" @click="saveBindings">保存白名单</a-button>
        </div>
      </template>
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
}
.toolbar-left { display: flex; gap: var(--gap); }
.toolbar-spacer { flex: 1; }
.toolbar-hint { color: var(--color-text-secondary); font-size: 13px; }
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
/* ---- 渠道表单（弹窗里那一段）----
   antd 纵向表单每个字段下方留 24px，字段一多整页就散。
   收到 12px 后再把标签与控件贴紧一点，一屏能多放两三段。 */
.channel-form :deep(.ant-form-item) { margin-bottom: 12px; }
.channel-form :deep(.ant-form-item-label) { padding-bottom: 2px; }
.channel-form :deep(.ant-form-item-label > label) { height: 22px; }
/* 标签旁的 ⓘ：说明文字收进 tooltip 后，标签本身要给出「这里有说明」的线索。
   用小一号的次要色，别让它跟标签抢注意力 */
.label-hint {
  margin-left: 4px;
  font-size: 12px;
  color: var(--color-text-secondary);
  cursor: help;
}
/* 整行说明（不挂在某个输入框下面，所以左边距按表单项对齐） */
.form-note {
  margin: -4px 0 12px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-secondary);
}
/* 开关与它的说明同一行：开关只有 16px 高，下面再单起一行说明太浪费 */
.switch-row { display: flex; align-items: center; gap: 8px; height: 32px; }
.switch-hint { font-size: 12px; color: var(--color-text-secondary); }
/* 白名单抽屉的说明块：用左侧一道主色竖线代替整块告警底色。
   这句话是「怎么填」的说明，不是需要警惕的异常状态；用 a-alert 会得到
   一整块主题色底 + 图标，在抽屉里比它要说明的表格还抢眼 */
.bind-intro {
  margin-bottom: 12px;
  padding: 8px 10px;
  border-left: 3px solid color-mix(in oklab, var(--color-primary) 60%, transparent);
  border-radius: 0 var(--radius-control) var(--radius-control) 0;
  background: color-mix(in oklab, var(--color-primary) 7%, transparent);
  font-size: 12px;
  line-height: 1.8;
}
/* 加粗的说明文字是正文，用 ink 版（主色在白底上只有 3.32:1） */
.bind-intro b { color: var(--text-primary-ink); font-weight: 600; }
/* 抽屉底部：提示靠左、按钮靠右 */
.bind-footer { display: flex; align-items: center; gap: 8px; }
.bind-hint { margin-right: auto; font-size: 12px; color: var(--color-text-secondary); }
.unassigned { color: var(--color-text-secondary); }
/* 名称那一格：图标 + 名字 + 可选的「代理」胶囊。
   margin-bottom 跟着「经 xxx 代理」那行一起去掉：现在只有一行了 */
.chan-title { display: flex; align-items: center; gap: 6px; }
.icon-row { display: flex; align-items: center; gap: 8px; }
.chan-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 「代理」胶囊：中性配色，与分组胶囊（GroupTag）同一套尺寸，
   但不参与分组配色 —— 它表达的是「出口」，与属于哪个分组无关。
   flex: 0 0 auto 让它不被压缩：被压的应该是渠道名。 */
.proxy-tag {
  flex: 0 0 auto;
  padding: 1px 8px;
  border-radius: var(--radius-control);
  border: 1px solid var(--color-border);
  background: color-mix(in oklab, var(--color-text-secondary) 10%, transparent);
  color: var(--color-text-secondary);
  font-size: 12px;
  font-weight: 500;
  line-height: 20px;
  white-space: nowrap;
}
.model-names { color: var(--color-text); }
/* 上游协议那一列：行内块自己截断（原因见模板里的注释） */
.proto-name {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}
/* 最近调用用等宽数字：这一列是时间量，比例字体下「分钟前」三个字的宽度
   会随数字变化，一列里参差不齐；tabular-nums 让它们对齐成一条竖线。
   这里原来还有一条 cursor: default，用来避免这一格出现文本 I 型；
   现在全站默认光标本身就是一张箭头图，继承下来就是箭头，不需要再声明。 */
.last-used {
  color: var(--color-text);
  font-variant-numeric: tabular-nums;
}
/* 「无记录」比正常时间弱一档：它是个空状态，不该和真时间抢同样的分量 */
.last-used.none { color: var(--color-text-secondary); }
/* 未定价提示：橙色而不是灰色 —— 它是一个待办，不是一句说明 */
.unpriced-hint {
  margin-left: 4px;
  padding: 0 5px;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-orange) 15%, transparent);
  color: var(--color-orange);
  font-size: 12px;
}
.muted { color: var(--color-text-secondary); }
.disabled { color: var(--color-text-secondary); cursor: not-allowed; }
/* 开关与异常图标同一行：图标紧跟在开关右侧，间距小一点才像「附属提示」 */
.enable-cell { display: inline-flex; align-items: center; gap: 6px; }
/* 异常提示用橙色，与「未定价」那类待办同色系；只在探测失败时出现 */
.health-warn { color: var(--color-orange); font-size: 13px; }
/* 「顺序」表头在 44px 的列里折成了上下两行，把整行表头从 39px 顶到 61px：
   antd 在每侧留 8px 内边距，44px 的列只剩 28px 给文字，而这两个字正好是
   28px 宽，卡在折行的边界上。这里只收窄这一列的内边距，**不加列宽** ——
   列宽总和 1227 是量过的（见表格上方的注释），而且窄窗口下表格本来就在横向
   溢出、固定的「操作」列已经在挤左邻列，再加宽只会让那个问题更明显。
   选择器要一路写到 `> tr > th` 而不是只写 `.ant-table-thead th`：
   antd 自己的内边距规则带 4 个类（.ant-table-wrapper .ant-table
   .ant-table-small .ant-table-thead），写短了会被它压住、改了等于没改
   （实测：短选择器下内边距仍是 8px、表头仍是 61px） */
:deep(.ant-table-wrapper .ant-table .ant-table-thead > tr > th:first-child) {
  padding-left: 4px;
  padding-right: 4px;
}
/* 拖拽手柄：平时低调，悬停才明显 —— 它是个辅助操作，
   不该和「测试/编辑」那些动作抢注意力。grab 光标是唯一的可拖拽提示 */
.drag-handle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  color: var(--color-text-secondary);
  cursor: grab;
}
/* 拖拽把手是图标（非文本，阈值 3:1），但 --color-primary 在浅色下
   对白卡片 3.32:1 只是刚过线、在深色下 3.98:1 也偏弱；
   --text-primary-ink 是主色的可读版，两套主题下都很清楚。 */
.drag-handle:hover { color: var(--text-primary-ink); }
.drag-handle:active { cursor: grabbing; }
/* 拖动中的行：半透明让下面的落点看得见（sortablejs 的 ghostClass） */
.row-ghost { opacity: 0.4; background: color-mix(in oklab, var(--color-primary) 10%, transparent); }
.row-chosen .drag-handle { color: var(--text-primary-ink); }
</style>

