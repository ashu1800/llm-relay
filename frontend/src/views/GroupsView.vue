<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, DeleteOutlined, EditOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import ProviderTag from '@/components/ProviderTag.vue'
import { useProviderStore } from '@/stores/providers'
import type { ChannelGroup } from '@/api/types'

// 路由策略选项：value 与后端 model.Strategy* 常量一致，label 同时用于下拉与表格展示
const STRATEGY_OPTIONS = [
  { value: 'weighted', label: '加权随机' },
  { value: 'round_robin', label: '轮询' },
  { value: 'least_latency', label: '最低延迟' },
  { value: 'failover', label: '故障转移' }
]

const loading = ref(false)
const providerStore = useProviderStore()
const rows = ref<ChannelGroup[]>([])
const loadError = ref('')

const modalOpen = ref(false)
const editing = ref<ChannelGroup | null>(null)
const saving = ref(false)

const form = reactive({
  name: '',
  remark: '',
  strategy: 'weighted',
  is_default: false,
  enabled: true,
  // 分组归属的模型商：新建必选。分组决定「只允许哪个模型商的模型走它」，
  // 所以 0（不限）只在编辑历史分组时保留。
  // 新建时留 undefined 而不是 0：0 会让下拉把原始值「0」显示出来，
  // 而「不限」这一项在新建时压根不存在（后端会拒），显示 0 就是个看不懂的值
  provider_id: undefined as number | undefined
})

const title = computed(() => (editing.value ? '编辑分组' : '新建分组'))

// 「不限」只在分组本来就是不限时可选：默认分组是历史数据，不能因为
// 编辑它（比如改个策略）就被迫给整个中转站限定一个模型商
const allowUnlimited = computed(() => !!editing.value && editing.value.provider_id === 0)

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<{ items: ChannelGroup[] }>('/groups')
    rows.value = res.items || []
    await providerStore.ensure()
  } catch (e: any) {
    // 失败时仍然清空列表：旧数据配上错误提示容易被当成「当前真实的分组」，
    // 清空后由 DataState 统一呈现「加载失败 + 重试」，不会退化成「暂无数据」
    rows.value = []
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

// 后端返回的策略标识转中文标签，遇到未知取值时原样显示
function strategyLabel(v: string) {
  const found = STRATEGY_OPTIONS.find((s) => s.value === v)
  return found ? found.label : v
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    remark: '',
    strategy: 'weighted',
    is_default: false,
    enabled: true,
    provider_id: undefined
  })
  modalOpen.value = true
}

function openEdit(row: ChannelGroup) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    remark: row.remark || '',
    strategy: row.strategy || 'weighted',
    is_default: !!row.is_default,
    enabled: !!row.enabled,
    provider_id: row.provider_id || 0
  })
  modalOpen.value = true
}

async function save() {
  if (!form.name.trim()) {
    message.warning('分组名称必填')
    return
  }
  if (!form.provider_id) {
    message.warning('模型商必选：分组决定只允许哪个模型商的模型走它')
    return
  }
  saving.value = true
  try {
    const body = {
      name: form.name.trim(),
      remark: form.remark.trim(),
      strategy: form.strategy,
      is_default: form.is_default,
      enabled: form.enabled,
      // 新建时没选就是 0，交给后端给出「模型商必选」的明确报错
      provider_id: form.provider_id ?? 0
    }
    if (editing.value) {
      await api.put('/groups/' + editing.value.id, body)
      message.success('更新成功')
    } else {
      await api.post('/groups', body)
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

function confirmDelete(row: ChannelGroup) {
  Modal.confirm({
    title: '确认删除分组',
    content: '删除后不可恢复。若分组下仍有渠道或模板，后端会拒绝删除并说明原因。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/groups/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        // 409 表示分组仍被渠道或模板占用：后端给的中文说明原样展示，不要改写
        message.error(e.message, e.status === 409 ? 6 : 3)
      }
    }
  })
}

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建分组</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">共 {{ rows.length }} 个分组</span>
      </div>

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="分组列表加载失败"
        @retry="load"
      >
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="false"
        row-key="id"
        size="small"
        :scroll="{ x: 960 }"
      >
        <a-table-column title="ID" data-index="id" :width="70" />
        <a-table-column title="名称" data-index="name" :width="180" />
        <a-table-column title="模型商" :width="140">
          <template #default="{ record }">
            <ProviderTag
              v-if="providerStore.byId(record.provider_id)"
              :code="providerStore.byId(record.provider_id)?.code"
              :name="providerStore.byId(record.provider_id)?.name"
            />
            <span v-else class="muted" title="不限定模型商，任何模型都能走这个分组">不限</span>
          </template>
        </a-table-column>
        <a-table-column title="备注" :width="240" ellipsis>
          <template #default="{ record }">
            <span v-if="record.remark">{{ record.remark }}</span>
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <a-table-column title="路由策略" :width="120">
          <template #default="{ record }">
            <a-tag>{{ strategyLabel(record.strategy) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="是否默认" :width="100">
          <template #default="{ record }">
            <a-tag v-if="record.is_default" color="blue">默认</a-tag>
            <span v-else class="muted">否</span>
          </template>
        </a-table-column>
        <a-table-column title="是否启用" :width="100">
          <template #default="{ record }">
            <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="150" fixed="right">
          <template #default="{ record }">
            <a-space>
              <a @click="openEdit(record)"><EditOutlined /> 编辑</a>
              <a class="danger-link" @click="confirmDelete(record)"><DeleteOutlined /> 删除</a>
            </a-space>
          </template>
        </a-table-column>
        <template #emptyText>
          <a-empty description="还没有分组，点「新建分组」创建第一个" />
        </template>
      </a-table>
      </DataState>
    </section>

    <a-modal v-model:open="modalOpen" :title="title" :confirm-loading="saving" width="560px" @ok="save">
      <a-form layout="vertical">
        <a-form-item label="分组名称" required>
          <a-input v-model:value="form.name" placeholder="例如 高优先级" />
        </a-form-item>
        <a-form-item label="备注">
          <a-input v-model:value="form.remark" placeholder="选填，说明这个分组的用途" />
        </a-form-item>
        <a-form-item label="模型商" required>
          <a-select v-model:value="form.provider_id" placeholder="选择这个分组归属的模型商">
            <a-select-option v-if="allowUnlimited" :value="0">不限（不限定模型商）</a-select-option>
            <a-select-option v-for="p in providerStore.items" :key="p.id" :value="p.id">
              <ProviderTag :code="p.code" :name="p.name" />
            </a-select-option>
          </a-select>
          <div class="field-hint">
            分组决定只允许哪个模型商的模型走它：组里的渠道与绑定的模型都会被限定在该模型商内。
          </div>
        </a-form-item>
        <a-form-item label="路由策略">
          <a-select v-model:value="form.strategy" :options="STRATEGY_OPTIONS" />
        </a-form-item>
        <a-row :gutter="8">
          <a-col :span="12">
            <a-form-item label="设为默认分组">
              <a-switch v-model:checked="form.is_default" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="启用">
              <a-switch v-model:checked="form.enabled" />
            </a-form-item>
          </a-col>
        </a-row>
      </a-form>
    </a-modal>
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
.muted { color: var(--color-text-secondary); }
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.danger-link { color: var(--color-red); }
</style>
