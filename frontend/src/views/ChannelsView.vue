<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, DeleteOutlined, EditOutlined, LinkOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import ModelWhitelistEditor, { type WhitelistRow } from '@/components/ModelWhitelistEditor.vue'
import GroupTag from '@/components/GroupTag.vue'
import { PROTOCOLS, type Channel, type ChannelGroup, type ChannelBinding } from '@/api/types'

type ChannelRow = Channel & { models?: string[]; model_count?: number }

const loading = ref(false)
const rows = ref<ChannelRow[]>([])
const groups = ref<ChannelGroup[]>([])

const modalOpen = ref(false)
const editing = ref<Channel | null>(null)
const saving = ref(false)

const bindOpen = ref(false)
const bindChannel = ref<ChannelRow | null>(null)
// 抽屉里编辑的是整张白名单，点保存时整表提交
const bindItems = ref<WhitelistRow[]>([])
const bindSaving = ref(false)

const form = reactive({
  name: '',
  protocol: 'openai-chat',
  base_url: '',
  api_key: '',
  group_id: 0,
  weight: 1,
  enabled: true,
  // 白名单随渠道一起提交：新建渠道时就把「能跑哪些模型」填完
  models: [] as WhitelistRow[]
})

const title = computed(() => (editing.value ? '编辑渠道' : '新建渠道'))

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为本来就没有渠道
const loadError = ref('')

/** 按分组 ID 取分组对象，用于渲染分组胶囊（颜色随分组配置） */
function groupOf(id: number) {
  return groups.value.find((g) => g.id === id)
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const [c, g] = await Promise.all([
      api.get<{ items: ChannelRow[] }>('/channels'),
      api.get<{ items: ChannelGroup[] }>('/groups')
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
  Object.assign(form, {
    name: '',
    protocol: 'openai-chat',
    base_url: '',
    api_key: '',
    group_id: groups.value.find((x) => x.is_default)?.id ?? groups.value[0]?.id ?? 0,
    weight: 1,
    enabled: true,
    models: [{ public_name: '', upstream_name: '', enabled: true }]
  })
  modalOpen.value = true
}

async function openEdit(row: ChannelRow) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    protocol: row.protocol,
    base_url: row.base_url,
    api_key: '',
    group_id: row.group_id,
    weight: row.weight,
    enabled: row.enabled,
    models: [] as WhitelistRow[]
  })
  modalOpen.value = true
  // 编辑时把已有白名单读出来一起改：白名单是渠道的一部分，
  // 分开两个入口改很容易出现「改了渠道没改模型」的错觉
  try {
    const res = await api.get<{ items: ChannelBinding[] }>('/channels/' + row.id + '/models')
    form.models = (res.items || []).map((b) => ({
      public_name: b.public_name,
      upstream_name: b.upstream_name === b.public_name ? '' : b.upstream_name,
      enabled: b.enabled
    }))
  } catch (e: any) {
    message.error('读取模型白名单失败：' + e.message)
  }
}

// 白名单在提交前先自查一遍：后端也会校验，但等一个来回再报错体验差得多
function whitelistPayload(): WhitelistRow[] | null {
  const rows = form.models
    .map((r) => ({
      public_name: r.public_name.trim(),
      upstream_name: r.upstream_name.trim(),
      enabled: r.enabled
    }))
    .filter((r) => r.public_name || r.upstream_name)
  const seen = new Set<string>()
  for (const r of rows) {
    if (!r.public_name) {
      message.warning('有白名单条目只填了上游模型名，请补上对外模型名')
      return null
    }
    if (seen.has(r.public_name)) {
      message.warning('模型白名单里对外名重复：' + r.public_name)
      return null
    }
    seen.add(r.public_name)
  }
  return rows
}

async function save() {
  if (!form.name.trim() || !form.base_url.trim()) {
    message.warning('渠道名称与地址必填')
    return
  }
  const models = whitelistPayload()
  if (!models) return
  if (!models.length) {
    message.warning('请至少填一个模型：没有白名单的渠道不会参与任何路由')
    return
  }
  saving.value = true
  try {
    const payload: Record<string, unknown> = {
      name: form.name.trim(),
      protocol: form.protocol,
      base_url: form.base_url.trim(),
      group_id: form.group_id,
      weight: form.weight,
      enabled: form.enabled,
      models
    }
    if (form.api_key) payload.api_key = form.api_key
    if (editing.value) {
      await api.put('/channels/' + editing.value.id, payload)
      message.success('更新成功')
    } else {
      await api.post('/channels', payload)
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

async function openBindings(row: ChannelRow) {
  bindChannel.value = row
  bindOpen.value = true
  await loadBindings()
}

async function loadBindings() {
  if (!bindChannel.value) return
  try {
    const res = await api.get<{ items: ChannelBinding[] }>('/channels/' + bindChannel.value.id + '/models')
    bindItems.value = (res.items || []).map((b) => ({
      public_name: b.public_name,
      // 上游名与对外名相同时留空显示，避免满屏重复的模型名
      upstream_name: b.upstream_name === b.public_name ? '' : b.upstream_name,
      enabled: b.enabled
    }))
  } catch (e: any) {
    message.error(e.message)
  }
}

// 整表提交而不是逐条增删：一张表改完一次保存，
// 不会出现「删了两条加了一条只生效一半」的中间状态
async function saveBindings() {
  if (!bindChannel.value) return
  const items = bindItems.value
    .map((r) => ({
      public_name: r.public_name.trim(),
      upstream_name: r.upstream_name.trim() || r.public_name.trim(),
      enabled: r.enabled
    }))
    .filter((r) => r.public_name)
  if (!items.length) {
    message.warning('白名单不能为空：没有模型的渠道不会参与任何路由')
    return
  }
  const names = items.map((r) => r.public_name)
  if (new Set(names).size !== names.length) {
    message.warning('模型白名单里有重复的对外名')
    return
  }
  bindSaving.value = true
  try {
    await api.put('/channels/' + bindChannel.value.id + '/models', { items })
    message.success('白名单已保存')
    await loadBindings()
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    bindSaving.value = false
  }
}

// protocolLabel 把协议常量显示成选项里的中文/英文名，
// 列表里直接摊开 anthropic-messages 这种常量对用户没有意义
function protocolLabel(value: string) {
  return PROTOCOLS.find((p) => p.value === value)?.label ?? value
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
           「操作」列会盖住左边最后一列，表现为表头被截断、内容被压住。
           1170 = 各列宽度之和，实测容器宽 1182（scripts/measure-tables.mjs） -->
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
        <a-table-column title="名称" :width="150">
          <template #default="{ record }">
            <div class="chan-name">{{ record.name }}</div>
          </template>
        </a-table-column>
        <a-table-column title="模型" :width="200" ellipsis>
          <template #default="{ record }">
            <!-- 白名单是模型存在的唯一依据，「一条都没有」必须显眼：
                 这种渠道看着一切正常，实际任何请求都不会路由到它 -->
            <span v-if="!record.model_count" class="unassigned">未配置模型</span>
            <!-- 列宽有限，装不下的模型名走省略号，完整清单放 title（悬停可见）：
                 模型目录现在只存在于这些白名单里，列表是最常用的查看入口 -->
            <span v-else class="model-names" :title="(record.models || []).join('、')">
              {{ (record.models || []).join('、') }}
            </span>
          </template>
        </a-table-column>
        <a-table-column title="上游协议" :width="125">
          <template #default="{ record }">{{ protocolLabel(record.protocol) }}</template>
        </a-table-column>
        <a-table-column title="地址" data-index="base_url" :width="174" ellipsis />
        <a-table-column title="分组" :width="110">
          <template #default="{ record }">
            <!-- 分组名用全站统一的胶囊：颜色与分组管理里配的一致，
                 这样「渠道属于哪个分组」在列表里一眼能认出来 -->
            <GroupTag
              :name="groupOf(record.group_id)?.name ?? String(record.group_id)"
              :color="groupOf(record.group_id)?.color"
            />
          </template>
        </a-table-column>
        <a-table-column title="权重" data-index="weight" :width="58" />
        <a-table-column title="密钥" data-index="api_key_hint" :width="105" />
        <a-table-column title="状态" :width="78">
          <template #default="{ record }">
            <a-tag :color="healthTag(record).color">{{ healthTag(record).text }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="190" fixed="right">
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
        <a-form-item label="上游协议" required>
          <a-select v-model:value="form.protocol" :options="PROTOCOLS" />
          <div class="field-hint">
            客户端用哪种协议请求都行：会先归一成 OpenAI Chat，再按这里选的协议转成上游格式。
          </div>
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
          <div class="field-hint">分组决定路由与权限范围，模型由下面的白名单决定。</div>
        </a-form-item>

        <a-form-item label="模型白名单" required>
          <ModelWhitelistEditor v-model:items="form.models" />
          <div class="field-hint">
            只有写在这里的模型才会被路由到这条渠道；上游名留空表示与对外名相同。
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

    <a-drawer v-model:open="bindOpen" :title="'模型白名单 · ' + (bindChannel?.name ?? '')" width="620">
      <a-space direction="vertical" style="width: 100%" :size="12">
        <a-card size="small" title="这条渠道能跑哪些模型">
          <a-space direction="vertical" style="width: 100%">
            <a-alert
              type="info"
              show-icon
              message="对外名是客户端请求时用的名字；上游名是转发给上游时替换成的名字。客户端写错名字是最常见的 502 原因。"
            />
            <ModelWhitelistEditor v-model:items="bindItems" />
            <a-button type="primary" block :loading="bindSaving" @click="saveBindings">保存白名单</a-button>
          </a-space>
        </a-card>

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
.model-names { color: var(--color-text); }
.muted { color: var(--color-text-secondary); }
.danger-link { color: var(--color-red); }
</style>
