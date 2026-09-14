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
}
</script>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { message } from 'ant-design-vue'
import { PlusOutlined, DeleteOutlined, SnippetsOutlined, DollarOutlined } from '@ant-design/icons-vue'
import ModelPricingEditor, { emptyPrice, hasPrice, priceSummary } from './ModelPricingEditor.vue'

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
  update([...props.items, { public_name: '', upstream_name: '', enabled: true, ...emptyPrice() }])
}

function removeRow(index: number) {
  const next = [...props.items]
  next.splice(index, 1)
  update(next)
}

function setField(index: number, field: 'public_name' | 'upstream_name', value: string) {
  const next = props.items.map((row, i) => (i === index ? { ...row, [field]: value } : row))
  update(next)
}

function setProxy(index: number, value: number) {
  const next = props.items.map((row, i) => (i === index ? { ...row, proxy_id: value } : row))
  update(next)
}

const proxyOptions = computed(() => [
  { value: 0, label: '跟随渠道' },
  ...(props.proxies || []).map((p) => ({ value: p.id, label: p.name + (p.enabled ? '' : '（已停用）') }))
])

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
      enabled: true
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
      <a-button size="small" @click="bulkOpen = true"><SnippetsOutlined /> 批量粘贴</a-button>
      <!-- 条数常驻显示：批量粘贴会一次加几十条，只靠一闪而过的提示
           没法确认到底进来了几条 -->
      <span v-if="items.length" class="wl-count">共 {{ items.length }} 条</span>
    </div>

    <ModelPricingEditor
      :currency="currency"
      v-model:open="priceOpen"
      :model-name="priceRow?.public_name || ''"
      :value="priceRow"
      @save="savePrice"
    />

    <a-modal v-model:open="bulkOpen" title="批量粘贴模型清单" width="560px" centered @ok="applyBulk">
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
  --wl-cols: minmax(0, 1fr) minmax(0, 1fr) 124px 42px 26px;
  --wl-gap: 6px;
  display: flex;
  flex-direction: column;
  gap: 0;
}
.wl-editor.has-proxy {
  --wl-cols: minmax(0, 1fr) minmax(0, 1fr) 104px 124px 42px 26px;
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

.wl-head {
  padding-bottom: 6px;
  border-bottom: 1px solid var(--color-border);
  font-size: 12px;
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
  font-size: 12px;
  cursor: pointer;
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
  cursor: pointer;
  transition: background 0.15s var(--ease-expo);
}
.wl-del:hover { background: color-mix(in oklab, var(--color-red) 14%, transparent); }

/* 键盘走到这里要看得见焦点：胶囊与删除都是图标动作，没有焦点环
   等于键盘用户不知道自己停在哪儿 */
.price-pill:focus-visible,
.wl-del:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 1px;
}

.wl-empty {
  padding: 12px;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-control);
  color: var(--color-text-secondary);
  font-size: 13px;
  line-height: 1.6;
}
.wl-actions { display: flex; align-items: center; gap: 8px; margin-top: 12px; }
/* 条数靠右：操作按钮在左、计数在右，视线不用来回跳 */
.wl-count { margin-left: auto; font-size: 12px; color: var(--color-text-secondary); }
.field-hint { font-size: 12px; color: var(--color-text-secondary); }
</style>
