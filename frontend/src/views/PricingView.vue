<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  PlusOutlined,
  ReloadOutlined,
  EditOutlined,
  DeleteOutlined,
  ExperimentOutlined,
  SearchOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import { type Channel, type Pricing, type RateRule } from '@/api/types'
import DataState from '@/components/DataState.vue'

const loading = ref(false)
const rows = ref<Pricing[]>([])
const total = ref(0)
const query = reactive({ page: 1, page_size: 50, keyword: '', bound_only: false })

const editing = ref<Pricing | null>(null)
const formOpen = ref(false)
const saving = ref(false)
const form = reactive({
  model_key: '',
  match_type: 'exact',
  input_per_1m: '0',
  output_per_1m: '0',
  cache_read_per_1m: '0',
  cache_write_per_1m: '0',
  // 固定倍率：没有时段命中时按它算。1 表示原价
  multiplier: 1 as number,
  // 时段倍率：命中窗口时按窗口倍率算，优先于固定倍率
  peak_rules: [] as RateRule[]
})

const WEEKDAYS = [
  { label: '周一', value: 1 },
  { label: '周二', value: 2 },
  { label: '周三', value: 3 },
  { label: '周四', value: 4 },
  { label: '周五', value: 5 },
  { label: '周六', value: 6 },
  { label: '周日', value: 7 }
]

const resolveOpen = ref(false)
const resolveForm = reactive({ model: '', at: '' })
// 快照里既有字符串也有布尔（peak_applied），用宽松类型承接
const resolveResult = ref<Record<string, any> | null>(null)
const resolveMiss = ref(false)

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为这个模型本来就没有定价
const loadError = ref('')

// 未定价的模型：渠道白名单里有、定价表里没有。
// 这种模型跑起来一切正常，只是费用恒为 0 —— 不主动提示就会被当成「免费」
const unpriced = ref<string[]>([])

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const p = new URLSearchParams()
    p.set('page', String(query.page))
    p.set('page_size', String(query.page_size))
    if (query.keyword.trim()) p.set('keyword', query.keyword.trim())
    if (query.bound_only) p.set('bound_only', 'true')
    const res = await api.get<{ items: Pricing[]; total: number }>('/pricing?' + p.toString())
    rows.value = res.items || []
    total.value = res.total || 0
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
  await loadUnpriced()
}

// loadUnpriced 用「渠道白名单的模型名」减去「已定价的模型名」。
// 定价表现在是手工维护的，所以「哪些模型还没定价」只能这样算出来。
async function loadUnpriced() {
  try {
    // 渠道列表单页上限是 200，所以显式带上 page_size：
    // 默认只有 50，渠道一多就会把「已定价」的模型误报成未定价
    const [channels, priced] = await Promise.all([
      api.get<{ items: Channel[] }>('/channels?page_size=200'),
      api.get<{ items: Pricing[] }>('/pricing?page_size=500')
    ])
    const whitelist = new Set<string>()
    for (const ch of channels.items || []) {
      for (const name of ch.models || []) whitelist.add(name)
    }
    const pricedKeys = new Set(
      (priced.items || []).filter((r) => r.active && r.match_type !== 'prefix').map((r) => r.model_key)
    )
    unpriced.value = [...whitelist].filter((m) => !pricedKeys.has(m)).sort()
  } catch {
    // 这只是一个提示，拿不到就不显示，不打扰用户
    unpriced.value = []
  }
}

function search() {
  query.page = 1
  load()
}

function changePage(p: number, ps: number) {
  query.page = p
  query.page_size = ps
  load()
}

function emptyForm() {
  return {
    model_key: '',
    match_type: 'exact',
    input_per_1m: '0',
    output_per_1m: '0',
    cache_read_per_1m: '0',
    cache_write_per_1m: '0',
    multiplier: 1,
    peak_rules: [] as RateRule[]
  }
}

function openCreate(modelKey = '') {
  editing.value = null
  Object.assign(form, emptyForm(), { model_key: modelKey })
  formOpen.value = true
}

function openEdit(row: Pricing) {
  editing.value = row
  Object.assign(form, emptyForm(), {
    model_key: row.model_key,
    match_type: row.match_type,
    input_per_1m: row.input_per_1m,
    output_per_1m: row.output_per_1m,
    cache_read_per_1m: row.cache_read_per_1m,
    cache_write_per_1m: row.cache_write_per_1m,
    multiplier: Number(row.multiplier) || 1,
    peak_rules: (row.peak_rules || []).map((r) => ({ ...r, days: [...(r.days || [])] }))
  })
  formOpen.value = true
}

function addRule() {
  form.peak_rules.push({ days: [], start: '09:00', end: '12:00', multiplier: 2, label: '' })
}

function removeRule(index: number) {
  form.peak_rules.splice(index, 1)
}

// 用户的原话就是「周一到周五 9:00-12:00、14:00-18:00 双倍」，
// 直接给一键预设，省得手填四条规则还容易填错
function applyWorkdayPreset() {
  form.peak_rules = [
    { days: [1, 2, 3, 4, 5], start: '09:00', end: '12:00', multiplier: 2, label: '上午高峰' },
    { days: [1, 2, 3, 4, 5], start: '14:00', end: '18:00', multiplier: 2, label: '下午高峰' }
  ]
}

function applyNightPreset() {
  form.peak_rules = [{ days: [], start: '22:00', end: '06:00', multiplier: 0.5, label: '夜间五折' }]
}

// 时段窗口写错不会报错、只会永不命中，所以提交前先在本地拦一道
function validateRules(): string | null {
  const timeRe = /^\d{2}:\d{2}$/
  for (let i = 0; i < form.peak_rules.length; i++) {
    const r = form.peak_rules[i]
    if (!timeRe.test(r.start) || !timeRe.test(r.end)) {
      return '第 ' + (i + 1) + ' 条时段规则：时间要填成 09:00 这样的格式'
    }
    if (!r.multiplier || r.multiplier <= 0) {
      return '第 ' + (i + 1) + ' 条时段规则：倍率要大于 0'
    }
  }
  return null
}

async function save() {
  if (!form.model_key.trim()) {
    message.warning('模型名必填')
    return
  }
  const ruleErr = validateRules()
  if (ruleErr) {
    message.warning(ruleErr)
    return
  }
  saving.value = true
  const payload = {
    model_key: form.model_key.trim(),
    match_type: form.match_type,
    input_per_1m: form.input_per_1m,
    output_per_1m: form.output_per_1m,
    cache_read_per_1m: form.cache_read_per_1m,
    cache_write_per_1m: form.cache_write_per_1m,
    multiplier: form.multiplier || 1,
    // 空数组表示「清掉所有时段规则」，与「没传这个字段」是两件事
    peak_rules: form.peak_rules
  }
  try {
    if (editing.value) {
      await api.put('/pricing/' + editing.value.id, payload)
      message.success('已更新，下一次请求起按新价计费')
    } else {
      await api.post('/pricing', payload)
      message.success('已新增')
    }
    formOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

async function toggleActive(row: Pricing, next: boolean) {
  try {
    await api.put('/pricing/' + row.id, { active: next })
    row.active = next
    message.success(next ? '已启用' : '已停用（停用的定价不参与计费）')
    await loadUnpriced()
  } catch (e: any) {
    message.error(e.message)
  }
}

function confirmDelete(row: Pricing) {
  Modal.confirm({
    title: '确认删除定价',
    content: '删除后该模型将按其他匹配规则或不计费。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/pricing/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

function openResolve(modelKey = '') {
  resolveForm.model = modelKey
  resolveForm.at = ''
  resolveResult.value = null
  resolveMiss.value = false
  resolveOpen.value = true
}

async function doResolve() {
  if (!resolveForm.model.trim()) {
    message.warning('请填写模型名')
    return
  }
  try {
    const res = await api.post<{ found: boolean; snapshot?: Record<string, any> }>('/pricing/resolve', {
      model: resolveForm.model.trim(),
      at: resolveForm.at.trim()
    })
    resolveMiss.value = !res.found
    resolveResult.value = res.found ? res.snapshot ?? null : null
  } catch (e: any) {
    message.error(e.message)
  }
}

// 倍率列的展示：固定倍率与时段规则分开写，别让人以为只有一个能生效
function fixedText(row: Pricing) {
  const m = Number(row.multiplier)
  if (!m || m === 1) return ''
  return '固定 ×' + m
}

function peakText(row: Pricing) {
  const rules = row.peak_rules
  if (!rules || !rules.length) return ''
  const mults = [...new Set(rules.map((r: RateRule) => r.multiplier))]
  const desc = mults.map((m) => '×' + m).join('/')
  return '时段 ' + desc + '（' + rules.length + ' 段）'
}

// 鼠标悬停看完整规则：表格里只放得下「×2（2 段）」这样的摘要
function peakTitle(row: Pricing) {
  return (row.peak_rules || [])
    .map((r: RateRule) => dayText(r.days) + ' ' + r.start + '-' + r.end + ' ×' + r.multiplier)
    .join('；')
}

function dayText(days: number[]) {
  if (!days || !days.length) return '每天'
  return days.map((d) => WEEKDAYS.find((w) => w.value === d)?.label ?? d).join('、')
}

const hasRules = computed(() => form.peak_rules.length > 0)

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate()"><PlusOutlined /> 新增定价</a-button>
          <a-button @click="openResolve()"><ExperimentOutlined /> 价格试算</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <div class="toolbar-spacer" />
        <a-input
          v-model:value="query.keyword"
          placeholder="搜索模型名"
          allow-clear
          style="width: 200px"
          @press-enter="search"
        >
          <template #prefix><SearchOutlined /></template>
        </a-input>
        <a-checkbox v-model:checked="query.bound_only" @change="search">仅看渠道白名单里的模型</a-checkbox>
        <a-button type="primary" @click="search">查询</a-button>
      </div>

      <!-- 未定价的模型：跑得通但费用恒为 0，不说的话没人会发现 -->
      <a-alert
        v-if="unpriced.length"
        type="warning"
        show-icon
        class="unpriced-hint"
        :message="'有 ' + unpriced.length + ' 个模型在渠道白名单里，但还没有定价（这些模型的费用会记为 0）'"
      >
        <template #description>
          <a-space wrap>
            <a
              v-for="name in unpriced"
              :key="name"
              class="unpriced-item"
              @click="openCreate(name)"
            >{{ name }}</a>
          </a-space>
        </template>
      </a-alert>

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="定价列表加载失败"
        @retry="load"
      >
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="{
          current: query.page,
          pageSize: query.page_size,
          total,
          showSizeChanger: true,
          pageSizeOptions: ['20', '50', '100', '200'],
          showTotal: (t: number) => '共 ' + t + ' 条',
          onChange: changePage
        }"
        row-key="id"
        size="small"
        :scroll="{ x: 1080 }"
      >
        <template #emptyText>
          <a-empty description="还没有定价记录，点「新增定价」录入：单价按每 100 万 token 的美元价填" />
        </template>
        <a-table-column title="模型名" data-index="model_key" :width="180" fixed="left" ellipsis />
        <a-table-column title="输入 /1M" data-index="input_per_1m" :width="95" />
        <a-table-column title="输出 /1M" data-index="output_per_1m" :width="95" />
        <a-table-column title="缓存读 /1M" data-index="cache_read_per_1m" :width="105" />
        <a-table-column title="缓存写 /1M" data-index="cache_write_per_1m" :width="105" />
        <a-table-column title="固定倍率" :width="100">
          <template #default="{ record }">
            <span v-if="fixedText(record)">{{ fixedText(record) }}</span>
            <span v-else class="unassigned">—</span>
          </template>
        </a-table-column>
        <a-table-column title="时段倍率" :width="150">
          <template #default="{ record }">
            <span v-if="peakText(record)" :title="peakTitle(record)">
              {{ peakText(record) }}
            </span>
            <span v-else class="unassigned">—</span>
          </template>
        </a-table-column>
        <a-table-column title="匹配" data-index="match_type" :width="80" />
        <a-table-column title="启用" :width="80">
          <template #default="{ record }">
            <a-switch :checked="record.active" size="small" @change="(v: any) => toggleActive(record, !!v)" />
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="130" fixed="right">
          <template #default="{ record }">
            <a-space>
              <a @click="openEdit(record)"><EditOutlined /> 改价</a>
              <a class="danger-link" @click="confirmDelete(record)"><DeleteOutlined /></a>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
      </DataState>
    </section>

    <!-- 新增 / 改价 -->
    <a-modal
      v-model:open="formOpen"
      :title="editing ? '修改定价' : '新增定价'"
      :confirm-loading="saving"
      width="720px"
      @ok="save"
    >
      <a-alert
        type="info"
        show-icon
        message="单价单位为「每 100 万 token 的美元价」。倍率可以叠加时段：命中时段规则时用时段倍率，其余时间用固定倍率。"
        style="margin-bottom: 12px"
      />
      <a-form layout="vertical">
        <a-form-item label="模型名" required>
          <a-input v-model:value="form.model_key" placeholder="客户端请求时使用的模型名，例如 deepseek-v4-flash" />
        </a-form-item>
        <a-form-item label="匹配方式">
          <a-radio-group v-model:value="form.match_type">
            <a-radio value="exact">精确匹配</a-radio>
            <a-radio value="prefix">前缀匹配</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="输入价 /1M">
              <a-input v-model:value="form.input_per_1m" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="输出价 /1M">
              <a-input v-model:value="form.output_per_1m" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="缓存读 /1M">
              <a-input v-model:value="form.cache_read_per_1m" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="缓存写 /1M">
              <a-input v-model:value="form.cache_write_per_1m" />
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item label="固定倍率">
          <a-input-number v-model:value="form.multiplier" :min="0.01" :max="100" :step="0.1" style="width: 160px" />
          <div class="field-hint">1 = 原价；0.5 = 五折；没有命中任何时段规则时按它算。</div>
        </a-form-item>

        <a-form-item>
          <template #label>
            <span>时段倍率</span>
            <a-space style="margin-left: 12px">
              <a @click="applyWorkdayPreset">工作日高峰预设</a>
              <a @click="applyNightPreset">夜间五折预设</a>
              <a @click="addRule"><PlusOutlined /> 加一条</a>
            </a-space>
          </template>
          <div v-if="!hasRules" class="rule-empty">
            没有时段规则：全程按固定倍率计费。
          </div>
          <div v-for="(rule, index) in form.peak_rules" :key="index" class="rule-row">
            <a-select
              v-model:value="rule.days"
              mode="multiple"
              :options="WEEKDAYS"
              placeholder="每天"
              style="width: 190px"
              :max-tag-count="2"
            />
            <a-time-picker v-model:value="rule.start" value-format="HH:mm" format="HH:mm" :minute-step="5" style="width: 100px" />
            <span class="rule-dash">—</span>
            <a-time-picker v-model:value="rule.end" value-format="HH:mm" format="HH:mm" :minute-step="5" style="width: 100px" />
            <a-input-number v-model:value="rule.multiplier" :min="0.01" :max="100" :step="0.1" style="width: 90px" />
            <a-input v-model:value="rule.label" placeholder="备注（可选）" style="width: 130px" />
            <a class="danger-link" @click="removeRule(index)"><DeleteOutlined /></a>
          </div>
          <div class="field-hint">
            时间按服务器本地时区判断（当前部署是 Asia/Shanghai）；结束时间早于开始时间表示跨午夜；
            多条同时命中时，靠后的那条生效。倍率大于 1 是加价，小于 1 是折扣。
          </div>
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 价格试算 -->
    <a-modal v-model:open="resolveOpen" title="价格试算" :footer="null" width="560px">
      <a-alert
        type="info"
        show-icon
        message="填入模型名与时刻，查看那一刻实际生效的单价（含时段倍率）。留空时刻表示当前。"
        style="margin-bottom: 12px"
      />
      <a-space direction="vertical" style="width: 100%">
        <a-input v-model:value="resolveForm.model" placeholder="模型名，例如 deepseek-v4-flash" />
        <a-input v-model:value="resolveForm.at" placeholder="时刻（可选），RFC3339，例如 2026-09-14T09:30:00+08:00" />
        <a-button type="primary" block @click="doResolve"><ExperimentOutlined /> 试算</a-button>
      </a-space>

      <a-alert
        v-if="resolveMiss"
        type="warning"
        show-icon
        message="未匹配到定价"
        description="该模型没有对应的定价记录，账单中费用会记为 0。"
        style="margin-top: 12px"
      />
      <a-descriptions v-if="resolveResult" :column="1" bordered size="small" style="margin-top: 12px">
        <a-descriptions-item label="模型">{{ resolveResult.model_key }}</a-descriptions-item>
        <a-descriptions-item label="生效时刻">{{ resolveResult.resolved_at }}</a-descriptions-item>
        <a-descriptions-item label="输入 /1M">{{ resolveResult.input_per_1m }}</a-descriptions-item>
        <a-descriptions-item label="输出 /1M">{{ resolveResult.output_per_1m }}</a-descriptions-item>
        <a-descriptions-item label="缓存读 /1M">{{ resolveResult.cache_read_per_1m }}</a-descriptions-item>
        <a-descriptions-item label="生效倍率">
          {{ resolveResult.multiplier }}
          <a-tag v-if="resolveResult.peak_applied" color="orange" style="margin-left: 6px">
            {{ resolveResult.peak_label || '时段倍率' }}
          </a-tag>
          <a-tag v-else-if="resolveResult.multiplier_source === 'fixed'" style="margin-left: 6px">固定倍率</a-tag>
          <a-tag v-else style="margin-left: 6px">原价</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="固定倍率">{{ resolveResult.fixed_multiplier }}</a-descriptions-item>
      </a-descriptions>
    </a-modal>
  </div>
</template>

<style scoped>
.manage-container { padding: var(--gap); }
.unassigned { color: var(--color-text-secondary); }
.manage-panel { padding: 0; overflow: hidden; }
.manage-toolbar {
  display: flex;
  align-items: center;
  gap: var(--gap);
  padding: var(--gap);
  min-height: 64px;
  flex-wrap: wrap;
}
.toolbar-left { display: flex; gap: var(--gap); flex-wrap: wrap; }
.toolbar-spacer { flex: 1; }
.danger-link { color: var(--color-red); }
.unpriced-hint { margin: 0 var(--gap) var(--gap); }
.unpriced-item { cursor: pointer; }
.rule-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}
.rule-dash { color: var(--color-text-secondary); }
.rule-empty {
  color: var(--color-text-secondary);
  font-size: 13px;
  margin-bottom: 8px;
}
</style>
