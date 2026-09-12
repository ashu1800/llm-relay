<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { InputNumber, message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, DeleteOutlined, CopyOutlined, EditOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import type { APIKey, ChannelGroup } from '@/api/types'

const loading = ref(false)
const rows = ref<APIKey[]>([])
const groups = ref<ChannelGroup[]>([])
const modalOpen = ref(false)
const saving = ref(false)
const createdKey = ref('')
const editing = ref<APIKey | null>(null)

const form = reactive({
  name: '',
  rate_limit_rpm: 0,
  enabled: true,
  // 白名单为空数组表示不限制，与后端 StringList 的语义一致
  allowed_models: [] as string[],
  allowed_groups: [] as string[]
})

const title = computed(() => (editing.value ? '编辑密钥' : '新建密钥'))

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为密钥本来就没有
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<{ items: APIKey[] }>('/keys')
    rows.value = res.items || []
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

// 分组白名单的候选项：用分组名作为值，用户也可以自己输入别的值
async function loadGroups() {
  try {
    const res = await api.get<{ items: ChannelGroup[] }>('/groups')
    groups.value = res.items || []
  } catch (e: any) {
    message.error('分组候选加载失败：' + e.message)
  }
}

function openCreate() {
  editing.value = null
  form.name = ''
  form.rate_limit_rpm = 0
  form.enabled = true
  form.allowed_models = []
  form.allowed_groups = []
  createdKey.value = ''
  modalOpen.value = true
}

function openEdit(row: APIKey) {
  editing.value = row
  form.name = row.name
  form.rate_limit_rpm = row.rate_limit_rpm
  form.enabled = row.enabled
  form.allowed_models = [...(row.allowed_models || [])]
  form.allowed_groups = [...(row.allowed_groups || [])]
  createdKey.value = ''
  modalOpen.value = true
}

// 限流额度的展示：0 表示跟随全局默认，负数表示不限
function limitText(v: number) {
  if (v === 0) return '跟随全局默认'
  if (v < 0) return '不限'
  return v + ' 次/分钟'
}

// 白名单展示：空表示不限制
function whitelistText(v: string[] | null) {
  if (!v || v.length === 0) return '不限'
  return v.join('、')
}

// tags 模式可以自由输入，提交前去空白、去重
function cleanList(v: string[]) {
  return Array.from(new Set((v || []).map((s) => String(s).trim()).filter(Boolean)))
}

function setLimit(row: APIKey) {
  let input = String(row.rate_limit_rpm)
  Modal.confirm({
    title: '设置每分钟请求上限 · ' + row.name,
    content: () =>
      h('div', [
        h('p', { style: 'font-size:12px;color:#888;margin-bottom:8px' }, [
          '填 0 表示跟随全局默认，填负数表示这把密钥完全不限流（适合本地压测）。'
        ]),
        // antd 的 InputNumber 组件类型与 h() 的重载对不上（改动前就存在的报错），
        // 这里显式断言绕开类型检查，运行时行为不变
        h(InputNumber as any, {
          defaultValue: row.rate_limit_rpm,
          min: -1,
          max: 1000000,
          style: 'width:100%',
          'onUpdate:value': (v: number) => {
            input = String(v ?? 0)
          }
        })
      ]),
    async onOk() {
      const n = parseInt(input, 10)
      if (!isFinite(n) || n < -1) {
        message.warning('请填 0 或正整数，-1 表示不限')
        return
      }
      try {
        await api.put('/keys/' + row.id, { rate_limit_rpm: n })
        message.success('已更新')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

async function save() {
  if (!form.name.trim()) {
    message.warning('名称必填')
    return
  }
  saving.value = true
  try {
    // 白名单始终显式发送：空数组是有效值（表示清空），不传则后端保持原值
    const body = {
      name: form.name.trim(),
      rate_limit_rpm: form.rate_limit_rpm,
      allowed_models: cleanList(form.allowed_models),
      allowed_groups: cleanList(form.allowed_groups)
    }
    if (editing.value) {
      await api.put('/keys/' + editing.value.id, { ...body, enabled: form.enabled })
      message.success('已更新')
      modalOpen.value = false
      await load()
    } else {
      const res = await api.post<{ key: string }>('/keys', { ...body, enabled: form.enabled })
      // 明文只返回一次，留在弹窗里等用户复制
      createdKey.value = res.key
      await load()
    }
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

onMounted(() => {
  load()
  loadGroups()
})
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

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="密钥列表加载失败"
        @retry="load"
      >
      <a-table :data-source="rows" :loading="loading" :pagination="false" row-key="id" size="small" :scroll="{ x: 1170 }">
        <template #emptyText>
          <a-empty description="还没有密钥，点「新建密钥」创建第一个；明文只在创建时显示一次" />
        </template>
        <a-table-column title="名称" data-index="name" :width="150" />
        <a-table-column title="密钥前缀" data-index="key_prefix" :width="140" />
        <a-table-column title="模型白名单" :width="170" ellipsis>
          <template #default="{ record }">
            <span v-if="whitelistText(record.allowed_models) === '不限'" class="muted">不限</span>
            <span v-else>{{ whitelistText(record.allowed_models) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="分组白名单" :width="150" ellipsis>
          <template #default="{ record }">
            <span v-if="whitelistText(record.allowed_groups) === '不限'" class="muted">不限</span>
            <span v-else>{{ whitelistText(record.allowed_groups) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="最后使用" :width="150">
          <template #default="{ record }">{{ fmt(record.last_used_at) }}</template>
        </a-table-column>
        <a-table-column title="限流" :width="130">
          <template #default="{ record }">{{ limitText(record.rate_limit_rpm) }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="90">
          <template #default="{ record }">
            <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="190" fixed="right">
          <template #default="{ record }">
            <a-space>
              <a @click="openEdit(record)"><EditOutlined /> 编辑</a>
              <a @click="setLimit(record)">改限额</a>
              <a @click="toggle(record)">{{ record.enabled ? '停用' : '启用' }}</a>
              <a class="danger-link" @click="confirmDelete(record)"><DeleteOutlined /> 删除</a>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
      </DataState>
    </section>

    <a-modal v-model:open="modalOpen" :title="title" :confirm-loading="saving" width="600px" @ok="save">
      <a-form layout="vertical">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" placeholder="例如 本地客户端" />
        </a-form-item>
        <a-form-item label="每分钟请求上限">
          <a-input-number v-model:value="form.rate_limit_rpm" :min="-1" :max="1000000" style="width: 100%" />
          <div class="field-hint">0 表示跟随全局默认；负数表示这把密钥完全不限流。</div>
        </a-form-item>
        <a-form-item label="允许调用的模型">
          <a-select
            v-model:value="form.allowed_models"
            mode="tags"
            :token-separators="[',', '，']"
            placeholder="输入模型名后回车，可填多个"
          />
          <div class="field-hint">留空表示不限制；填了则只有列表内的模型可以被这把密钥调用。</div>
        </a-form-item>
        <a-form-item label="允许使用的分组">
          <a-select
            v-model:value="form.allowed_groups"
            mode="tags"
            :token-separators="[',', '，']"
            placeholder="选择分组名，或直接输入分组名 / 分组 ID"
          >
            <a-select-option v-for="g in groups" :key="g.id" :value="g.name">{{ g.name }}</a-select-option>
          </a-select>
          <div class="field-hint">留空表示不限制；可选分组名或分组 ID，也支持下拉里没有的取值。</div>
        </a-form-item>
        <a-form-item label="启用">
          <a-switch v-model:checked="form.enabled" />
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
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.toolbar-left { display: flex; gap: var(--gap); }
.toolbar-spacer { flex: 1; }
.toolbar-hint { color: var(--color-text-secondary); font-size: 13px; }
.muted { color: var(--color-text-secondary); }
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
.key-box code { flex: 1; font-family: var(--font-family-mono); font-size: 12px; }
</style>
