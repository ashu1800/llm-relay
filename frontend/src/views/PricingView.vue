<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  PlusOutlined,
  ReloadOutlined,
  SyncOutlined,
  EditOutlined,
  DeleteOutlined,
  ExperimentOutlined,
  SearchOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import { PRICE_SOURCES, SOURCE_META, type Pricing, type PricingSyncLog, type PricingSyncResult } from '@/api/types'
import { useProviderStore } from '@/stores/providers'
import ProviderTag from '@/components/ProviderTag.vue'
import DataState from '@/components/DataState.vue'

const providerStore = useProviderStore()
const loading = ref(false)
const rows = ref<Pricing[]>([])
const total = ref(0)
const query = reactive({ page: 1, page_size: 50, keyword: '', source: '', bound_only: false })

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

const syncing = ref(false)
const syncOpen = ref(false)
const syncResults = ref<PricingSyncResult[]>([])
const historyOpen = ref(false)
const history = ref<PricingSyncLog[]>([])

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
    if (query.source) p.set('source', query.source)
    if (query.bound_only) p.set('bound_only', 'true')
    const res = await api.get<{ items: Pricing[]; total: number }>('/pricing?' + p.toString())
    rows.value = res.items || []
    total.value = res.total || 0
    // 模型商列表用于给每行打上对应标识；失败不阻塞定价列表
    await providerStore.ensure()
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
      message.success('已更新，该条已转为手工来源（优先级最高）')
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

async function runSync() {
  syncing.value = true
  try {
    const res = await api.post<{ results: PricingSyncResult[] }>('/pricing/sync', {})
    syncResults.value = res.results || []
    syncOpen.value = true
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    syncing.value = false
  }
}

async function openHistory() {
  historyOpen.value = true
  try {
    const res = await api.get<{ items: PricingSyncLog[] }>('/pricing/history')
    history.value = res.items || []
  } catch (e: any) {
    message.error(e.message)
  }
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

function sourceMeta(s: string) {
  return SOURCE_META[s] ?? { label: s, color: 'default' }
}

function peakText(row: Pricing) {
  const rules = row.peak_rules
  if (!rules || !rules.length) return '无'
  const mult = (rules[0] as any).multiplier
  return '×' + mult + '（' + rules.length + ' 个时段）'
}

function fmtTime(t: string | null) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新增定价</a-button>
          <a-button :loading="syncing" @click="runSync"><SyncOutlined /> 同步价格</a-button>
          <a-button @click="openHistory">同步历史</a-button>
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
        <a-select
          v-model:value="query.source"
          :options="[{ value: '', label: '全部来源' }, ...PRICE_SOURCES]"
          style="width: 130px"
          @change="search"
        />
        <a-checkbox v-model:checked="query.bound_only" @change="search">仅看已绑定模型</a-checkbox>
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
        :scroll="{ x: 1320 }"
      >
        <template #emptyText>
          <a-empty description="还没有定价记录，点「新增定价」手工添加，或点「同步价格」从官方源拉取" />
        </template>
        <a-table-column title="模型名" data-index="model_key" :width="220" fixed="left" ellipsis />
        <a-table-column title="模型商" :width="140">
          <template #default="{ record }">
            <!-- LiteLLM 覆盖数百家模型商，未接入的标 0；显示占位而不是硬凑一个标签 -->
            <ProviderTag
              v-if="providerStore.byId(record.provider_id)"
              :code="providerStore.byId(record.provider_id)?.code"
              :name="providerStore.byId(record.provider_id)?.name"
            />
            <span v-else class="unassigned" title="不属于当前已接入的模型商">—</span>
          </template>
        </a-table-column>
        <a-table-column title="来源" :width="90">
          <template #default="{ record }">
            <a-tag :color="sourceMeta(record.source).color">{{ sourceMeta(record.source).label }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="输入 /1M" data-index="input_per_1m" :width="105" />
        <a-table-column title="输出 /1M" data-index="output_per_1m" :width="105" />
        <a-table-column title="缓存读 /1M" data-index="cache_read_per_1m" :width="115" />
        <a-table-column title="缓存写 /1M" data-index="cache_write_per_1m" :width="115" />
        <a-table-column title="峰时" :width="130">
          <template #default="{ record }">{{ peakText(record) }}</template>
        </a-table-column>
        <a-table-column title="匹配" data-index="match_type" :width="80" />
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
      :title="editing ? '修改定价（将转为手工来源，优先于自动同步）' : '新增定价'"
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

    <!-- 同步结果 -->
    <a-modal v-model:open="syncOpen" title="价格同步结果" :footer="null" width="620px">
      <a-alert
        type="info"
        show-icon
        message="来源优先级：手工录入 > 官方页面 > LiteLLM。低优先级不会覆盖高优先级。"
        style="margin-bottom: 12px"
      />
      <a-table
        :data-source="syncResults"
        :pagination="false"
        row-key="source"
        size="small"
      >
        <a-table-column title="来源" data-index="source" :width="100" />
        <a-table-column title="状态" :width="90">
          <template #default="{ record }">
            <a-tag :color="record.status === 'ok' ? 'green' : 'red'">
              {{ record.status === 'ok' ? '成功' : '失败' }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="新增" data-index="added" :width="70" />
        <a-table-column title="更新" data-index="updated" :width="70" />
        <a-table-column title="未变" data-index="unchanged" :width="70" />
        <a-table-column title="跳过" data-index="skipped_manual" :width="80" />
        <a-table-column title="错误" data-index="error" ellipsis />
      </a-table>
    </a-modal>

    <!-- 同步历史 -->
    <a-drawer v-model:open="historyOpen" title="同步历史" width="720">
      <a-table :data-source="history" :pagination="false" row-key="id" size="small">
        <a-table-column title="来源" data-index="source" :width="90" />
        <a-table-column title="状态" :width="80">
          <template #default="{ record }">
            <a-tag :color="record.status === 'ok' ? 'green' : 'red'">
              {{ record.status === 'ok' ? '成功' : '失败' }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="新增" data-index="added" :width="70" />
        <a-table-column title="更新" data-index="updated" :width="70" />
        <a-table-column title="跳过" data-index="skipped_manual" :width="70" />
        <a-table-column title="开始时间" :width="170">
          <template #default="{ record }">{{ fmtTime(record.started_at) }}</template>
        </a-table-column>
      </a-table>
    </a-drawer>

    <!-- 价格试算 -->
    <a-modal v-model:open="resolveOpen" title="价格试算" :footer="null" width="560px">
      <a-alert
        type="info"
        show-icon
        message="填入模型名与时刻，查看那一刻实际生效的单价（含峰时倍率）。留空时刻表示当前。"
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
