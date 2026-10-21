<script lang="ts">
import type { PriceConfig } from './ModelPricingEditor.vue'

// WhitelistRow 是渠道模型白名单里的一行：客户端请求 public_name，
// 转发时替换成 upstream_name（留空则同名），价格也挂在这一行上 ——
// 同一个模型名在不同渠道成本不同，价格只有跟着渠道走才对得上账。
// 价格字段用 Partial：老数据（价格功能上线前建的渠道）与「还没配价」的行
// 都不带这些字段，强制必需会让「从后端读回来的白名单」过不了类型检查
export interface WhitelistRow extends Partial<PriceConfig> {
  public_name: string
  upstream_name: string
  enabled: boolean
  /** 这一个模型走哪个代理；0 / 不填 = 跟随渠道 */
  proxy_id?: number
  /**
   * 这个模型对外声明支持的思考档位（/v1/models 的 supported_efforts）。
   * 空 / 不填 = 没配，读侧按渠道协议推导（见 relay.SupportedEfforts）——
   * 推导出来的那份不写回这里：它是随协议变化的结果，固化下来就成了两份真相。
   */
  supported_efforts?: string[]
}
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { message } from '@/utils/feedback'
import { PlusOutlined, DeleteOutlined, SnippetsOutlined, DollarOutlined, UndoOutlined } from '@ant-design/icons-vue'
import { modalWidth } from '@/utils/modalWidth'
import { useDirtyClose } from '@/composables/useDirtyClose'
import ModelPricingEditor, { emptyPrice, hasPrice, priceSummary } from './ModelPricingEditor.vue'
import { effortOptions, normalizeEfforts } from './efforts'

// 这是纯展示型编辑器：数据的保存方式由父组件决定 ——
// 建/改渠道时随渠道一起提交，抽屉里则单独整表提交。
// 白名单是模型在系统里的唯一登记处，所以这里不做「先建模型再绑定」的两步操作。
const props = defineProps<{
  items: WhitelistRow[]
  /** 可选：传了才显示「代理」列。没有代理可选的场景不必多一列空下拉 */
  proxies?: { id: number; name: string; enabled: boolean }[]
  /** 所属渠道的记账币种（CNY / USD）：单价的单位与摘要都按它显示 */
  currency?: string
}>()
const emit = defineEmits<{ (e: 'update:items', v: WhitelistRow[]): void }>()

const bulkOpen = ref(false)
const bulkText = ref('')

function update(next: WhitelistRow[]) {
  emit('update:items', next)
}

// 定价弹窗：一次只开一行，记录是哪一行
const priceOpen = ref(false)
const priceIndex = ref(-1)
const priceRow = computed(() => (priceIndex.value >= 0 ? props.items[priceIndex.value] : null))

function openPrice(index: number) {
  const row = props.items[index]
  if (!row.public_name.trim()) {
    message.warning('先填上对外模型名，再配这个模型的价格')
    return
  }
  priceIndex.value = index
  priceOpen.value = true
}

function savePrice(cfg: PriceConfig) {
  const next = props.items.map((row, i) => (i === priceIndex.value ? { ...row, ...cfg } : row))
  update(next)
}

function addRow() {
  update([
    ...props.items,
    { public_name: '', upstream_name: '', enabled: true, supported_efforts: [], ...emptyPrice() }
  ])
}

/**
 * 删掉一行 + 3 秒内可撤销（2026-10-11）。
 *
 * 为什么加撤销而不是加确认框：这一行的内容可能包含刚手工录入的一整组单价
 * （输入/输出/缓存读/缓存写四个数字，还能带按时段倍率），删掉之后**没有任何
 * 地方能找回** —— 只能凭记忆重填。而一个「确认删除」弹窗在这种连续录入的
 * 场景里很快就变成无脑回车（用户会条件反射地按下确定），既不防错也打断节奏。
 * 3 秒撤销的窗口足够覆盖「手滑点错」，又不会让错误一直挂在那里。
 *
 * 撤销恢复的是**整行快照**（含单价、时段倍率、代理），并且按原下标插回 ——
 * 白名单的顺序就是列表顺序，用户按那个顺序读它。
 */
function removeRow(index: number) {
  const removed = props.items[index]
  if (!removed) return
  const next = [...props.items]
  next.splice(index, 1)
  update(next)
  // 同一时刻只留一个撤销入口：连删两行时，第一行的撤销很快就会被第二行顶掉，
  // 这是有意的 —— 保留一摞撤销栈会让「撤销」这个按钮的语义变得不确定
  undoRow.value = { index, row: removed }
  if (undoTimer) window.clearTimeout(undoTimer)
  undoTimer = window.setTimeout(() => {
    undoRow.value = null
    undoTimer = 0
  }, UNDO_MS)
}

/** 撤销入口的显示状态；null = 当前没有可撤销的删除 */
const undoRow = ref<{ index: number; row: WhitelistRow } | null>(null)
let undoTimer = 0
const UNDO_MS = 3000

function undoRemove() {
  const u = undoRow.value
  if (!u) return
  const next = [...props.items]
  // 原下标可能已经越界（期间又删了几行），夹回合法范围而不是丢弃
  next.splice(Math.min(u.index, next.length), 0, u.row)
  update(next)
  undoRow.value = null
  if (undoTimer) {
    window.clearTimeout(undoTimer)
    undoTimer = 0
  }
}

// 组件卸载时清掉定时器：它回调里会写 undoRow，卸载后写一个已经没人看的 ref
// 不会报错，但留着定时器没有意义
onBeforeUnmount(() => {
  if (undoTimer) window.clearTimeout(undoTimer)
})

function setField(index: number, field: 'public_name' | 'upstream_name', value: string) {
  const next = props.items.map((row, i) => (i === index ? { ...row, [field]: value } : row))
  update(next)
}

function setProxy(index: number, value: number) {
  const next = props.items.map((row, i) => (i === index ? { ...row, proxy_id: value } : row))
  update(next)
}

// 档位：空数组的语义是「没配」，落库时后端归一成 NULL（读侧按渠道协议推导）。
// 这里不做「全选时写成空」之类的折叠 —— 用户勾了哪几档就是哪几档，
// 「回落到推导」只能由他主动清空表达。
function setEfforts(index: number, value: string[]) {
  const next = props.items.map((row, i) =>
    (i === index ? { ...row, supported_efforts: normalizeEfforts(value) } : row)
  )
  update(next)
}

const proxyOptions = computed(() => [
  { value: 0, label: '跟随渠道' },
  ...(props.proxies || []).map((p) => ({ value: p.id, label: p.name + (p.enabled ? '' : '（已停用）') }))
])

// 档位下拉的候选：八档全出，外加这一行声明里词表之外的条目
// （从备份导入或后端新增档位时会遇到，丢掉它们等于静默改写用户的配置）
function rowEffortOptions(row: WhitelistRow) {
  return effortOptions(row.supported_efforts)
}

// 档位格的悬停说明：只写「这一格是什么」，不重复提示里的推导规则
function effortTip(row: WhitelistRow) {
  const n = normalizeEfforts(row.supported_efforts).length
  return n
    ? `已声明 ${n} 档：${normalizeEfforts(row.supported_efforts).join(' / ')}`
    : '留空 = 按渠道协议自动推导（Anthropic / Gemini 八档全在，OpenAI 兼容上游只列 low 以上）'
}

// 价格胶囊的悬停说明：胶囊本身宽度有限（列宽固定），带时段规则的摘要
// 会被省略号截掉，完整内容在这里给出，同时说清点下去会发生什么
function priceTip(row: WhitelistRow) {
  return hasPrice(row)
    ? '单价 ' + priceSummary(row, props.currency) + '（每百万词元）· 点击修改'
    : '还没配价：这条模型的调用会被记成 0 元 · 点击配价'
}

function toggleRow(index: number, value: boolean) {
  const next = props.items.map((row, i) => (i === index ? { ...row, enabled: value } : row))
  update(next)
}

// 批量粘贴：一行一个模型，支持「对外名」或「对外名=上游名」，
// 也接受逗号分隔。重复的对外名直接跳过并告知条数 ——
// 中转站抄上游模型清单时动辄几十行，逐条录入不现实。
// 防误关：批量粘贴框里可能已经贴了几十行模型清单，误触遮罩丢掉同样心疼
const bulkDirty = useDirtyClose({
  open: bulkOpen,
  snapshot: () => bulkText.value,
  onDiscard: () => {
    bulkText.value = ''
  }
})

function openBulk() {
  bulkText.value = ''
  bulkOpen.value = true
  bulkDirty.markClean()
}

function applyBulk() {
  const lines = bulkText.value
    .split(/[\n,，;；]/)
    .map((s) => s.trim())
    .filter(Boolean)
  if (!lines.length) {
    message.warning('没有可导入的内容')
    return
  }
  const next = [...props.items]
  const seen = new Set(next.map((r) => r.public_name.trim()))
  let added = 0
  let skipped = 0
  for (const line of lines) {
    const [rawName, rawUpstream] = line.split('=').map((s) => (s || '').trim())
    if (!rawName) continue
    if (seen.has(rawName)) {
      skipped++
      continue
    }
    seen.add(rawName)
    next.push({
      public_name: rawName,
      upstream_name: rawUpstream || '',
      enabled: true,
      // 批量粘贴只带模型名：档位留空，按渠道协议推导 ——
      // 这里凭空填一套会让「导入完就锁定了推理档位」变成隐形默认值
      supported_efforts: []
    })
    added++
  }
  update(next)
  bulkText.value = ''
  bulkOpen.value = false
  message.success(`已添加 ${added} 条${skipped ? `，跳过重复 ${skipped} 条` : ''}`)
}
</script>

<template>
  <div class="wl-editor" :class="{ 'has-proxy': !!proxies }">
    <!-- 表头标签只写短名：完整解释在 title 与父级的说明里。
         原来写成「对外模型名（客户端请求用）」，在 190px 的列里会折成两行，
         而折行位置随列宽变化，看起来像串行 -->
    <div v-if="items.length" class="wl-head">
      <span title="客户端请求时用的名字。写错这里是最常见的 502 原因。">对外模型名</span>
      <span title="转发给上游时替换成的名字；留空表示与对外名相同。">上游模型名</span>
      <span v-if="proxies" title="这一个模型单独走哪个出口；默认跟随渠道。">代理</span>
      <span title="这个模型支持的思考档位（/v1/models 的 supported_efforts）；留空按渠道协议推导。">档位</span>
      <span title="单价（每百万词元）；点右边的胶囊可改。">定价</span>
      <span class="wl-center">启用</span>
      <span></span>
    </div>
    <div v-for="(row, index) in items" :key="index" class="wl-row">
      <a-input
        size="small"
        :value="row.public_name"
        placeholder="deepseek-chat"
        :aria-label="`第 ${index + 1} 行的对外模型名`"
        @update:value="(v: string) => setField(index, 'public_name', v)"
      />
      <a-input
        size="small"
        :value="row.upstream_name"
        :placeholder="row.public_name || '同上'"
        :aria-label="`第 ${index + 1} 行的上游模型名，留空表示与对外名相同`"
        @update:value="(v: string) => setField(index, 'upstream_name', v)"
      />
      <a-select
        v-if="proxies"
        size="small"
        :value="row.proxy_id || 0"
        :options="proxyOptions"
        :aria-label="`第 ${index + 1} 行使用的代理`"
        @change="(v: any) => setProxy(index, Number(v) || 0)"
      />
      <!-- 档位：多选而不是自由输入。候选是固定词表，允许用户敲出词表外的值
           只会造出「后端归一后丢掉」的静默偏差（见 efforts.ts 的说明）。
           高度与同排控件对齐：antd 在多选模式下会把高度撑到内容，这里用
           .wl-efforts 把内边距收住，一行里不至于比输入框高出一截。
           不加悬停提示：下拉展开时 tooltip 会压住选项列表（与 ColumnSettings
           的触发按钮同一类坑，那边因此改用原生 title） -->
      <a-select
        class="wl-efforts"
        size="small"
        mode="multiple"
        :value="normalizeEfforts(row.supported_efforts)"
        :options="rowEffortOptions(row)"
        :max-tag-count="1"
        :max-tag-placeholder="(omitted: any[]) => '+' + omitted.length"
        :allow-clear="true"
        :title="effortTip(row)"
        :aria-label="`第 ${index + 1} 行支持的思考档位，留空按协议推导`"
        @change="(v: any) => setEfforts(index, v || [])"
      />
      <span class="wl-price">
        <!-- 定价做成弹窗而不是行内输入：四个单价 + 倍率 + 时段规则塞进一行
             会把这张表挤到没法看，而配价是低频动作 -->
        <a-tooltip :title="priceTip(row)">
          <!-- 用 button 而不是 span：它能被 Tab 聚焦、回车触发，
               图标动作只靠鼠标点击对键盘用户等于不存在 -->
          <button
            type="button"
            class="price-pill"
            :class="{ unset: !hasPrice(row) }"
            @click="openPrice(index)"
          >
            <DollarOutlined />
            <span class="price-text">{{ priceSummary(row, currency) }}</span>
          </button>
        </a-tooltip>
      </span>
      <span class="wl-center">
        <a-tooltip :title="row.enabled ? '停用后这条模型不再被路由' : '启用后这条模型才会被路由'">
          <a-switch
            :checked="row.enabled"
            size="small"
            :aria-label="`第 ${index + 1} 行是否启用`"
            @change="(v: any) => toggleRow(index, !!v)"
          />
        </a-tooltip>
      </span>
      <span class="wl-center">
        <button
          type="button"
          class="wl-del"
          :aria-label="`删除第 ${index + 1} 行${row.public_name ? ' ' + row.public_name : ''}`"
          title="删除这一条"
          @click="removeRow(index)"
        >
          <DeleteOutlined />
        </button>
      </span>
    </div>
    <div v-if="!items.length" class="wl-empty">
      还没有模型：填上这条渠道能跑的模型名，请求才能路由到它。
    </div>
    <div class="wl-actions">
      <a-button size="small" @click="addRow"><PlusOutlined /> 添加一行</a-button>
      <a-button size="small" @click="openBulk"><SnippetsOutlined /> 批量粘贴</a-button>
      <!-- 条数常驻显示：批量粘贴会一次加几十条，只靠一闪而过的提示
           没法确认到底进来了几条 -->
      <span v-if="items.length" class="wl-count">共 {{ items.length }} 条</span>
      <!-- 撤销入口（2026-10-11）：3 秒窗口，就地出现在动作按钮旁边 ——
           删除按钮在表格行的最右侧，撤销入口跟在同一块区域，
           手不需要在屏幕上跳。role=status 让读屏也听得到「刚删了一条」 -->
      <button v-if="undoRow" type="button" class="wl-undo" @click="undoRemove">
        <UndoOutlined aria-hidden="true" />
        已删除「{{ undoRow.row.public_name || '未命名' }}」，撤销
      </button>
    </div>

    <ModelPricingEditor
      :currency="currency"
      v-model:open="priceOpen"
      :model-name="priceRow?.public_name || ''"
      :value="priceRow"
      @save="savePrice"
    />

    <a-modal v-model:open="bulkOpen" title="批量粘贴模型清单" :width="modalWidth(560)" centered :mask-closable="false" @cancel="bulkDirty.guardCancel" @ok="applyBulk">
      <div class="field-hint" style="margin-bottom: 8px">
        一行一个模型。只写模型名表示「上游同名」；用 <code>对外名=上游名</code> 可以做映射。
        重复的对外名会自动跳过。
      </div>
      <a-textarea
        v-model:value="bulkText"
        :rows="8"
        placeholder="deepseek-chat&#10;deepseek-reasoner=deepseek-reasoner-0813&#10;gpt-4o=openai/gpt-4o"
      />
    </a-modal>
  </div>
</template>

<style scoped>
/* 列宽模板只写在这里一处，表头与数据行共用同一份。
   原来两边各自用 flex 定义列宽：表头是文字、数据行是输入框，两者的
   内容最小宽度不同，于是同一「列」在两行里宽度不一样 —— 实测「启用」
   表头和下面的开关错开了 100 多像素，看起来像串了行。
   用 minmax(0, 1fr) 而不是 1fr：默认的 min-width:auto 会让内容把列撑开，
   窄容器里就会溢出。 */
.wl-editor {
  --wl-cols: minmax(0, 1fr) minmax(0, 1fr) 124px 124px 42px 26px;
  --wl-gap: 6px;
  display: flex;
  flex-direction: column;
  gap: 0;
}
.wl-editor.has-proxy {
  --wl-cols: minmax(0, 1fr) minmax(0, 1fr) 104px 124px 124px 42px 26px;
}

.wl-head,
.wl-row {
  display: grid;
  grid-template-columns: var(--wl-cols);
  gap: var(--wl-gap);
  align-items: center;
  /* 左右内边距必须两边都写、且数值一致：列位置由内容盒起点决定，
     只给其中一边加就会差这几个像素，两边的列又对不齐。
     用负外边距去补更糟 —— 行会比容器宽 8px，实测编辑器
     scrollWidth 比 clientWidth 大 8px，窄容器里就是一条横向滚动条 */
  padding-left: 4px;
  padding-right: 4px;
}

/* 撤销入口：与「添加一行」那一排同高，但走警示色（--text-amber）——
   它是一个**限时的**动作，颜色让人在它还亮着的时候多看一眼。
   border-bottom 下划线而不是整块底色：这一条不该抢走表格的注意力。 */
.wl-undo {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-left: auto;
  padding: 2px 6px;
  border: none;
  border-bottom: 1px dashed var(--text-amber);
  background: none;
  color: var(--text-amber);
  font: inherit;
  font-size: var(--font-size-sm);
  cursor: var(--cursor-hand);
}

.wl-undo:hover {
  opacity: 0.8;
}

.wl-head {  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border);
  font-size: var(--font-size-sm);
  color: var(--color-text-secondary);
}

.wl-row {
  /* 上下 3px：24px 的控件 + 6px 行距，一屏能看下十几二十行 */
  padding-top: 3px;
  padding-bottom: 3px;
  border-radius: var(--radius-control);
  transition: background 0.15s var(--ease-expo);
}
.wl-row + .wl-row {
  /* 行间分隔线用 inset 阴影而不是 border：border 会把行撑高 1px，
     几十行下来行高就不齐了 */
  box-shadow: inset 0 1px 0 var(--color-border);
}
.wl-row:hover {
  background: color-mix(in oklab, var(--color-text-secondary) 8%, transparent);
}
/* 悬停时把分隔线让开，免得底色上还压着一条灰线 */
.wl-row:hover,
.wl-row:hover + .wl-row {
  box-shadow: none;
}

.wl-center { display: flex; justify-content: center; }

/* 档位下拉：多选模式默认会随内容换行、把行撑高，这里把它压成单行控件高度，
   与同一行的输入框、下拉、开关齐平（同 .price-pill 的高度处理）。
   溢出用省略号而不是换行 —— 一行白名单的高度必须稳定，否则几十行下来
   每一行都不一样高，横向看过去全是错位。 */
.wl-efforts {
  width: 100%;
  /* 默认 min-height 是多选模式的 auto，会把行撑到 32px+；钉成与
     --size-control-height（24px）一致 */
  min-height: var(--size-control-height);
}
.wl-efforts :deep(.ant-select-selector) {
  min-height: var(--size-control-height);
  padding-top: 0;
  padding-bottom: 0;
  align-items: center;
}
.wl-efforts :deep(.ant-select-selection-overflow) {
  flex-wrap: nowrap;
  overflow: hidden;
}
/* 选中项标签收窄：124px 的格里放得下「xhigh」这种短词，多选的其余档位
   收成「+N」（max-tag-placeholder 里只给计数，antd 默认的「+ N ...」在
   这么窄的格里会把省略号也画出来，读起来像被截断了）。
   注意不能把整个格压得更窄 —— 它是这一行唯一能表达思考能力的控件，
   被压成「+3」之后就看不见到底选了哪几档了（悬停有完整说明，
   但那要求把鼠标停在上面，而滑过长表的动作不会停）。 */
.wl-efforts :deep(.ant-select-selection-item) {
  font-size: var(--font-size-sm);
  line-height: 18px;
  margin-inline-end: 2px;
}
.wl-efforts :deep(.ant-select-selection-item-content) { overflow: hidden; text-overflow: ellipsis; }
/* 多选模式下清空按钮与标签挤在同一行，右内边距留出来 */
.wl-efforts :deep(.ant-select-clear) { right: 22px; }

/* 价格胶囊：与密钥胶囊同一套视觉语言 —— 一眼能看出「这条配过价没有」。
   高度对齐 --size-control-height，和同一行的输入框、下拉、开关齐平
   （原来是默认高度的输入框配 small 下拉，一行里两种高度） */
.price-pill {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 100%;
  height: var(--size-control-height);
  padding: 0 8px;
  border: none;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-primary) 13%, transparent);
  /* 13% 主色底上主色文字只有 2.89:1；ink 版 5.17:1 */
  color: var(--text-primary-ink);
  font-family: var(--font-family-mono);
  font-size: var(--font-size-sm);
  cursor: var(--cursor-hand);
}
.price-pill:hover { background: color-mix(in oklab, var(--color-primary) 22%, transparent); }
/* 未定价用灰底而不是主题色：提示语是「这里缺东西」，不是「这里能点」 */
.price-pill.unset {
  background: color-mix(in oklab, var(--color-text-secondary) 12%, transparent);
  color: var(--color-text-secondary);
}
.price-text { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 删除：图标按钮要有自己的点击区域（原来是一个 14px 的图标，紧贴着开关）。
   保持红色 —— 全站「删除」都是红的，这里不另立一套 */
.wl-del {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: var(--size-control-height);
  height: var(--size-control-height);
  border: none;
  border-radius: var(--radius-control);
  background: transparent;
  /* 删除图标是唯一的功能提示，用 --text-red（白底 5.44:1）而不是
     --color-red（3.90:1）—— 图标本身适用 3:1，但它同时承担了
     「这是删除」的语义，深色主题下 3.38:1 也不够看清 */
  color: var(--text-red);
  cursor: var(--cursor-hand);
  transition: background 0.15s var(--ease-expo);
}
.wl-del:hover { background: color-mix(in oklab, var(--color-red) 14%, transparent); }

/* 键盘走到这里要看得见焦点：胶囊与删除都是图标动作，没有焦点环
   等于键盘用户不知道自己停在哪儿。
   焦点环的样式不在这里写 —— theme.css 有一条全站 :focus-visible
   （2026-09-24 UI 审评 P1-7：此前四处各写一份，颜色与偏移各不相同）。 */

.wl-empty {
  padding: 12px;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-control);
  color: var(--color-text-secondary);
  font-size: var(--font-size-md);
  line-height: 1.6;
}
.wl-actions { display: flex; align-items: center; gap: 8px; margin-top: 12px; }
/* 条数靠右：操作按钮在左、计数在右，视线不用来回跳 */
.wl-count { margin-left: auto; font-size: var(--font-size-sm); color: var(--color-text-secondary); }
.field-hint { font-size: var(--font-size-sm); color: var(--color-text-secondary); }
</style>
