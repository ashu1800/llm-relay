<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import type { ModelItem } from '@/api/types'
import { useProviderStore } from '@/stores/providers'
import ProviderTag from '@/components/ProviderTag.vue'

const providerStore = useProviderStore()
const loading = ref(false)
const rows = ref<ModelItem[]>([])
const modalOpen = ref(false)
const saving = ref(false)
const editing = ref<ModelItem | null>(null)
const form = reactive({ public_name: '', description: '', provider_id: 1, enabled: true })

async function load() {
  loading.value = true
  try {
    const [m] = await Promise.all([
      api.get<{ items: ModelItem[] }>('/models'),
      providerStore.ensure()
    ])
    rows.value = m.items || []
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    public_name: '', description: '',
    provider_id: providerStore.items[0]?.id ?? 1,
    enabled: true
  })
  modalOpen.value = true
}

function openEdit(row: ModelItem) {
  editing.value = row
  Object.assign(form, {
    public_name: row.public_name,
    description: row.description,
    provider_id: row.provider_id,
    enabled: row.enabled
  })
  modalOpen.value = true
}

async function save() {
  if (!form.public_name.trim()) {
    message.warning('对外模型名必填')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await api.put('/models/' + editing.value.id, form)
      message.success('更新成功')
    } else {
      await api.post('/models', form)
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

function confirmDelete(row: ModelItem) {
  Modal.confirm({
    title: '确认删除模型',
    content: '将同时解除所有渠道与该模型的绑定。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/models/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e.message)
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
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建模型</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">共 {{ rows.length }} 个模型</span>
      </div>

      <a-table :data-source="rows" :loading="loading" :pagination="false" row-key="id" size="small">
        <a-table-column title="对外模型名" data-index="public_name" :width="240" />
        <a-table-column title="模型商" :width="150">
          <template #default="{ record }">
            <ProviderTag
              :code="providerStore.byId(record.provider_id)?.code"
              :name="providerStore.byId(record.provider_id)?.name"
            />
          </template>
        </a-table-column>
        <a-table-column title="备注" data-index="description" ellipsis />
        <a-table-column title="状态" :width="90">
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
      </a-table>
    </section>

    <a-modal
      v-model:open="modalOpen"
      :title="editing ? '编辑模型' : '新建模型'"
      :confirm-loading="saving"
      @ok="save"
    >
      <a-form layout="vertical">
        <a-form-item label="对外模型名" required>
          <a-input v-model:value="form.public_name" placeholder="客户端请求时使用的模型名" />
        </a-form-item>
        <a-form-item label="模型商">
          <a-select v-model:value="form.provider_id">
            <a-select-option v-for="p in providerStore.items" :key="p.id" :value="p.id">
              <ProviderTag :code="p.code" :name="p.name" />
            </a-select-option>
          </a-select>
          <div class="field-hint">
            模型商决定该模型用哪一套单价；改错会取不到对应模型商的价格。
          </div>
        </a-form-item>
        <a-form-item label="备注">
          <a-input v-model:value="form.description" />
        </a-form-item>
        <a-form-item label="启用">
          <a-switch v-model:checked="form.enabled" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<style scoped>
.manage-container { padding: var(--gap); }
.manage-panel { padding: 0; overflow: hidden; }
.manage-toolbar { display: flex; align-items: center; gap: var(--gap); padding: var(--gap); min-height: 64px; }
.toolbar-left { display: flex; gap: var(--gap); }
.toolbar-spacer { flex: 1; }
.toolbar-hint { color: var(--color-text-secondary); font-size: 13px; }
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.danger-link { color: var(--color-red); }
</style>
