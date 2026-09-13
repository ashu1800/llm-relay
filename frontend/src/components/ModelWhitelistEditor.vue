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
  <div class="wl-editor">
    <div v-if="items.length" class="wl-head">
      <span class="wl-col-name">对外模型名（客户端请求用）</span>
      <span class="wl-col-up">模型映射（转发时替换成）</span>
      <span v-if="proxies" class="wl-col-proxy">代理</span>
      <span class="wl-col-price">定价</span>
      <span class="wl-col-on">启用</span>
      <span class="wl-col-op"></span>
    </div>
    <div v-for="(row, index) in items" :key="index" class="wl-row">
      <a-input
        class="wl-col-name"
        :value="row.public_name"
        placeholder="deepseek-chat"
        @update:value="(v: string) => setField(index, 'public_name', v)"
      />
      <a-input
        class="wl-col-up"
        :value="row.upstream_name"
        :placeholder="row.public_name || '同上'"
        @update:value="(v: string) => setField(index, 'upstream_name', v)"
      />
      <a-select
        v-if="proxies"
        class="wl-col-proxy"
        size="small"
        :value="row.proxy_id || 0"
        :options="proxyOptions"
        @change="(v: any) => setProxy(index, Number(v) || 0)"
      />
      <span class="wl-col-price">
        <!-- 定价做成弹窗而不是行内输入：四个单价 + 倍率 + 时段规则塞进一行
             会把这张表挤到没法看，而配价是低频动作 -->
        <a-tooltip :title="hasPrice(row) ? '点击修改这条模型的价格' : '这条模型还没配价，调用会被记成 0 元'">
          <span class="price-pill" :class="{ unset: !hasPrice(row) }" @click="openPrice(index)">
            <DollarOutlined />
            {{ priceSummary(row) }}
          </span>
        </a-tooltip>
      </span>
      <span class="wl-col-on">
        <a-switch :checked="row.enabled" size="small" @change="(v: any) => toggleRow(index, !!v)" />
      </span>
      <span class="wl-col-op">
        <a class="danger-link" title="删除这一条" @click="removeRow(index)"><DeleteOutlined /></a>
      </span>
    </div>
    <div v-if="!items.length" class="wl-empty">
      还没有模型：填上这条渠道能跑的模型名，请求才能路由到它。
    </div>
    <div class="wl-actions">
      <a-button size="small" @click="addRow"><PlusOutlined /> 添加一行</a-button>
      <a-button size="small" @click="bulkOpen = true"><SnippetsOutlined /> 批量粘贴</a-button>
    </div>

    <ModelPricingEditor
      v-model:open="priceOpen"
      :model-name="priceRow?.public_name || ''"
      :value="priceRow"
      @save="savePrice"
    />

    <a-modal v-model:open="bulkOpen" title="批量粘贴模型清单" width="560px" @ok="applyBulk">
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
.wl-editor { display: flex; flex-direction: column; gap: 6px; }
.wl-head { display: flex; gap: 6px; font-size: 12px; color: var(--color-text-secondary); }
.wl-row { display: flex; gap: 6px; align-items: center; }
.wl-col-name { flex: 1 1 34%; }
.wl-col-up { flex: 1 1 34%; }
.wl-col-proxy { flex: 0 0 128px; }
.wl-col-price { flex: 0 0 168px; }
.wl-col-on { flex: 0 0 44px; text-align: center; }
/* 价格胶囊：与密钥胶囊同一套视觉语言 —— 一眼能看出「这条配过价没有」 */
.price-pill {
  display: inline-flex; align-items: center; gap: 4px;
  max-width: 100%; padding: 1px 7px;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-primary) 13%, transparent);
  color: var(--color-primary);
  font-family: var(--font-family-mono); font-size: 12px; line-height: 20px;
  cursor: pointer; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.price-pill:hover { background: color-mix(in oklab, var(--color-primary) 22%, transparent); }
/* 未定价用灰底而不是主题色：提示语是「这里缺东西」，不是「这里能点」 */
.price-pill.unset {
  background: color-mix(in oklab, var(--color-text-secondary) 12%, transparent);
  color: var(--color-text-secondary);
}
.wl-col-op { flex: 0 0 24px; text-align: center; }
.wl-head .wl-col-on, .wl-head .wl-col-op { font-size: 12px; }
.wl-empty {
  padding: 10px 12px;
  border: 1px dashed var(--color-border);
  border-radius: 6px;
  color: var(--color-text-secondary);
  font-size: 13px;
}
.wl-actions { display: flex; gap: 8px; margin-top: 2px; }
.danger-link { color: var(--color-red); }
.field-hint { font-size: 12px; color: var(--color-text-secondary); }
</style>
