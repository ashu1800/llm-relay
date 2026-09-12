<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
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
import { type Pricing } from '@/api/types'
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
  cache_write_per_1m: '0'
})

const resolveOpen = ref(false)
const resolveForm = reactive({ model: '', at: '' })
// 快照里既有字符串也有布尔（peak_applied），用宽松类型承接
const resolveResult = ref<Record<string, any> | null>(null)
const resolveMiss = ref(false)

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为这个模型本来就没有定价
const loadError = ref('')

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

function openCreate() {
  editing.value = null
  Object.assign(form, {
    model_key: '',
    match_type: 'exact',
    input_per_1m: '0',
    output_per_1m: '0',
    cache_read_per_1m: '0',
    cache_write_per_1m: '0'
  })
  formOpen.value = true
}

function openEdit(row: Pricing) {
  editing.value = row
  Object.assign(form, {
    model_key: row.model_key,
    match_type: row.match_type,
    input_per_1m: row.input_per_1m,
    output_per_1m: row.output_per_1m,
    cache_read_per_1m: row.cache_read_per_1m,
    cache_write_per_1m: row.cache_write_per_1m
  })
  formOpen.value = true
}

async function save() {
  if (!form.model_key.trim()) {
    message.warning('模型名必填')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await api.put('/pricing/' + editing.value.id, { ...form })
      message.success('已更新')
    } else {
      await api.post('/pricing', { ...form })
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

function openResolve() {
  resolveForm.model = ''
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

function peakText(row: Pricing) {
  const rules = row.peak_rules
  if (!rules || !rules.length) return '无'
  const mult = (rules[0] as any).multiplier
  return '×' + mult + '（' + rules.length + ' 个时段）'
}

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新增定价</a-button>
          <a-button @click="openResolve"><ExperimentOutlined /> 价格试算</a-button>
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
        :scroll="{ x: 960 }"
      >
        <template #emptyText>
          <a-empty description="还没有定价记录，点「新增定价」录入：单价按每 100 万 token 的美元价填" />
        </template>
        <a-table-column title="模型名" data-index="model_key" :width="180" fixed="left" ellipsis />
        <a-table-column title="输入 /1M" data-index="input_per_1m" :width="95" />
        <a-table-column title="输出 /1M" data-index="output_per_1m" :width="95" />
        <a-table-column title="缓存读 /1M" data-index="cache_read_per_1m" :width="105" />
        <a-table-column title="缓存写 /1M" data-index="cache_write_per_1m" :width="105" />
        <a-table-column title="倍率" :width="120">
          <template #default="{ record }">{{ peakText(record) }}</template>
        </a-table-column>
        <a-table-column title="匹配" data-index="match_type" :width="80" />
        <a-table-column title="操作" :width="110" fixed="right">
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
      width="560px"
      @ok="save"
    >
      <a-alert
        type="info"
        show-icon
        message="单价单位为「每 100 万 token 的美元价」，与厂商官网口径一致。"
        style="margin-bottom: 12px"
      />
      <a-form layout="vertical">
        <a-form-item label="模型名" required>
          <a-input v-model:value="form.model_key" placeholder="客户端请求时使用的模型名" />
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
        <a-input v-model:value="resolveForm.model" placeholder="模型名，例如 deepseek-v4-pro" />
        <a-input v-model:value="resolveForm.at" placeholder="时刻（可选），RFC3339，例如 2026-09-14T02:00:00Z" />
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
        <a-descriptions-item label="峰时倍率">
          {{ resolveResult.multiplier }}
          <a-tag v-if="resolveResult.peak_applied" color="orange" style="margin-left: 6px">
            {{ resolveResult.peak_label || '峰时' }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="价格来源">{{ resolveResult.source }}</a-descriptions-item>
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
</style>
