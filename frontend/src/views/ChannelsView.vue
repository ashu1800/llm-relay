<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  PlusOutlined,
  ReloadOutlined,
  DeleteOutlined,
  EditOutlined,
  LinkOutlined,
  ThunderboltOutlined,
  CloudDownloadOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import ModelWhitelistEditor, { type WhitelistRow } from '@/components/ModelWhitelistEditor.vue'
// Proxy 只用于代理下拉的选项类型
import type { Proxy } from '@/api/types'
import GroupTag from '@/components/GroupTag.vue'
import ChannelIcon from '@/components/ChannelIcon.vue'
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
  // 出站代理：0 = 直连。模型条目上还能单独覆盖（见白名单里的「代理」列）
  proxy_id: 0,
  // 渠道图标：data URI / 图片地址 / 一两个字符，空表示用默认图标
  icon: '',
  // 单渠道并发上限。新建时给 10：不限并发会让一条渠道把上游打满，
  // 而用户多半没意识到「不限」就是当前的行为
  max_concurrency: 10,
  // 白名单随渠道一起提交：新建渠道时就把「能跑哪些模型」填完
  models: [] as WhitelistRow[]
})

// 代理列表：渠道表单与白名单里的「代理」列共用
const proxies = ref<Proxy[]>([])

const title = computed(() => (editing.value ? '编辑渠道' : '新建渠道'))

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为本来就没有渠道
const loadError = ref('')

/** 按分组 ID 取分组对象，用于渲染分组胶囊（颜色随分组配置） */
function groupOf(id: number) {
  return groups.value.find((g) => g.id === id)
}

/** 代理名；直连（0）或代理已被删掉时返回空，列表里就不显示这一行 */
function proxyName(id: number) {
  if (!id) return ''
  return proxies.value.find((p) => p.id === id)?.name || '已删除的代理 #' + id
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const [c, g, px] = await Promise.all([
      api.get<{ items: ChannelRow[] }>('/channels'),
      api.get<{ items: ChannelGroup[] }>('/groups'),
      api.get<{ items: Proxy[] }>('/proxies')
    ])
    rows.value = c.items || []
    groups.value = g.items || []
    proxies.value = px.items || []
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
    proxy_id: 0,
    icon: '',
    max_concurrency: 10,
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
    proxy_id: row.proxy_id || 0,
    icon: row.icon || '',
    // 没配过并发上限的渠道读出来是 0（不限制），如实显示 ——
    // 强行显示成 10 会让用户以为它一直是 10
    max_concurrency: Number((row.extra_config as any)?.max_concurrency) || 0,
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
      enabled: b.enabled,
      proxy_id: b.proxy_id || 0
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
      enabled: r.enabled,
      proxy_id: r.proxy_id || 0
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

// nextExtraConfig 在原有扩展配置上只改并发这一项。
//
// 单独提交一个 { max_concurrency } 会把 headers 等键整体覆盖掉 ——
// 「改个并发把自定义请求头弄没了」是那种当场看不出、过几天才发作的问题。
function nextExtraConfig(): Record<string, unknown> {
  const extra: Record<string, unknown> = { ...((editing.value?.extra_config as any) || {}) }
  if (form.max_concurrency > 0) {
    extra.max_concurrency = form.max_concurrency
  } else {
    delete extra.max_concurrency
  }
  return extra
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
      proxy_id: form.proxy_id || 0,
      // 总是带上：空字符串的语义是「清空图标，回到默认」，
      // 不传的话用户就没法把自定义图标去掉
      icon: form.icon.trim(),
      // extra_config 里有别的键（自定义请求头等），必须整个带着走，
      // 否则改一次并发就把它们抹掉了
      extra_config: nextExtraConfig(),
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

// 从上游抓图标。只能在编辑已有渠道时用 —— 触发方式是把当前表单里的地址
// 交给后端的抓取接口，所以它不依赖「先保存」。
const iconFetching = ref(false)

async function fetchIcon() {
  if (!editing.value) {
    message.info('先保存渠道，再抓取图标')
    return
  }
  if (!form.base_url.trim()) {
    message.warning('先填上游地址')
    return
  }
  iconFetching.value = true
  try {
    // 用当前表单里的地址去抓（而不是库里那份）：用户刚改完地址就点抓取，
    // 期望的是从新地址抓
    const res = await api.post<{ ok: boolean; icon: string; error?: string }>(
      '/channels/' + editing.value.id + '/icon',
      { icon: '', base_url: form.base_url.trim() }
    )
    if (res.ok) {
      form.icon = res.icon
      message.success('已获取图标，保存后生效')
    } else {
      Modal.warning({ title: '没能从上游取到图标', content: res.error || '未知原因', width: 520 })
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    iconFetching.value = false
  }
}

// 往渠道发一句 "hi"：后端用真实转发链路（协议转换、鉴权、代理）发一次最小请求。
// 结果的展示方式跟着结果走 —— 成功一条 message 就够，
// 失败要用 Modal：上游的错误信息往往有几十个字，一闪而过读不完
const testingId = ref(0)

interface ChannelTestResult {
  ok: boolean
  status_code?: number
  latency_ms: number
  model?: string
  upstream_model?: string
  reply?: string
  error?: string
}

async function testChannel(row: ChannelRow) {
  if (testingId.value) return
  testingId.value = row.id
  try {
    const res = await api.post<ChannelTestResult>('/channels/' + row.id + '/test', {})
    if (res.ok) {
      Modal.success({
        title: row.name + ' 连通正常（' + res.latency_ms + ' ms）',
        content: h('div', [
          h('div', '模型：' + (res.model || '-') + (res.upstream_model && res.upstream_model !== res.model ? ' → ' + res.upstream_model : '')),
          h('div', res.reply ? '回复：' + res.reply : '上游返回 ' + (res.status_code || 200) + '，但没有正文（推理型模型可能把内容放在 reasoning 里）')
        ])
      })
    } else {
      Modal.error({
        title: row.name + ' 连通失败' + (res.status_code ? '（HTTP ' + res.status_code + '）' : ''),
        content: res.error || '未知错误',
        width: 560
      })
    }
    // 后端会把这次结果写进 health_status，列表要跟着刷新
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    testingId.value = 0
  }
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
      enabled: b.enabled,
      proxy_id: b.proxy_id || 0
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
      enabled: r.enabled,
      proxy_id: r.proxy_id || 0
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
           1230 = 各列宽度之和（名称列 150 -> 170 是为了放下「经 xxx」那行代理信息，
           操作列 190 -> 230 是为了放下「测试」），实测容器宽 1182（scripts/measure-tables.mjs） -->
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="false"
        row-key="id"
        size="small"
        :scroll="{ x: 1230 }"
      >
        <template #emptyText>
          <a-empty description="还没有渠道，点「新建渠道」添加第一个" />
        </template>
        <a-table-column title="名称" :width="170">
          <template #default="{ record }">
            <div class="chan-title">
              <ChannelIcon :name="record.name" :icon="record.icon" :size="20" />
              <span class="chan-name">{{ record.name }}</span>
            </div>
            <!-- 走了代理的渠道要能一眼看出来：排查「为什么这条渠道的错误
                 和别的渠道不一样」时，第一件事就是确认它的出口 -->
            <div v-if="proxyName(record.proxy_id)" class="sub-text">
              经 {{ proxyName(record.proxy_id) }}
            </div>
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
        <a-table-column title="操作" :width="230" fixed="right">
          <template #default="{ record }">
            <a-space>
              <a :class="{ disabled: testingId === record.id }" @click="testChannel(record)">
                <ThunderboltOutlined />
                {{ testingId === record.id ? '测试中…' : '测试' }}
              </a>
              <a @click="openBindings(record)"><LinkOutlined /> 模型</a>
              <a @click="openEdit(record)"><EditOutlined /> 编辑</a>
              <a class="danger-link" @click="confirmDelete(record)"><DeleteOutlined /> 删除</a>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
      </DataState>
    </section>

    <a-modal v-model:open="modalOpen" :title="title" :confirm-loading="saving" width="720px" @ok="save">
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

        <a-form-item label="渠道图标">
          <div class="icon-row">
            <ChannelIcon :name="form.name" :icon="form.icon" :size="28" />
            <a-input
              v-model:value="form.icon"
              placeholder="图片地址 / data URI，或者直接写一个 emoji"
              allow-clear
            />
            <a-button :loading="iconFetching" @click="fetchIcon">
              <CloudDownloadOutlined /> 从上游获取
            </a-button>
          </div>
          <div class="field-hint">
            「从上游获取」会去渠道的上游站点抓一次 favicon；抓不到就用默认图标（渠道名首字母）。
            也可以直接填一个 emoji 当图标。
          </div>
        </a-form-item>

        <a-row :gutter="8">
          <a-col :span="14">
            <a-form-item label="出站代理">
              <a-select v-model:value="form.proxy_id">
                <a-select-option :value="0">直连（不使用代理）</a-select-option>
                <a-select-option v-for="p in proxies" :key="p.id" :value="p.id">
                  {{ p.name }}（{{ p.protocol }}://{{ p.host }}:{{ p.port }}）{{ p.enabled ? '' : ' · 已停用' }}
                </a-select-option>
              </a-select>
              <div class="field-hint">代理不可用时请求直接失败，不会悄悄改成直连。</div>
            </a-form-item>
          </a-col>
          <a-col :span="10">
            <a-form-item label="并发上限">
              <a-input-number v-model:value="form.max_concurrency" :min="0" style="width: 100%" />
              <div class="field-hint">0 表示不限制。</div>
            </a-form-item>
          </a-col>
        </a-row>

        <a-form-item label="模型白名单与映射" required>
          <ModelWhitelistEditor v-model:items="form.models" :proxies="proxies" />
          <div class="field-hint">
            只有写在这里的模型才会被路由到这条渠道。「模型映射」把客户端请求的模型名
            换成上游真正认识的模型名，留空表示同名；需要单独出口的模型可以在「代理」列覆盖渠道设置。
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
            <ModelWhitelistEditor v-model:items="bindItems" :proxies="proxies" />
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
.chan-title { display: flex; align-items: center; gap: 6px; margin-bottom: 2px; }
.icon-row { display: flex; align-items: center; gap: 8px; }
.chan-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 名称下方的「经 xxx」代理提示：比正文弱一档，不抢渠道名的注意力 */
.sub-text { color: var(--color-text-secondary); font-size: 12px; }
.model-names { color: var(--color-text); }
.muted { color: var(--color-text-secondary); }
.danger-link { color: var(--color-red); }
.disabled { color: var(--color-text-secondary); cursor: not-allowed; }
</style>
