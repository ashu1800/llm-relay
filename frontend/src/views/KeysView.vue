<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, DeleteOutlined, CopyOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import type { APIKey } from '@/api/types'

const loading = ref(false)
const rows = ref<APIKey[]>([])
const modalOpen = ref(false)
const saving = ref(false)
const createdKey = ref('')
const form = reactive({ name: '' })

async function load() {
  loading.value = true
  try {
    const res = await api.get<{ items: APIKey[] }>('/keys')
    rows.value = res.items || []
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  form.name = ''
  createdKey.value = ''
  modalOpen.value = true
}

async function save() {
  if (!form.name.trim()) {
    message.warning('名称必填')
    return
  }
  saving.value = true
  try {
    const res = await api.post<{ key: string }>('/keys', { name: form.name.trim() })
    // 明文只返回一次，留在弹窗里等用户复制
    createdKey.value = res.key
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

async function copyKey() {
  try {
    await navigator.clipboard.writeText(createdKey.value)
    message.success('已复制到剪贴板')
  } catch {
    message.warning('复制失败，请手动选择复制')
  }
}

async function toggle(row: APIKey) {
  try {
    await api.put('/keys/' + row.id, { enabled: !row.enabled })
    await load()
  } catch (e: any) {
    message.error(e.message)
  }
}

function confirmDelete(row: APIKey) {
  Modal.confirm({
    title: '确认删除密钥',
    content: '使用该密钥的客户端将立即无法调用。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/keys/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

function fmt(t: string | null) {
  if (!t) return '从未使用'
  return new Date(t).toLocaleString('zh-CN')
}

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建密钥</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">共 {{ rows.length }} 个密钥</span>
      </div>

      <a-table :data-source="rows" :loading="loading" :pagination="false" row-key="id" size="small">
        <a-table-column title="名称" data-index="name" :width="180" />
        <a-table-column title="密钥前缀" data-index="key_prefix" :width="180" />
        <a-table-column title="最后使用" :width="200">
          <template #default="{ record }">{{ fmt(record.last_used_at) }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="100">
          <template #default="{ record }">
            <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="160" fixed="right">
          <template #default="{ record }">
            <a-space>
              <a @click="toggle(record)">{{ record.enabled ? '停用' : '启用' }}</a>
              <a class="danger-link" @click="confirmDelete(record)"><DeleteOutlined /> 删除</a>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
    </section>

    <a-modal v-model:open="modalOpen" title="新建密钥" :confirm-loading="saving" @ok="save">
      <a-form layout="vertical">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" placeholder="例如 本地客户端" />
        </a-form-item>
      </a-form>

      <a-alert
        v-if="createdKey"
        type="success"
        show-icon
        message="密钥创建成功"
        description="明文仅展示这一次，请立即复制保存。"
        style="margin-top: 8px"
      />
      <div v-if="createdKey" class="key-box">
        <code>{{ createdKey }}</code>
        <a-button size="small" @click="copyKey"><CopyOutlined /> 复制</a-button>
      </div>
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
.danger-link { color: var(--color-red); }
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
.key-box code { flex: 1; font-size: 12px; }
</style>
