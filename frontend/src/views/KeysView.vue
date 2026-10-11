<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { message, modal } from '@/utils/feedback'
import { modalWidth } from '@/utils/modalWidth'
import {
  PlusOutlined,
  ReloadOutlined,
  DeleteOutlined,
  CopyOutlined,
  CheckOutlined,
  EditOutlined,
  KeyOutlined,
  DashboardOutlined,
  StopOutlined,
  PlayCircleOutlined,
  SearchOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import { useFormValidate } from '@/composables/useFormValidate'
import { useDirtyClose } from '@/composables/useDirtyClose'
import { useCopyFeedback } from '@/composables/useCopyFeedback'
// 本地搜索 + 客户端分页（四张管理表共用，判据在 components/tableTools.ts 有单测）
import { useTableTools } from '@/composables/useTableTools'
// 批量启停（与渠道页共用：顺序执行、失败不中断、汇报失败名单）
import { useBatchToggle } from '@/composables/useBatchToggle'
// 行级操作（启停 / 删除）之后把焦点还给同一行的同一个按钮（见 utils/focusRow）
import { focusRowAction } from '@/utils/focusRow'
import DataState from '@/components/DataState.vue'
import EmptyState from '@/components/EmptyState.vue'
import GroupTag from '@/components/GroupTag.vue'
import { writeClipboard } from '@/utils/clipboard'
import { fmtTime } from '@/utils/fmtTime'

// 表单校验（2026-09-24 UI 审评补）：原来名称的 required 只是个视觉星号，
// 弹窗里失焦没有任何反应，填漏了只得到顶部一句 message
import type { FormInstance, Rule } from 'ant-design-vue/es/form'

import type { APIKey, ChannelGroup } from '@/api/types'

const loading = ref(false)
const rows = ref<APIKey[]>([])
// 本地搜索 + 客户端分页（2026-10-11）：接口一次返回全量，所以两件事都在前端做。
// 可搜索的字段 = 这一页所有能一眼看到的文本列（名称、密钥前缀）+ 两个白名单
// （它们列在表里，用户会照着它们搜「哪些密钥能用 glm」）
const {
  q,
  rows: pagedRows,
  pagination,
  total,
  filtered: filteredKeys
} = useTableTools(rows, {
  storageKey: 'keys-page-size',
  searchText: (k) =>
    [k.name, k.key_prefix, (k.allowed_models || []).join(' '), (k.allowed_groups || []).join(' ')].join(' ')
})

// 批量启停（2026-10-11）：作用在**当前筛选结果**上，与渠道页同一套
// （理由见 composables/useBatchToggle.ts 的文件头：勾选列要多占 32px 宽，
//  而这两张表的列宽都已经贴着容器宽了）。
const batch = useBatchToggle<APIKey>({
  noun: '密钥',
  label: (k) => k.name,
  setEnabled: async (k, enabled) => {
    await api.put('/keys/' + k.id, { enabled })
  },
  reload: load
})

/** 停用会让正在使用它的客户端当场收到 401，所以先确认一次（与渠道页一致） */
function confirmBatchDisable() {
  const n = filteredKeys.value.length
  if (!n) return
  // 报真正会改动的条数（见 useBatchToggle.needChange）：筛选结果里已是停用
  // 状态的密钥会被跳过，报总数会让提示与结果对不上
  const willChange = batch.needChange(filteredKeys.value, false).length
  const already = n - willChange
  if (willChange === 0) {
    message.info('这 ' + n + ' 个密钥已经是停用状态，无需改动')
    return
  }
  const scope = q.value.trim() ? '搜索「' + q.value.trim() + '」' : '全部密钥'
  modal.confirm({
    centered: true,
    title: '批量停用 ' + willChange + ' 个密钥？',
    content:
      '作用范围：' + scope + '。' +
      (already > 0 ? '其中 ' + already + ' 个本来就是停用状态，会被跳过。' : '') +
      '停用后使用这些密钥的客户端会立即收到 401，直到你重新启用它们。',
    okText: '停用这 ' + willChange + ' 个',
    cancelText: '取消',
    okType: 'danger',
    onOk: () => batch.run(filteredKeys.value, false)
  })
}
// 表格容器的 ref：行级操作（启停 / 删除）之后焦点往哪儿去的落点（见 utils/focusRow）
const tableWrap = ref<HTMLElement | null>(null)
const groups = ref<ChannelGroup[]>([])
const modalOpen = ref(false)
const saving = ref(false)
const createdKey = ref('')
const editing = ref<APIKey | null>(null)

const form = reactive({
  name: '',
  rate_limit_rpm: 0,
  enabled: true,
  // 白名单为空数组表示不限制，与后端 StringList 的语义一致
  allowed_models: [] as string[],
  allowed_groups: [] as string[]
})

const title = computed(() => (editing.value ? '编辑密钥' : '新建密钥'))

// ---- 表单校验（2026-09-24 UI 审评补）----
//
// 补之前：`<a-form-item label="名称" required>` 的 required 只是个**视觉星号** ——
// a-form 没绑 :model、字段没绑 name、表单没绑 :rules，失焦不校验，
// 提交失败只有顶部一句「名称必填」。这里把它接上：
// 名称失焦即校验、错误显示在输入框下方；提交前 validateForm() 兜底。
const formRef = ref<FormInstance>()

const formRules: Record<string, Rule[]> = {
  name: [
    { required: true, message: '给这把密钥起个名字：它在列表与分组统计里标识调用方', trigger: 'blur' },
    { max: 64, message: '名字最长 64 个字符', trigger: 'blur' }
  ]
}

// 校验失败的统一收尾（滚动 + 聚焦）在 composables/useFormValidate.ts，
// 四个视图共用同一份
const { validateForm } = useFormValidate(formRef)

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为密钥本来就没有
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<{ items: APIKey[] }>('/keys')
    rows.value = res.items || []
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

// 分组白名单条目的解析：条目统一是分组 ID（保存时后端归一化），但老数据、
// 导入的备份里可能还是名字 —— 数字先按 ID 找，找不到再按名字找一次，
// 都找不到的就是悬空引用（分组已删），界面上要能看出来而不是显示一个裸 ID
interface GroupRefInfo {
  ref: string
  id?: number
  label: string
  color?: string
  missing: boolean
}
function groupRefInfo(ref: string): GroupRefInfo {
  const g =
    groups.value.find((x) => String(x.id) === ref) ?? groups.value.find((x) => x.name === ref)
  if (g) return { ref, id: g.id, label: g.name, color: g.color, missing: false }
  return { ref, label: ref, missing: true }
}

// 列表渲染用：一次解析整条记录的分组引用。
// 不要在模板里对同一个 ref 反复调 groupRefInfo —— 每次调用都要扫两遍 groups
function resolvedGroups(refs: string[] | null): GroupRefInfo[] {
  return (refs || []).map(groupRefInfo)
}

// 密钥明文：按需从后端解密，取到后缓存在内存里。
// 缓存是刻意的 —— 悬停和点击复制是同一个诉求的两种触发方式，
// 不缓存的话鼠标扫过列表就会打出一串解密请求。
//
// ---- 但缓存必须会过期（2026-10-11）----
//
// 原来是一份**永不过期**的映射：鼠标从列表上扫过之后，那把密钥的明文就一直在
// JS 内存里躺到这个标签页关闭为止。两个问题：
//
//   1. 机密性。共享屏幕、录屏、或者别人接手这台机器时打开 DevTools，
//      都能从内存里读到已经不再显示在屏幕上的明文。缓存本来只是为了让
//      「悬停 → 点击复制」不重复解密，这个理由撑不起「一直留着」。
//   2. 正确性。密钥在别处轮换过之后，界面还在拿旧明文……用户复制走、
//      粘进客户端、调用失败，而界面上「看起来」是好的。
//
// 所以给两个出口：60 秒 TTL（够完成「看一眼 → 复制」这个动作），
// 以及**页面一切走就全部丢掉**（那时缓存的存在理由已经不成立了）。
interface RevealResult {
  available: boolean
  key?: string
  reason?: string
}
type RevealEntry = { res: RevealResult; at: number }
const revealed = reactive<Record<number, RevealEntry | 'loading'>>({})

/** 明文缓存的存活时间：够「悬停看一眼再点复制」，又不至于一直留着 */
const REVEAL_TTL_MS = 60_000

/** 取仍然新鲜的缓存（过期即丢），没有则返回 null */
function freshReveal(id: number): RevealResult | null {
  const entry = revealed[id]
  if (!entry || entry === 'loading') return null
  if (Date.now() - entry.at > REVEAL_TTL_MS) {
    delete revealed[id]
    return null
  }
  return entry.res
}

/** 丢掉全部明文（切走页面 / 离开这一页时调用） */
function dropRevealed() {
  for (const k of Object.keys(revealed)) delete revealed[Number(k)]
}

async function ensureKey(record: APIKey): Promise<RevealResult | null> {
  const cached = freshReveal(record.id)
  if (cached) return cached
  const entry = revealed[record.id]
  if (entry === 'loading') return null
  revealed[record.id] = 'loading'
  try {
    const res = await api.get<RevealResult>('/keys/' + record.id + '/reveal')
    revealed[record.id] = { res, at: Date.now() }
    return res
  } catch (e: any) {
    const fail: RevealResult = { available: false, reason: e.message || '读取失败' }
    revealed[record.id] = { res: fail, at: Date.now() }
    return fail
  }
}

// 页面不可见即丢掉全部明文（安全上的默认动作，见上面那段说明）。
// 挂在 document 上而不是用 onDeactivated：切换浏览器标签、最小化、锁屏
// 都会触发 visibilitychange，而这些正是「人不在看着」的时刻。
function onVisibilityDrop() {
  if (document.hidden) dropRevealed()
}

function tooltipOf(record: APIKey) {
  const info = revealed[record.id]
  if (info === 'loading') return '读取中…'
  // 走 freshReveal 而不是直接读 revealed[record.id]（2026-10-11 修）：
  // 悬停提示是**唯一会把明文显示出来**的路径，它原来绕过了 TTL ——
  // 只要明文在 60 秒内被读进过缓存（鼠标扫过列表就会触发 open-change），
  // 之后每次悬停都会原样把它显示出来，而且不重新解密、永不过期。
  // 那正好推翻了上面写下的两个理由（内存里不长期留明文、别处轮换后不显示旧值）。
  // 两份取值口径必须**同一份**：只修缓存那边，显示这一边就还是漏的。
  const fresh = freshReveal(record.id)
  if (!fresh) return '悬停读取完整密钥，点击复制'
  if (!fresh.available) return fresh.reason || '明文不可用'
  return fresh.key + '（点击复制）'
}

// 写剪贴板的实现提取到了 utils/clipboard.ts（日志详情的 Trace ID 复制
// 也要用）：降级路径只维护一份，别处出问题只改一处。

// 复制成功的原位反馈（图标变形为对勾，见 useCopyFeedback 的说明）：
// 三处复制（密钥胶囊 / BaseURL 胶囊 / 新建弹窗明文）共用一个状态
const { copiedKey: copiedWhat, markCopied } = useCopyFeedback()

// 复制结果的读屏播报（2026-10-11）。
// 复制成功的视觉反馈是「图标原位变成绿色对勾」——纯视觉，读屏用户点完
// 得不到任何确认。用一行 sr-only 的 aria-live 补上（看板的实时数字播报
// 用的是同一套做法，.sr-only 见 theme.css）。
const copiedText = computed(() => {
  if (!copiedWhat.value) return ''
  if (copiedWhat.value === 'baseurl') return '已复制 BaseURL'
  if (copiedWhat.value === 'created') return '已复制新密钥'
  return '已复制密钥明文'
})

async function copyRowKey(record: APIKey) {
  const info = await ensureKey(record)
  if (!info) return
  if (!info.available || !info.key) {
    // 看不到明文不是错误，是一种正常状态（升级前创建的密钥只存过哈希）
    modal.info({ title: '看不到这把密钥的明文', content: info.reason || '明文不可用', width: 460 })
    return
  }
  if (await writeClipboard(info.key)) markCopied('key-' + record.id)
  else message.warning('复制失败，请在悬浮提示里手动选择复制')
}

// 客户端该填的调用地址 —— 中转端点和这个后台同进程、同端口（见后端 Register 里的
// /v1 与 /v1beta 路由分组），所以它就是当前访问的这个源加上 /v1。
//
// 不写死 127.0.0.1:8888：端口可能改过，也可能从别的机器用 IP 打开这个后台，
// 写死的地址复制走就是错的。带 /v1 是因为它要填进各家客户端的 Base URL 输入框，
// 客户端会把 /chat/completions 接在它后面。
const baseUrl = window.location.origin + '/v1'

async function copyBaseUrl() {
  // 与复制密钥共用 writeClipboard：http 下 clipboard API 不可用、
  // 以及 writeText 被挂起等授权这两种情况它都处理过了
  if (await writeClipboard(baseUrl)) markCopied('baseurl')
  else message.warning('复制失败，请手动选择复制')
}

async function loadGroups() {
  try {
    const res = await api.get<{ items: ChannelGroup[] }>('/groups')
    groups.value = res.items || []
  } catch (e: any) {
    message.error('分组候选加载失败：' + e.message)
  }
}

// 空白表单：新建、以及放弃改动后复位时共用同一份定义
function blankForm() {
  form.name = ''
  form.rate_limit_rpm = 0
  form.enabled = true
  form.allowed_models = []
  form.allowed_groups = []
}

// 防误关：遮罩不再能关掉弹窗，有改动时按 X / Esc 要先确认（见 composable 顶部）
const dirty = useDirtyClose({
  open: modalOpen,
  snapshot: () => JSON.stringify(form),
  onDiscard: () => {
    editing.value = null
    createdKey.value = ''
    blankForm()
  }
})

function openCreate() {
  editing.value = null
  blankForm()
  createdKey.value = ''
  modalOpen.value = true
  dirty.markClean()
}

function openEdit(row: APIKey) {
  editing.value = row
  form.name = row.name
  form.rate_limit_rpm = row.rate_limit_rpm
  form.enabled = row.enabled
  form.allowed_models = [...(row.allowed_models || [])]
  // 白名单条目换成分组 ID（受控多选的值）：库里老条目可能是名字，
  // 能解析就换，解析不了的（悬空引用）原样带着 —— 保存时后端会报错指出它
  form.allowed_groups = (row.allowed_groups || []).map((ref) => {
    const info = groupRefInfo(ref)
    return info.id !== undefined ? String(info.id) : ref
  })
  createdKey.value = ''
  modalOpen.value = true
  dirty.markClean()
}

// 限流额度的展示：0 表示跟随全局默认，负数表示不限
function limitText(v: number) {
  if (v === 0) return '跟随全局默认'
  if (v < 0) return '不限'
  return v + ' 次/分钟'
}

// 白名单展示：空表示不限制
function whitelistText(v: string[] | null) {
  if (!v || v.length === 0) return '不限'
  return v.join('、')
}

// 模型白名单是 tags 模式，可以自由输入，提交前去空白、去重
//（分组白名单是 multiple 模式，值只能来自选项、且是分组 ID，无需这一步）
function cleanList(v: string[]) {
  return Array.from(new Set((v || []).map((s) => String(s).trim()).filter(Boolean)))
}

// onCancel 处理「取消 / 再建一把」：已经看到明文时，这个按钮的语义是
// 「再建一把」—— 清掉明文并重置表单，弹窗留着；其余情况就是普通关闭。
//
// 普通关闭这一支走防误关（dirty.guardCancel）：改过内容就先问一句再关。
// 「再建一把」那一支**不**问 —— 用户明确点的是「再建一把」，而且此时
// 明文已经展示过、改动也已经落库，弹窗根本没打算关。
function onCancel() {
  if (createdKey.value) {
    createdKey.value = ''
    blankForm()
    dirty.markClean()
    return
  }
  dirty.guardCancel()
}

// closeModal 关掉弹窗并把「刚建出来的明文」清掉，下次打开是干净的表单
function closeModal() {
  modalOpen.value = false
  createdKey.value = ''
}

async function save() {
  // 刚创建完、弹窗还开着（在展示明文）时，确定按钮的语义是「完成」而不是
  // 「再建一把」。原来它一直绑着创建动作，用户看完明文顺手点确定，
  // 就会静默建出第二把一模一样的密钥 —— 而且可以无限点下去。
  if (createdKey.value) {
    closeModal()
    return
  }
  if (!(await validateForm())) return
  saving.value = true
  try {
    // 白名单始终显式发送：空数组是有效值（表示清空），不传则后端保持原值
    const body = {
      name: form.name.trim(),
      rate_limit_rpm: form.rate_limit_rpm,
      allowed_models: cleanList(form.allowed_models),
      allowed_groups: form.allowed_groups
    }
    if (editing.value) {
      await api.put('/keys/' + editing.value.id, { ...body, enabled: form.enabled })
      message.success('已更新')
      closeModal()
      await load()
    } else {
      const res = await api.post<{ key: string }>('/keys', { ...body, enabled: form.enabled })
      // 明文留在弹窗里方便立刻复制走（列表里也随时能看，见 ensureKey）
      createdKey.value = res.key
      await load()
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

async function copyKey() {
  if (await writeClipboard(createdKey.value)) markCopied('created')
  else message.warning('复制失败，请手动选择复制')
}

// ---- 每分钟上限：真正的表单弹窗（2026-10-11 从 modal.confirm 改过来）----
//
// 原来这里是 modal.confirm + h(InputNumber)，有两个绕不过去的坑：
//
//   1. 校验不过时 onOk 无处可去。返回 resolved 会**关掉弹窗**（用户刚敲的数字
//      白填）；返回 rejected 则要读 antd 的实现才知道它「不关闭、按钮复位」，
//      而且它会把拒绝继续抛出去（_util/ActionButton.js:74）—— 控制台每次多一条
//      未处理的 Promise 拒绝。返回一个永不 settle 的 promise 更糟：按钮会
//      永远转圈（loading 只在 then/catch 里复位）。
//   2. 服务端返回的校验错误没地方放，只能弹一条顶部 message，用户看不出
//      是哪一格错了 —— 与别的表单（渠道、分组、密钥新建）的处理方式也不一致。
//
// 换成 a-form 之后：规则写在 rules 里、错误显示在输入框正下方、提交前先
// validate()、服务端错误挂到同一个字段上（:help）。
const limitOpen = ref(false)
const limitRow = ref<APIKey | null>(null)
const limitRpm = ref(0)
const limitSaving = ref(false)
/** 服务端回来的字段级错误（本地规则查不出来的那种，如后端 400 的具体原因） */
const limitError = ref('')
const limitRef = ref<FormInstance | null>(null)

const limitRules: Rule[] = [
  {
    required: true,
    // 用 validator 而不是 type:'number'：这一格允许 -1（完全不限流），
    // 也允许 0（跟随全局默认），范围检查只能自己写
    validator: async (_r, v) => {
      if (v === null || v === undefined || v === '') throw new Error('请填一个数字')
      const n = Number(v)
      if (!Number.isFinite(n)) throw new Error('请填一个数字')
      if (n < -1) throw new Error('最小是 -1（完全不限流）')
      if (n > 1000000) throw new Error('最大是 1000000')
    }
  }
]

function setLimit(row: APIKey) {
  limitRow.value = row
  limitRpm.value = row.rate_limit_rpm
  limitError.value = ''
  limitOpen.value = true
  // 打开后焦点进输入框：这个弹窗唯一的动作就是改一个数字。
  // rAF 是等 antd 把弹窗摆好再聚焦（挂载那一拍元素还在做入场定位）
  requestAnimationFrame(() => {
    ;(limitRef.value as any)?.focus?.('rpm')
  })
}

async function submitLimit() {
  const row = limitRow.value
  if (!row) return
  limitError.value = ''
  try {
    await limitRef.value?.validate()
  } catch {
    return // 规则错误已经显示在字段下方，这里不用再做别的
  }
  limitSaving.value = true
  try {
    await api.put('/keys/' + row.id, { rate_limit_rpm: Number(limitRpm.value) })
    message.success('已更新')
    limitOpen.value = false
    await load()
    // 整表重拉销毁了触发按钮，焦点找回同一行的同一个动作。
    // 前缀必须与模板里那枚按钮的 aria-label **逐字一致** —— 它靠这个字符串
    // 定位（见 utils/focusRow）
    await nextTick()
    focusRowAction(tableWrap.value, row.id, '修改 ' + row.name + ' 的每分钟请求上限')
  } catch (e: any) {
    // 服务端说不行时把原因留在字段下方，而不是弹一条转瞬即逝的顶部提示：
    // 弹窗还开着，用户正好能就地改
    limitError.value = e?.message || '保存失败'
  } finally {
    limitSaving.value = false
  }
}

async function toggle(row: APIKey) {
  // 切换后的动作名会翻（停用 ↔ 启用），焦点归还时要用**新的**那个前缀
  const nextLabel = (!row.enabled ? '停用密钥 ' : '启用密钥 ') + row.name
  // 同一时刻只允许**一行**在途（2026-10-11 修）。原来只挡同一行
  // （`togglingId === row.id`），于是 A 行在途时点 B 行会照常发出第二个 PUT，
  // 两条请求各自 `await load()` —— 先回来的那条被后回来的完整覆盖，
  // 屏幕上于是有一行的启停状态与库里不一致，直到下一次刷新。
  // 渠道页早就用 `:disabled="togglingId !== 0 && togglingId !== record.id"`
  // 挡住了这一路，密钥页当时没跟上；现在两处口径一致：
  // 按钮上 disabled + loading，这里再兜一道。
  if (togglingId.value) {
    if (togglingId.value !== row.id) message.info('上一项操作还在进行中，请稍候')
    return
  }
  togglingId.value = row.id
  try {
    await api.put('/keys/' + row.id, { enabled: !row.enabled })
    await load()
    // 整表重拉销毁了触发按钮，焦点会掉回 body —— 找回同一行的同一个开关
    await nextTick()
    focusRowAction(tableWrap.value, row.id, nextLabel)
  } catch (e: any) {
    message.error(e.message)
  } finally {
    togglingId.value = 0
  }
}

/** 正在切换的那一行（0 = 没有）。用它给开关加 loading 并把重复点击吃掉 */
const togglingId = ref(0)

function confirmDelete(row: APIKey) {
  modal.confirm({
    centered: true,
    title: '确认删除密钥',
    content: '使用该密钥的客户端将立即无法调用。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/keys/' + row.id)
        message.success('已删除')
        await load()
        // 行已不在列表里：焦点交给表格容器，而不是掉回 body
        await nextTick()
        focusRowAction(tableWrap.value, row.id, '删除密钥 ')
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

function fmt(t: string | null) {
  if (!t) return '从未使用'
  // 与全站统一口径（原来 toLocaleString 出来是斜杠分隔、月日不补零）
  return fmtTime(t)
}

onMounted(() => {
  load()
  loadGroups()
  document.addEventListener('visibilitychange', onVisibilityDrop)
})

// 离开这一页也丢掉明文（切路由 / 关标签页）—— 这时缓存与「悬停不重复解密」
// 已经没有关系了，留着只是把机密留在内存里
onUnmounted(() => {
  document.removeEventListener('visibilitychange', onVisibilityDrop)
  dropRevealed()
})
</script>

<template>
  <div class="manage-container">
    <!-- 复制结果的读屏播报（见 script 里的说明）。.sr-only 而不是 display:none：
         后者会让它从无障碍树上消失，aria-live 也就无从播报 -->
    <p class="sr-only" aria-live="polite">{{ copiedText }}</p>
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建密钥</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <!-- 客户端该填的地址放在这里：建完密钥紧接着就是把它填进客户端，
             而这一页原来只在列密钥，地址得去别处找。
             用 <button> 而不是 <span @click>（2026-10-11）：没有 tabindex 的
             span 键盘永远聚焦不到、回车也触发不了，而复制地址是这一页的
             主要动作之一。同仓 ModelWhitelistEditor 早就为同样的场景这么做了。 -->
        <a-tooltip title="点击复制这个地址">
          <button type="button" class="baseurl-pill" @click="copyBaseUrl">
            <span class="baseurl-label">BaseURL</span>
            <span class="baseurl-text">{{ baseUrl }}</span>
            <!-- 复制成功 0.9s 内图标原位变形为绿色对勾（与密钥胶囊同一套反馈） -->
            <CheckOutlined v-if="copiedWhat === 'baseurl'" class="baseurl-copy is-copied" />
            <CopyOutlined v-else class="baseurl-copy" />
          </button>
        </a-tooltip>
        <div class="toolbar-spacer" />
        <!-- 批量启停（2026-10-11）：≥2 行才出现，作用范围是当前筛选结果，
             按钮上写着数量（与渠道页同一套，见 useBatchToggle 的文件头） -->
        <template v-if="filteredKeys.length >= 2">
          <a-button size="small" :loading="batch.running.value" @click="batch.run(filteredKeys, true)">
            <PlayCircleOutlined /> 批量启用{{ filteredKeys.length }} 个
          </a-button>
          <a-button size="small" danger :loading="batch.running.value" @click="confirmBatchDisable">
            <StopOutlined /> 批量停用{{ filteredKeys.length }} 个
          </a-button>
          <span v-if="batch.running.value" class="batch-progress" role="status">
            正在处理 {{ batch.progress.value.done }}/{{ batch.progress.value.total }}
          </span>
        </template>
        <!-- 本地搜索（2026-10-11）：列表接口一次返回全量密钥，几十把之后
             「按名字找那一把」只能靠眼睛扫。搜索是纯客户端的 —— 不额外发请求，
             也不会因为搜不到而让表格进入「加载中」的错觉。
             allow-clear：搜索框有内容时必须能一键清空（否则要按七八次退格）。 -->
        <a-input
          v-model:value="q"
          class="toolbar-search"
          placeholder="搜索名称 / 密钥前缀 / 白名单"
          allow-clear
          :aria-label="'搜索密钥'"
          aria-keyshortcuts="/"
        >
          <template #prefix><SearchOutlined aria-hidden="true" /></template>
        </a-input>
        <span class="toolbar-hint">
          <!-- 搜索时改说「找到 N 个 / 共 M 个」：只显示「共 3 个」会让用户
               以为密钥被删了 -->
          <template v-if="q.trim()">找到 {{ total }} 个 / 共 {{ rows.length }} 个</template>
          <template v-else>共 {{ rows.length }} 个密钥</template>
        </span>
      </div>

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="密钥列表加载失败"
        @retry="load"
      >
      <!-- scroll.x 必须不小于各列宽度之和（名称 150 + 密钥 170 + 模型白名单 160
           + 分组白名单 180 + 最后使用 150 + 限流 130 + 状态 90 + 操作 132 = 1162）：
           声明偏小时右侧固定的「操作」列会盖住左边最后一列，
           表现为表头被截断、单元格内容被压住，而且不报错。
           原来写的是 1170、少了 60（那是四列宽度还没跟着改的时候）。
           操作 200 -> 132 是图标按钮那一次，见下方操作列上方的注释。
           核对脚本：scripts/check-table-widths.mjs -->
      <!-- 表格外层只为一件事存在：给「删掉一行之后焦点往哪儿去」一个落点。
           tabindex="-1" 让它可被程序化聚焦但不进 Tab 序 —— 没有它的话，
           焦点会掉回 body，键盘用户要从页面顶部重新 Tab 一遍（见 utils/focusRow）。 -->
      <div ref="tableWrap" tabindex="-1">
      <a-table :data-source="pagedRows" :loading="loading" :pagination="pagination" row-key="id" size="small" :scroll="{ x: 1162 }">
        <template #emptyText>
          <!-- 空态自带行动按钮：原来只有一句「点『新建密钥』创建第一个」，
               让用户自己去工具栏找那颗按钮（2026-10-10）。
               2026-10-11 补搜索分支：搜索无结果 ≠ 一把密钥都没有，
               后者给「新建」是对的，前者给「新建」是把用户往错方向引 -->
          <EmptyState v-if="q.trim()" size="sm" text="没有匹配的密钥">
            <template #action>
              <a-button size="small" @click="q = ''">清空搜索</a-button>
            </template>
          </EmptyState>
          <EmptyState v-else size="sm" text="还没有密钥">
            <template #action>
              <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建密钥</a-button>
            </template>
          </EmptyState>
        </template>
        <a-table-column title="名称" data-index="name" :width="150" />
        <a-table-column title="密钥" :width="170">
          <template #default="{ record }">
            <!-- 胶囊 + 悬停看全量 + 点击复制：三个动作都指向同一件事
                 「我要把这把密钥拿去用」。明文按需从后端解密，不随列表下发。

                 destroy-tooltip-on-hide（2026-10-11）：antd 默认把提示的 DOM
                 留在页面上（只是隐藏），于是「悬停看过一次」的全量明文会一直
                 躺在 DOM 里 —— 打开 DevTools 就能读到，而屏幕上什么都没有。
                 关掉即销毁之后，明文只在它真的显示时存在于 DOM 中。 -->
            <a-tooltip
              placement="topLeft"
              :title="tooltipOf(record)"
              :destroy-tooltip-on-hide="true"
              @open-change="(open: boolean) => open && ensureKey(record)"
            >
              <button type="button" class="key-pill" @click="copyRowKey(record)">
                <KeyOutlined />
                <span class="key-text">{{ record.key_prefix }}…</span>
                <!-- 复制成功 0.9s 内图标原位变形为绿色对勾：复制发生在
                     这颗胶囊上，反馈也留在这里，不再打断视线去顶部弹消息 -->
                <CheckOutlined v-if="copiedWhat === 'key-' + record.id" class="key-copy is-copied" />
                <CopyOutlined v-else class="key-copy" />
              </button>
            </a-tooltip>
          </template>
        </a-table-column>
        <a-table-column title="模型白名单" :width="160" ellipsis>
          <template #default="{ record }">
            <span v-if="whitelistText(record.allowed_models) === '不限'" class="muted">不限</span>
            <span v-else>{{ whitelistText(record.allowed_models) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="分组白名单" :width="180" ellipsis>
          <template #default="{ record }">
            <span v-if="!(record.allowed_groups || []).length" class="muted">不限</span>
            <span v-else class="group-tag-list">
              <!-- 条目是分组 ID（颜色与名字去分组表里解析），与分组管理、
                   渠道列表用的是同一份颜色。悬空引用（分组已删）单独标出来：
                   那种条目会让这把密钥调用时 403，不能只显示一个裸 ID -->
              <template v-for="info in resolvedGroups(record.allowed_groups)" :key="info.ref">
                <GroupTag v-if="!info.missing" :name="info.label" :color="info.color" />
                <a-tooltip v-else :key="info.ref + '-missing'" :title="'引用的分组已不存在：' + info.ref + '（这会让该密钥调用时被拒，请编辑密钥清掉它）'">
                  <span class="group-ref-missing">{{ info.ref }}</span>
                </a-tooltip>
              </template>
            </span>
          </template>
        </a-table-column>
        <a-table-column title="最后使用" :width="150">
          <template #default="{ record }">{{ fmt(record.last_used_at) }}</template>
        </a-table-column>
        <a-table-column title="限流" :width="130">
          <template #default="{ record }">{{ limitText(record.rate_limit_rpm) }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="90">
          <template #default="{ record }">
            <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
          </template>
        </a-table-column>
        <!-- 四个动作改成图标按钮，与渠道页同一个写法（.table-icon-btn）。
             原来四个「图标 + 文字」链接实测占 181px，列宽因此要 200；
             图标按钮每个 28px，四个加间距共 124px，列宽 132 就够，
             表格总宽从 1230 降到 1162 —— 1440 与 1334 两个视口下都能完整装进
             容器，横向滚动消失，也不再出现「固定列悬停提示被裁掉一截」。

             四个动作里有两个不是自解释的：
               · 「改限额」—— 图标只能是仪表盘/速度一类，看不出改的是「每分钟上限」
               · 「启用/停用」—— 同一个按钮两种语义，图标必须跟着 :checked 换
             这两处靠 tooltip 补名字，其余两个（编辑、删除）本就是通用约定。 -->
        <a-table-column title="操作" :width="132" fixed="right">
          <template #default="{ record }">
            <a-space :size="4">
              <a-tooltip title="编辑密钥：名称、白名单">
                <a-button
                  class="table-icon-btn"
                  type="text"
                  size="small"
                  :aria-label="'编辑密钥 ' + record.name"
                  @click="openEdit(record)"
                >
                  <EditOutlined />
                </a-button>
              </a-tooltip>
              <a-tooltip title="改限额：每分钟请求数上限">
                <a-button
                  class="table-icon-btn"
                  type="text"
                  size="small"
                  :aria-label="'修改 ' + record.name + ' 的每分钟请求上限'"
                  @click="setLimit(record)"
                >
                  <DashboardOutlined />
                </a-button>
              </a-tooltip>
              <a-tooltip :title="record.enabled ? '停用：使用该密钥的客户端立即失效' : '启用：恢复这把密钥的调用'">
                <a-button
                  class="table-icon-btn"
                  type="text"
                  size="small"
                  :loading="togglingId === record.id"
                  :disabled="togglingId !== 0 && togglingId !== record.id"
                  :aria-label="(record.enabled ? '停用密钥 ' : '启用密钥 ') + record.name"
                  @click="toggle(record)"
                >
                  <!-- 图标跟着状态换，不只换颜色：色觉障碍下也要能分辨
                       「这一下会把它关掉」还是「会把它打开」 -->
                  <StopOutlined v-if="record.enabled" />
                  <PlayCircleOutlined v-else />
                </a-button>
              </a-tooltip>
              <a-tooltip title="删除密钥">
                <a-button
                  class="table-icon-btn is-danger"
                  type="text"
                  size="small"
                  :aria-label="'删除密钥 ' + record.name"
                  @click="confirmDelete(record)"
                >
                  <DeleteOutlined />
                </a-button>
              </a-tooltip>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
      </div>
      </DataState>
    </section>

    <a-modal
      v-model:open="modalOpen"
      :title="title"
      :confirm-loading="saving"
      :width="modalWidth(600)"
      :ok-text="createdKey ? '完成' : '确定'"
      :cancel-text="createdKey ? '再建一把' : '取消'"
      centered
      :mask-closable="false"
      @ok="save"
      @cancel="onCancel"
    >
      <a-form ref="formRef" :model="form" :rules="formRules" layout="vertical">
        <a-form-item label="名称" name="name">
          <a-input v-model:value="form.name" placeholder="例如 本地客户端" />
        </a-form-item>
        <a-form-item label="每分钟请求上限">
          <a-input-number v-model:value="form.rate_limit_rpm" :min="-1" :max="1000000" style="width: 100%" />
          <div class="field-hint">0 表示跟随全局默认；负数表示这把密钥完全不限流。</div>
        </a-form-item>
        <a-form-item label="允许调用的模型">
          <a-select
            v-model:value="form.allowed_models"
            mode="tags"
            :token-separators="[',', '，']"
            placeholder="输入模型名后回车，可填多个"
          />
          <div class="field-hint">留空表示不限制；填了则只有列表内的模型可以被这把密钥调用。</div>
        </a-form-item>
        <a-form-item label="允许使用的分组">
          <!-- 受控多选而不是自由输入的 tags：值是分组 ID，选项就是现有分组。
               以前存名字 + 允许任意输入，打错的名字静默入库，分组一改名
               还会把引用它的密钥全体打断 —— 现在保存前就在这份选项里选 -->
          <a-select
            v-model:value="form.allowed_groups"
            mode="multiple"
            placeholder="选择允许使用的分组"
          >
            <a-select-option v-for="g in groups" :key="g.id" :value="String(g.id)">{{ g.name }}</a-select-option>
          </a-select>
          <div class="field-hint">留空表示不限制。按分组记录、不受分组改名影响；引用的分组被删除时调用会被拒绝并提示。</div>
        </a-form-item>
        <a-form-item label="启用">
          <a-switch v-model:checked="form.enabled" />
        </a-form-item>
      </a-form>

      <a-alert
        v-if="createdKey"
        type="success"
        show-icon
        message="密钥创建成功"
        description="密钥已保存。以后随时能在列表里悬停查看、点击复制，这里也可以直接复制走。"
        style="margin-top: 8px"
      />
      <div v-if="createdKey" class="key-box">
        <code>{{ createdKey }}</code>
        <a-button size="small" @click="copyKey">
          <CheckOutlined v-if="copiedWhat === 'created'" class="btn-copy-ok" />
          <CopyOutlined v-else />
          {{ copiedWhat === 'created' ? '已复制' : '复制' }}
        </a-button>
      </div>
    </a-modal>

    <!-- 每分钟上限（2026-10-11 从 modal.confirm 改过来，理由见 setLimit 上方）：
         规则错误显示在输入框正下方、服务端错误挂在同一个字段上，
         校验不过时弹窗**不关**，用户原地改一个数就能重试 -->
    <a-modal
      v-model:open="limitOpen"
      :title="'设置每分钟请求上限 · ' + (limitRow?.name || '')"
      :width="modalWidth(400)"
      :confirm-loading="limitSaving"
      :mask-closable="false"
      ok-text="保存"
      @ok="submitLimit"
    >
      <a-form ref="limitRef" layout="vertical" style="margin-top: 12px">
        <a-form-item
          label="每分钟请求上限"
          name="rpm"
          :rules="limitRules"
          :validate-status="limitError ? 'error' : undefined"
          :help="limitError || undefined"
        >
          <a-input-number v-model:value="limitRpm" :min="-1" :max="1000000" style="width: 100%" />
        </a-form-item>
      </a-form>
      <div class="field-hint">
        填 0 表示跟随全局默认；填 -1 表示这把密钥完全不限流（适合本地压测）。
      </div>
    </a-modal>
  </div>
</template>

<style scoped>
.manage-container { padding: var(--gap); }
.manage-panel { padding: 0; overflow: hidden; }
.manage-toolbar { display: flex; align-items: center; gap: var(--gap); padding: var(--gap); min-height: 64px; }
.field-hint { margin-top: 4px; font-size: var(--font-size-sm); color: var(--color-text-secondary); }
.toolbar-left { display: flex; gap: var(--gap); }
.toolbar-spacer { flex: 1; }
/* 批量处理进度：见渠道页同名前缀的说明（它是一个过程状态，不该抢注意力） */
.batch-progress { color: var(--color-text-secondary); font-size: var(--font-size-sm); }
.toolbar-hint { color: var(--color-text-secondary); font-size: var(--font-size-md); }
/* BaseURL 胶囊：与密钥胶囊同一套视觉语言，但走中性色 —— 它只是陈列一条
   可以复制走的地址，用主色会和旁边的「新建密钥」抢注意力。
   它是 <button>（键盘可达，见模板注释），所以浏览器默认的边框/底色/字体
   要显式写回来 —— 下面这几行就是原来 <span> 时的取值，观感一字未变。 */
.baseurl-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  max-width: 340px;
  padding: 3px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-bg);
  color: inherit;
  font-family: inherit;
  font-size: var(--font-size-sm);
  line-height: 20px;
  text-align: left;
  cursor: var(--cursor-hand);
  transition: border-color 0.2s ease;
}
.baseurl-pill:hover { border-color: var(--color-primary); }
.baseurl-label { flex: none; color: var(--color-text-secondary); }
/* 地址是代码类内容，等宽便于逐段核对；太长时省略号收尾，不撑破工具栏 */
.baseurl-text {
  font-family: var(--font-family-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.baseurl-copy { flex: none; opacity: 0.55; transition: opacity 0.2s ease; }
.baseurl-pill:hover .baseurl-copy { opacity: 1; color: var(--text-primary-ink); }
/* ---- 复制成功的原位变形（三处复制共用这套观感）----
   图标换成绿色对勾并做一次小弹出；颜色用 --text-green（正文级语义色，
   白底 5.13:1 / 暗色卡片 6.22:1），成功也是要被读到的一档信息。
   opacity/color 带 !important 是刻意的：胶囊上的对勾平时 opacity:0
   （悬停才显示）、悬停时还会染成主色，而复制刚发生时用户已点完、
   指针多半已移开，反馈必须此刻可见、且必须是成功色 —— 普通权重压不过
   那两条悬停规则。keyframes（copy-pop）在 theme.css 全局层，与日志详情共用 */
.is-copied {
  color: var(--text-green) !important;
  opacity: 1 !important;
  animation: copy-pop 0.22s cubic-bezier(0.2, 0, 0, 1);
}
.btn-copy-ok { color: var(--text-green); animation: copy-pop 0.22s cubic-bezier(0.2, 0, 0, 1); }
.muted { color: var(--color-text-secondary); }
/* 密钥胶囊：与分组胶囊同一套视觉语言（浅底 + 圆角 + 同色文字），
   但用等宽字体 —— 密钥是代码类内容，逐字符比对时等宽好读得多。
   它是 <button>（键盘可达，见模板注释）：边框/字体/文字色显式写回，
   取值就是原来 <span> 时的那些，观感一字未变。 */
.key-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  padding: 1px 8px;
  border: none;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-primary) 13%, transparent);
  /* 13% 主色底上主色文字只有 2.89:1；ink 版 5.17:1。
     底色仍留在主色系里，观感不变。 */
  color: var(--text-primary-ink);
  font-family: var(--font-family-mono);
  font-size: var(--font-size-sm);
  line-height: 18px;
  text-align: left;
  cursor: var(--cursor-hand);
  transition: background 0.2s ease;
}
.key-pill:hover { background: color-mix(in oklab, var(--color-primary) 22%, transparent); }
.key-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.key-copy { opacity: 0; transition: opacity 0.2s ease; }
.key-pill:hover .key-copy { opacity: 0.75; }

/* 触屏上没有 hover，而复制图标平时是 opacity:0 —— 手机上用户根本看不出
   这颗胶囊可以点（2026-10-11）。粗指针设备下让它常显，代价是胶囊略挤，
   收益是「这里能复制」这件事第一次变得可见。 */
@media (pointer: coarse) {
  .key-copy { opacity: 0.75; }
}

.group-tag-list { display: inline-flex; flex-wrap: wrap; gap: 4px; }
/* 悬空的分组引用：分组已删、条目还在。用红字虚线框与正常胶囊区分 ——
   它不是一种正常的白名单状态，留着它这把密钥调用时会被整体拒绝。

   文字色用 --text-red（5.44:1）而不是 --color-red：#ea4343 压白卡只有
   3.90:1，12px 正文不达 AA。虚线框仍用 --color-red（边框属非文本，
   3.90:1 过 1.4.11 的 3:1），顺带去掉了 antd 旧红兜底值 —— 本站
   所有主题块都定义了 --color-red，兜底只会让人以为它可以不存在。 */
.group-ref-missing {
  display: inline-block;
  padding: 1px 8px;
  border: 1px dashed var(--color-red);
  border-radius: var(--radius-control);
  color: var(--text-red);
  font-size: var(--font-size-sm);
  line-height: 20px;
  cursor: help;
}
.key-box {
  display: flex;
  align-items: center;
  gap: var(--gap);
  margin-top: 8px;
  padding: 8px;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-control);
  word-break: break-all;
}
.key-box code { flex: 1; font-family: var(--font-family-mono); font-size: var(--font-size-sm); }
</style>

