<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, DeleteOutlined, EditOutlined, LinkOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import { PROTOCOLS, type Channel, type ChannelGroup, type ChannelBinding } from '@/api/types'

const loading = ref(false)
const rows = ref<Channel[]>([])
const groups = ref<ChannelGroup[]>([])

const modalOpen = ref(false)
const editing = ref<Channel | null>(null)
const saving = ref(false)

const bindOpen = ref(false)
const bindChannel = ref<Channel | null>(null)
const bindings = ref<ChannelBinding[]>([])
const bindForm = reactive({ public_name: '', upstream_name: '' })

const form = reactive({
  name: '',
  protocol: 'openai-chat',
  base_url: '',
  api_key: '',
  group_id: 0,
  weight: 1,
  enabled: true
})

const title = computed(() => (editing.value ? '编辑渠道' : '新建渠道'))

async function load() {
  loading.value = true
  try {
    const [c, g] = await Promise.all([
      api.get<{ items: Channel[] }>('/channels'),
      api.get<{ items: ChannelGroup[] }>('/groups')
    ])
    rows.value = c.items || []
    groups.value = g.items || []
    if (!form.group_id && groups.value.length) {
      form.group_id = groups.value.find((x) => x.is_default)?.id ?? groups.value[0].id
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    protocol: 'openai-chat',
    base_url: '',
    api_key: '',
    group_id: groups.value.find((x) => x.is_default)?.id ?? groups.value[0]?.id ?? 0,
    weight: 1,
    enabled: true
  })
  modalOpen.value = true
}

function openEdit(row: Channel) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    protocol: row.protocol,
    base_url: row.base_url,
    api_key: '',
    group_id: row.group_id,
    weight: row.weight,
    enabled: row.enabled
  })
  modalOpen.value = true
}

async function save() {
  if (!form.name.trim() || !form.base_url.trim()) {
    message.warning('渠道名称与地址必填')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      // 密钥留空表示不修改，避免误清空已保存的密钥
      const payload: Record<string, unknown> = { ...form }
      if (!payload.api_key) delete payload.api_key
      await api.put('/channels/' + editing.value.id, payload)
      message.success('更新成功')
    } else {
      await api.post('/channels', form)
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

function confirmDelete(row: Channel) {
  Modal.confirm({
    title: '确认删除渠道',
    content: '将同时解除该渠道下的所有模型绑定，此操作不可撤销。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/channels/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

async function openBindings(row: Channel) {
  bindChannel.value = row
  bindForm.public_name = ''
  bindForm.upstream_name = ''
  bindOpen.value = true
  await loadBindings()
}

async function loadBindings() {
  if (!bindChannel.value) return
  try {
    const res = await api.get<{ items: ChannelBinding[] }>('/channels/' + bindChannel.value.id + '/models')
    bindings.value = res.items || []
  } catch (e: any) {
    message.error(e.message)
  }
}

async function addBinding() {
  if (!bindChannel.value || !bindForm.public_name.trim()) {
    message.warning('请填写对外模型名')
    return
  }
  try {
    await api.post('/channels/' + bindChannel.value.id + '/models', {
      public_name: bindForm.public_name.trim(),
      upstream_name: (bindForm.upstream_name || bindForm.public_name).trim()
    })
    message.success('已绑定')
    bindForm.public_name = ''
    bindForm.upstream_name = ''
    await loadBindings()
    await load()
  } catch (e: any) {
    message.error(e.message)
  }
}

async function removeBinding(b: ChannelBinding) {
  if (!bindChannel.value) return
  try {
    await api.del('/channels/' + bindChannel.value.id + '/models/' + b.id)
    message.success('已解绑')
    await loadBindings()
  } catch (e: any) {
    message.error(e.message)
  }
}

function healthTag(row: Channel) {
  if (!row.enabled) return { color: 'default', text: '已禁用' }
  if (row.health_status === 'healthy') return { color: 'green', text: '正常' }
  if (row.health_status === 'degraded') return { color: 'orange', text: '异常' }
  return { color: 'blue', text: '未探测' }
}

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建渠道</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">共 {{ rows.length }} 个渠道</span>
      </div>

      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="false"
        row-key="id"
        size="small"
        :scroll="{ x: 1100 }"
      >
        <a-table-column title="名称" data-index="name" :width="180" />
        <a-table-column title="协议" data-index="protocol" :width="180" />
        <a-table-column title="地址" data-index="base_url" :width="260" ellipsis />
        <a-table-column title="分组" :width="90">
          <template #default="{ record }">
            {{ groups.find((g) => g.id === record.group_id)?.name ?? record.group_id }}
          </template>
        </a-table-column>
        <a-table-column title="权重" data-index="weight" :width="70" />
        <a-table-column title="密钥" data-index="api_key_hint" :width="150" />
        <a-table-column title="状态" :width="90">
          <template #default="{ record }">
            <a-tag :color="healthTag(record).color">{{ healthTag(record).text }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="200" fixed="right">
          <template #default="{ record }">
            <a-space>
              <a @click="openBindings(record)"><LinkOutlined /> 模型</a>
              <a @click="openEdit(record)"><EditOutlined /> 编辑</a>
              <a class="danger-link" @click="confirmDelete(record)"><DeleteOutlined /> 删除</a>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
    </section>

    <a-modal v-model:open="modalOpen" :title="title" :confirm-loading="saving" width="640px" @ok="save">
      <a-form layout="vertical">
        <a-form-item label="渠道名称" required>
          <a-input v-model:value="form.name" placeholder="例如 ohub-deepseek" />
        </a-form-item>
        <a-form-item label="协议类型" required>
          <a-select v-model:value="form.protocol" :options="PROTOCOLS" />
        </a-form-item>
        <a-form-item label="上游地址" required>
          <a-input v-model:value="form.base_url" placeholder="https://api.example.com 或 https://api.example.com/v1" />
        </a-form-item>
        <a-form-item :label="editing ? 'API Key（留空表示不修改）' : 'API Key'">
          <a-input-password v-model:value="form.api_key" placeholder="sk-..." />
        </a-form-item>
        <a-form-item label="所属分组">
          <a-select v-model:value="form.group_id">
            <a-select-option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</a-select-option>
          </a-select>
        </a-form-item>
        <a-row :gutter="8">
          <a-col :span="12">
            <a-form-item label="路由权重">
              <a-input-number v-model:value="form.weight" :min="1" style="width: 100%" />
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

    <a-drawer v-model:open="bindOpen" :title="'模型绑定 · ' + (bindChannel?.name ?? '')" width="620">
      <a-space direction="vertical" style="width: 100%" :size="12">
        <a-card size="small" title="新增绑定">
          <a-space direction="vertical" style="width: 100%">
            <a-input v-model:value="bindForm.public_name" placeholder="对外模型名，例如 deepseek-v4-flash" />
            <a-input v-model:value="bindForm.upstream_name" placeholder="上游原生模型名（留空则同上）" />
            <a-button type="primary" block @click="addBinding"><PlusOutlined /> 绑定</a-button>
          </a-space>
        </a-card>

        <a-list :data-source="bindings" size="small" bordered>
          <template #renderItem="{ item }">
            <a-list-item>
              <a-list-item-meta>
                <template #title>{{ item.public_name }}</template>
                <template #description>上游：{{ item.upstream_name }}</template>
              </a-list-item-meta>
              <a class="danger-link" @click="removeBinding(item)">解绑</a>
            </a-list-item>
          </template>
        </a-list>
      </a-space>
    </a-drawer>
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
.danger-link { color: var(--color-red); }
</style>
