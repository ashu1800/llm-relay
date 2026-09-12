<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, DeleteOutlined, EditOutlined, LinkOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import { useProviderStore } from '@/stores/providers'
import ProviderTag from '@/components/ProviderTag.vue'
import DataState from '@/components/DataState.vue'
import { PROTOCOLS, type Channel, type ChannelGroup, type ChannelBinding } from '@/api/types'

const loading = ref(false)
const providerStore = useProviderStore()
const rows = ref<Channel[]>([])
const groups = ref<ChannelGroup[]>([])

const modalOpen = ref(false)
const editing = ref<Channel | null>(null)
const saving = ref(false)

const bindOpen = ref(false)
const bindChannel = ref<Channel | null>(null)
// 正在绑定的渠道所属分组限定的模型商（0 = 不限）
const bindGroupProvider = computed(
  () => groups.value.find((g) => g.id === bindChannel.value?.group_id)?.provider_id ?? 0
)
const bindings = ref<ChannelBinding[]>([])
const bindForm = reactive({ public_name: '', upstream_name: '' })

const form = reactive({
  name: '',
  protocol: 'openai-chat',
  base_url: '',
  api_key: '',
  group_id: 0,
  // 0 = 未指定。渠道的模型商只是一枚徽标（聚合站本来就不专属于某一家），
  // 所以允许留空；以前表单里根本没有这一项，后端又把空值兜底成 1，
  // 于是所有渠道都挂着 OpenAI 徽标。
  provider_id: 0,
  weight: 1,
  enabled: true
})

// 模型商默认值只在「用户还没自己选过」时跟着分组走：
// 分组按模型商命名（DeepSeek / OpenAI）时，换分组顺手把模型商也切过去；
// 他一旦手动选过，就不再覆盖他的选择。
const providerTouched = ref(false)

function groupName(id: number): string {
  return groups.value.find((g) => g.id === id)?.name ?? ''
}

/** 按分组名猜一个模型商；猜不出来就是 0（未指定），不兜底成 OpenAI */
function defaultProviderID(groupID: number): number {
  return providerStore.matchByText(groupName(groupID))?.id ?? 0
}

// 分组限定了模型商时，渠道必须跟着它 —— 分组的模型商就是这个组的路由范围，
// 渠道自己填一个不一样的只会让徽标和实际能跑的模型对不上。
// 后端同样会拒（分组与渠道模型商不一致时返回 400），这里先一步做在界面上。
const groupProviderID = computed(
  () => groups.value.find((g) => g.id === form.group_id)?.provider_id ?? 0
)

function onGroupChange() {
  if (groupProviderID.value) {
    form.provider_id = groupProviderID.value
    return
  }
  if (providerTouched.value) return
  form.provider_id = defaultProviderID(form.group_id)
}

const title = computed(() => (editing.value ? '编辑渠道' : '新建渠道'))

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为本来就没有渠道
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const [c, g] = await Promise.all([
      api.get<{ items: Channel[] }>('/channels'),
      api.get<{ items: ChannelGroup[] }>('/groups'),
      providerStore.ensure()
    ])
    rows.value = c.items || []
    groups.value = g.items || []
    if (!form.group_id && groups.value.length) {
      form.group_id = groups.value.find((x) => x.is_default)?.id ?? groups.value[0].id
    }
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  const gid = groups.value.find((x) => x.is_default)?.id ?? groups.value[0]?.id ?? 0
  providerTouched.value = false
  Object.assign(form, {
    name: '',
    protocol: 'openai-chat',
    base_url: '',
    api_key: '',
    group_id: gid,
    provider_id: groups.value.find((g) => g.id === gid)?.provider_id || defaultProviderID(gid),
    weight: 1,
    enabled: true
  })
  modalOpen.value = true
}

function openEdit(row: Channel) {
  editing.value = row
  // 编辑时以库里的值为准：改分组不再重算模型商，免得把已保存的值带偏
  providerTouched.value = true
  Object.assign(form, {
    name: row.name,
    protocol: row.protocol,
    base_url: row.base_url,
    api_key: '',
    group_id: row.group_id,
    provider_id: row.provider_id,
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

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="渠道列表加载失败"
        @retry="load"
      >
      <!-- scroll.x 必须不小于各列宽度之和：声明偏小时，固定在右侧的
           「操作」列会盖住左边最后一列，表现为表头被截断、内容被压住 -->
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="false"
        row-key="id"
        size="small"
        :scroll="{ x: 1170 }"
      >
        <template #emptyText>
          <a-empty description="还没有渠道，点「新建渠道」添加第一个" />
        </template>
        <a-table-column title="名称" :width="200">
          <template #default="{ record }">
            <div class="chan-name">{{ record.name }}</div>
            <ProviderTag
              v-if="providerStore.byId(record.provider_id)"
              :code="providerStore.byId(record.provider_id)?.code"
              :name="providerStore.byId(record.provider_id)?.name"
            />
            <!-- 未指定（0，聚合站常见）时给占位，不硬凑一个模型商标签 -->
            <span v-else class="unassigned" title="未指定模型商">—</span>
          </template>
        </a-table-column>
        <a-table-column title="协议" data-index="protocol" :width="150" />
        <a-table-column title="地址" data-index="base_url" :width="220" ellipsis />
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
      </DataState>
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
          <a-select v-model:value="form.group_id" @change="onGroupChange">
            <a-select-option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</a-select-option>
          </a-select>
        </a-form-item>
        <a-form-item label="模型商">
          <a-select
            v-model:value="form.provider_id"
            :disabled="groupProviderID !== 0"
            @change="providerTouched = true"
          >
            <a-select-option :value="0">未指定</a-select-option>
            <a-select-option v-for="p in providerStore.items" :key="p.id" :value="p.id">
              <ProviderTag :code="p.code" :name="p.name" />
            </a-select-option>
          </a-select>
          <div class="field-hint">
            <template v-if="groupProviderID">
              跟随分组：该分组限定只跑 {{ providerStore.byId(groupProviderID)?.name }} 的模型。
            </template>
            <template v-else>
              只决定渠道名称下方的模型商徽标；聚合站这类不专属于某家的渠道留「未指定」即可。
            </template>
          </div>
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
            <a-alert
              v-if="bindGroupProvider"
              type="info"
              show-icon
              :message="'该渠道所在分组限定只跑 ' + providerStore.byId(bindGroupProvider)?.name + ' 的模型，绑定其它模型商的模型会被拒绝。'"
            />
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
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.unassigned { color: var(--color-text-secondary); }
.chan-name { margin-bottom: 2px; }
.danger-link { color: var(--color-red); }
</style>
