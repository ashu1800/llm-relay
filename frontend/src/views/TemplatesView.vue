<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, EditOutlined, DeleteOutlined, ThunderboltOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import { PROTOCOLS } from '@/api/types'

interface Template {
  id: number
  name: string
  protocol: string
  base_url: string
  group_id: number
}

interface Group {
  id: number
  name: string
}

const loading = ref(false)
const rows = ref<Template[]>([])
const groups = ref<Group[]>([])

const modalOpen = ref(false)
const saving = ref(false)
const editingID = ref<number | null>(null)
const form = reactive({ name: '', protocol: 'openai-chat', base_url: '' })

const applyOpen = ref(false)
const applying = ref(false)
const applyTarget = ref<Template | null>(null)
// 模型白名单不在这个弹窗里配：用模板建完渠道后到「渠道管理」里填
const applyForm = reactive({ name: '', api_key: '', base_url: '', group_id: 0, weight: 1 })

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为模板本来就没有
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<{ items: Template[] }>('/channel-templates')
    rows.value = res.items || []
    const g = await api.get<{ items: Group[] }>('/groups')
    groups.value = g.items || []
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingID.value = null
  Object.assign(form, { name: '', protocol: 'openai-chat', base_url: '' })
  modalOpen.value = true
}

function openEdit(row: Template) {
  editingID.value = row.id
  Object.assign(form, { name: row.name, protocol: row.protocol, base_url: row.base_url })
  modalOpen.value = true
}

async function save() {
  if (!form.name.trim()) {
    message.warning('模板名必填')
    return
  }
  saving.value = true
  try {
    const body = { name: form.name.trim(), protocol: form.protocol, base_url: form.base_url.trim() }
    if (editingID.value) {
      await api.put('/channel-templates/' + editingID.value, body)
      message.success('已更新')
    } else {
      await api.post('/channel-templates', body)
      message.success('已新增')
    }
    modalOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

function confirmDelete(row: Template) {
  Modal.confirm({
    title: '确认删除模板',
    content: '删除模板不会影响已用它创建的渠道。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/channel-templates/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

// 用模板建渠道：模板只提供协议与地址，密钥必须现填，避免凭据落在模板里
function openApply(row: Template) {
  applyTarget.value = row
  const gid = row.group_id || (groups.value[0] ? groups.value[0].id : 0)
  Object.assign(applyForm, {
    name: row.name,
    api_key: '',
    base_url: row.base_url,
    group_id: gid,
    weight: 1
  })
  applyOpen.value = true
}

async function doApply() {
  if (!applyTarget.value) return
  if (!applyForm.api_key.trim()) {
    message.warning('请填写该渠道的上游密钥')
    return
  }
  applying.value = true
  try {
    await api.post('/channel-templates/' + applyTarget.value.id + '/apply', {
      name: applyForm.name.trim(),
      api_key: applyForm.api_key.trim(),
      base_url: applyForm.base_url.trim(),
      group_id: applyForm.group_id,
      weight: applyForm.weight
    })
    message.success('渠道已创建，记得去「渠道管理」绑定模型')
    applyOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    applying.value = false
  }
}

function protocolLabel(v: string) {
  const found = PROTOCOLS.find((p) => p.value === v)
  return found ? found.label : v
}

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建模板</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">共 {{ rows.length }} 个模板</span>
      </div>

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="渠道模板加载失败"
        @retry="load"
      >
      <a-table :data-source="rows" :loading="loading" :pagination="false" row-key="id" size="small">
        <template #emptyText>
          <a-empty description="还没有模板，点「新建模板」把常用的上游地址存下来" />
        </template>
        <a-table-column title="模板名" data-index="name" :width="200" />
        <a-table-column title="协议" :width="200">
          <template #default="{ record }">
            <a-tag>{{ protocolLabel(record.protocol) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="Base URL" data-index="base_url" ellipsis />
        <a-table-column title="操作" :width="230" fixed="right">
          <template #default="{ record }">
            <a-space>
              <a @click="openApply(record)"><ThunderboltOutlined /> 建渠道</a>
              <a @click="openEdit(record)"><EditOutlined /></a>
              <a class="danger-link" @click="confirmDelete(record)"><DeleteOutlined /></a>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
      </DataState>
    </section>

    <a-modal
      v-model:open="modalOpen"
      :title="editingID ? '编辑模板' : '新建模板'"
      :confirm-loading="saving"
      @ok="save"
    >
      <a-form layout="vertical">
        <a-form-item label="模板名" required>
          <a-input v-model:value="form.name" placeholder="例如 OpenAI 官方" />
        </a-form-item>
        <a-form-item label="协议">
          <a-select v-model:value="form.protocol" :options="PROTOCOLS" />
        </a-form-item>
        <a-form-item label="Base URL">
          <a-input v-model:value="form.base_url" placeholder="例如 https://api.openai.com/v1" />
          <div class="field-hint">末尾带不带 /v1 都可以，转发时会自动去重。</div>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="applyOpen"
      :title="'用模板建渠道 · ' + (applyTarget ? applyTarget.name : '')"
      :confirm-loading="applying"
      ok-text="创建渠道"
      @ok="doApply"
    >
      <a-alert
        type="info"
        show-icon
        message="模板只提供协议与地址；上游密钥不随模板保存，需要在这里现填。"
        style="margin-bottom: 12px"
      />
      <a-form layout="vertical">
        <a-form-item label="渠道名称" required>
          <a-input v-model:value="applyForm.name" />
        </a-form-item>
        <a-form-item label="上游密钥" required>
          <a-input-password v-model:value="applyForm.api_key" placeholder="sk-..." />
        </a-form-item>
        <a-form-item label="Base URL">
          <a-input v-model:value="applyForm.base_url" />
          <div class="field-hint">可在此覆盖模板里的地址，例如换成中转站。</div>
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="分组">
              <a-select v-model:value="applyForm.group_id">
                <a-select-option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</a-select-option>
              </a-select>
              <div class="field-hint">建好渠道后到「渠道管理」里填它的模型白名单，请求才能路由过去。</div>
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="权重">
              <a-input-number v-model:value="applyForm.weight" :min="1" style="width: 100%" />
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
  display: flex; align-items: center; gap: var(--gap);
  padding: var(--gap); min-height: 64px;
}
.toolbar-left { display: flex; gap: var(--gap); }
.toolbar-spacer { flex: 1; }
.toolbar-hint { font-size: 12px; color: var(--color-text-secondary); }
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.danger-link { color: var(--color-red); }
</style>
