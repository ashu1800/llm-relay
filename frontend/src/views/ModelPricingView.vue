<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { PlusOutlined, EditOutlined, DeleteOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import { message, modal } from '@/utils/feedback'
import DataState from '@/components/DataState.vue'
import EmptyState from '@/components/EmptyState.vue'
import { modalWidth } from '@/utils/modalWidth'
import {
  RMB_FIELDS,
  USD_FIELDS,
  amountError,
  currencyLabel,
  emptyForm,
  formError,
  formFrom,
  missingTierWarning,
  payloadFrom,
  priceState,
  priceSummary,
  type ModelPrice,
  type PriceForm,
  type PriceState
} from '@/components/modelPricing'

// 模型定价页（2026-10-27）。
//
// 这一页是单价的**唯一**编辑入口：渠道页只管倍率与时段规则。
// 拆开的理由：原来「给某个模型定价」等于「在每条渠道里各填一遍」，
// 同一个模型在三家上游就有三份互相不知道的价格，而用户想表达的是
// 「这个模型卖多少钱」—— 渠道差异用倍率表达就够了。
//
// 同一个模型可以同时有美元价和人民币价，互不换算：记账时按**所属渠道的
// 币种**取对应那一档（见后端 pricing.Engine.Resolve）。不做换算是因为
// 汇率会变，把它写进记账路径等于给历史账目埋一颗会漂移的雷。
const loading = ref(true)
const loadError = ref('')
const rows = ref<ModelPrice[]>([])
// 白名单里有、但没定价的模型名（后端给）：这些也要列出来，标「未定价」。
// 漏配的后果是这笔调用被记成 0 元，账面上完全看不出异常，只能靠这里点名。
const unpricedModels = ref<string[]>([])
// 模型 -> 它在渠道里实际用到的币种（后端给），用于提示「该档没填」
const usedCurrencies = ref<Record<string, string[]>>({})
const keyword = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<{
      items: ModelPrice[] | null
      unpriced_models: string[] | null
      used_currencies: Record<string, string[]> | null
    }>('/model-prices')
    rows.value = res.items || []
    unpricedModels.value = res.unpriced_models || []
    usedCurrencies.value = res.used_currencies || {}
  } catch (e: any) {
    loadError.value = e?.message || '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)

/**
 * 表格数据 = 已定价的行 + 未定价的白名单模型名。
 *
 * 未定价的行也进表（灰着显示「未定价」）：把它们藏起来的话，用户看到的
 * 是「所有模型都定好价了」——而那正是账单出错时最误导人的画面。
 */
interface TableRow {
  model_name: string
  form: PriceForm
  priced: boolean
}

const tableRows = computed<TableRow[]>(() => {
  const out: TableRow[] = rows.value.map((r) => ({
    model_name: r.model_name,
    form: formFrom(r),
    priced: true
  }))
  const known = new Set(out.map((r) => r.model_name))
  for (const name of unpricedModels.value) {
    if (known.has(name)) continue
    out.push({ model_name: name, form: emptyForm(), priced: false })
  }
  return out
})

const filtered = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return tableRows.value
  if (k === '未定价') return tableRows.value.filter((r) => !r.priced || priceState(r.form) === 'none')
  return tableRows.value.filter((r) => r.model_name.toLowerCase().includes(k))
})

const unpricedCount = computed(() => tableRows.value.filter((r) => priceState(r.form) === 'none').length)

// ---- 编辑弹窗 ----
const editing = ref(false)
const editingName = ref('')
const form = ref<PriceForm>(emptyForm())
const saving = ref(false)
const saveError = ref('')

const editingIsNew = computed(() => !rows.value.some((r) => r.model_name === editingName.value))

function startCreate() {
  editingName.value = ''
  form.value = emptyForm()
  saveError.value = ''
  editing.value = true
}

function startEdit(row: TableRow) {
  editingName.value = row.model_name
  form.value = formFrom(row.form)
  saveError.value = ''
  editing.value = true
}

const tierWarning = computed(() =>
  editingName.value ? missingTierWarning(form.value, usedCurrencies.value[editingName.value] || []) : ''
)

const formErr = computed(() => formError(form.value))
const canSave = computed(() => editingName.value.trim() !== '' && !formErr.value && !saving.value)

async function save() {
  if (!canSave.value) return
  saving.value = true
  saveError.value = ''
  try {
    await api.put(`/model-prices/${encodeURIComponent(editingName.value.trim())}`, payloadFrom(editingName.value.trim(), form.value))
    message.success('已保存')
    editing.value = false
    await load()
  } catch (e: any) {
    saveError.value = e?.message || '保存失败'
  } finally {
    saving.value = false
  }
}

function remove(row: TableRow) {
  modal.confirm({
    centered: true,
    title: `删除 ${row.model_name} 的定价？`,
    content: '删除后这个模型回到「未定价」，它的调用费用会被记成 0 元。渠道与倍率不受影响。',
    okText: '删除',
    cancelText: '取消',
    okType: 'danger',
    onOk: async () => {
      try {
        await api.del(`/model-prices/${encodeURIComponent(row.model_name)}`)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e?.message || '删除失败')
      }
    }
  })
}

// 状态 → 文案 + 颜色。合成一张表而不是两个 switch：
// 两处各自列一遍四种状态时，加一种状态（例如「渠道币种与已配档不匹配」）
// 很容易只改一处 —— 界面上就会出现「红色标签写着双币种」这种自相矛盾的格子，
// 而且不报错。
const STATE_META: Record<PriceState, { label: string; color: string }> = {
  both: { label: '双币种', color: 'green' },
  // 只有一档：橙。模型可能只在单币种渠道里用（那就没问题），
  // 所以不标红；真正的「会记 0 元」提示由 tierWarning 在编辑时给。
  'usd-only': { label: '仅美元', color: 'orange' },
  'rmb-only': { label: '仅人民币', color: 'orange' },
  none: { label: '未定价', color: 'red' }
}

function stateLabel(row: TableRow): string {
  return STATE_META[priceState(row.form)].label
}

function stateColor(row: TableRow): string {
  return STATE_META[priceState(row.form)].color
}

function usedLabel(name: string): string {
  const list = usedCurrencies.value[name] || []
  if (!list.length) return '—'
  return list.map(currencyLabel).join(' / ')
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div class="head-text">
        <h2 class="page-title">模型定价</h2>
      </div>
      <div class="head-actions">
        <a-button :loading="loading" @click="load">
          <template #icon><ReloadOutlined /></template>
          刷新
        </a-button>
        <a-button type="primary" @click="startCreate">
          <template #icon><PlusOutlined /></template>
          新增定价
        </a-button>
      </div>
    </div>

    <a-alert
      v-if="unpricedCount > 0"
      type="warning"
      show-icon
      class="unpriced-alert"
      :message="`有 ${unpricedCount} 个模型还没有定价`"
    >
      <template #description>
        <div class="unpriced-alert-desc">
          <span>它们的调用费用会被记成 0 元。请及时配置，避免账单与上游脱节。</span>
          <a-button type="link" size="small" class="unpriced-filter-btn" @click="keyword = '未定价'">
            只看未定价模型
          </a-button>
        </div>
      </template>
    </a-alert>

    <a-input v-model:value="keyword" placeholder="按模型名搜索" allow-clear class="search" />

    <DataState :loading="loading" :error="loadError" :empty="!filtered.length" empty-text="还没有模型定价">
      <a-table :data-source="filtered" :pagination="false" size="small" row-key="model_name" :scroll="{ x: 900 }">
        <a-table-column title="模型" data-index="model_name" :width="260" ellipsis>
          <template #default="{ record }">
            <span class="mono model-name">{{ record.model_name }}</span>
          </template>
        </a-table-column>
        <a-table-column title="单价（每 100 万 token）" :width="300">
          <template #default="{ record }">
            <span :class="{ muted: !record.priced }">{{ priceSummary(record.form) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="110">
          <template #default="{ record }">
            <a-tag :color="stateColor(record)">{{ stateLabel(record) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="渠道币种" :width="140">
          <template #default="{ record }">
            <span class="muted">{{ usedLabel(record.model_name) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="150" fixed="right">
          <template #default="{ record }">
            <a-button type="link" size="small" @click="startEdit(record)">
              <template #icon><EditOutlined /></template>
              {{ record.priced ? '编辑' : '定价' }}
            </a-button>
            <a-button v-if="record.priced" type="link" size="small" danger @click="remove(record)">
              <template #icon><DeleteOutlined /></template>
            </a-button>
          </template>
        </a-table-column>
        <template #emptyText>
          <EmptyState text="还没有模型定价" size="sm" />
        </template>
      </a-table>
    </DataState>

    <a-modal
      v-model:open="editing"
      :title="editingIsNew ? '新增模型定价' : `编辑 ${editingName} 的定价`"
      :width="modalWidth(560)"
      :confirm-loading="saving"
      :ok-button-props="{ disabled: !canSave }"
      ok-text="保存"
      cancel-text="取消"
      destroy-on-close
      @ok="save"
    >
      <a-alert v-if="saveError" type="error" show-icon :message="saveError" class="modal-alert" />
      <a-alert v-if="tierWarning" type="warning" show-icon :message="tierWarning" class="modal-alert" />

      <a-form layout="vertical">
        <a-form-item label="模型名（对外名称，必须与渠道白名单里的一致）" required>
          <a-input
            v-model:value="editingName"
            :disabled="!editingIsNew"
            placeholder="例如 gpt-5、claude-sonnet-4"
            class="mono"
          />
        </a-form-item>

        <div class="tier is-usd">
          <div class="tier-head">
            <span class="tier-title"><span class="currency-chip is-usd">$ USD</span> 美元价</span>
            <span class="muted">用于币种为美元的渠道</span>
          </div>
          <div class="tier-grid">
            <a-form-item v-for="f in USD_FIELDS" :key="f.key" :label="f.label + '（$ / 1M）'">
              <a-input v-model:value="form[f.key]" placeholder="0" class="mono" />
              <div v-if="amountError(form[f.key])" class="field-err">{{ amountError(form[f.key]) }}</div>
            </a-form-item>
          </div>
        </div>

        <div class="tier is-cny">
          <div class="tier-head">
            <span class="tier-title"><span class="currency-chip is-cny">¥ CNY</span> 人民币价</span>
            <span class="muted">用于币种为人民币的渠道</span>
          </div>
          <div class="tier-grid">
            <a-form-item v-for="f in RMB_FIELDS" :key="f.key" :label="f.label + '（¥ / 1M）'">
              <a-input v-model:value="form[f.key]" placeholder="0" class="mono" />
              <div v-if="amountError(form[f.key])" class="field-err">{{ amountError(form[f.key]) }}</div>
            </a-form-item>
          </div>
        </div>
      </a-form>

      <p class="modal-note">
        留空的档位按 0 计 —— 只有一档是合法状态（例如只按输入计价）。两档互不换算，
        各自独立生效。
      </p>
    </a-modal>
  </div>
</template>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: var(--gap);
  padding: var(--gap);
}

.page-head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: var(--gap);
  flex-wrap: wrap;
}

.page-title {
  margin: 0;
  font-size: var(--font-size-display-sm);
  font-weight: 700;
  color: var(--color-text);
}

.head-actions {
  display: flex;
  gap: 8px;
}

.search {
  max-width: 320px;
}

.unpriced-alert {
  margin-bottom: 0;
}

.model-name {
  font-family: var(--font-family-mono);
}

.muted {
  color: var(--color-text-secondary);
}

.unpriced-alert {
  box-shadow: 0 0 14px color-mix(in oklab, var(--color-orange) 20%, transparent);
}

.unpriced-alert-desc {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}

.unpriced-filter-btn {
  padding: 0;
  height: auto;
  font-size: var(--font-size-xs);
}

.modal-alert {
  margin-bottom: 12px;
}

.tier {
  margin-bottom: 8px;
  padding: 10px 12px;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-bg) 60%, var(--color-fg));
  border: 1px solid var(--color-border);
}

.tier.is-usd {
  border-left: 3px solid var(--color-green);
}

.tier.is-cny {
  border-left: 3px solid var(--color-orange);
}

.currency-chip {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-2xs);
  padding: 1px 5px;
  border-radius: var(--radius-control);
}

.currency-chip.is-usd {
  background: color-mix(in oklab, var(--color-green) 18%, transparent);
  color: var(--text-green);
}

.currency-chip.is-cny {
  background: color-mix(in oklab, var(--color-orange) 18%, transparent);
  color: var(--text-amber);
}

.tier-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 8px;
  font-size: var(--font-size-sm);
}

.tier-title {
  font-weight: 600;
  color: var(--color-text);
}

.tier-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0 12px;
}

.field-err {
  margin-top: 2px;
  font-size: var(--font-size-sm);
  color: var(--text-red);
}

.modal-note {
  margin: 0;
  font-size: var(--font-size-sm);
  line-height: 1.7;
  color: var(--color-text-secondary);
}

/* 窄屏：四列并排会挤到看不清输入框内容 */
@media (max-width: 768px) {
  .tier-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
