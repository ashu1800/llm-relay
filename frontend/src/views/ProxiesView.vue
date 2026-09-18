<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, ThunderboltOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import { fmtTime } from '@/utils/fmtTime'
import type { Proxy, ProxyTestResult } from '@/api/types'

const loading = ref(false)
const rows = ref<Proxy[]>([])
const loadError = ref('')

const modalOpen = ref(false)
const saving = ref(false)
const editing = ref<Proxy | null>(null)
/** 正在测试的行 id；用 -1 表示正在测弹窗里那份还没保存的配置 */
const testing = ref<number | null>(null)

const form = reactive({
  name: '',
  protocol: 'socks5',
  host: '',
  port: 1080,
  username: '',
  // 编辑时留空表示「沿用原密码」—— 界面上拿不到明文，也不该拿
  password: '',
  enabled: true
})

// 协议换了要给个合理的默认端口：改完协议还得手动改端口是纯粹的摩擦
const protocolOptions = [
  { value: 'socks5', label: 'SOCKS5', port: 1080 },
  { value: 'http', label: 'HTTP', port: 8080 },
  { value: 'https', label: 'HTTPS（TLS 连代理）', port: 8443 }
]

function onProtocolChange(v: string) {
  const hit = protocolOptions.find((p) => p.value === v)
  if (hit) form.port = hit.port
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<{ items: Proxy[] }>('/proxies')
    rows.value = res.items || []
  } catch (e: any) {
    // 不清空列表：这里原来清空过，但那是在治 DataState 互斥链 bug 的
    // 衍生症状（alert 命中时 slot 不渲染，表格消失）。根因修复后
    // （alert 与 slot 同渲染），保留旧数据 + 常驻错误提示是全站统一行为，
    // 用户既看得到「刷新失败了」也还看得见正在看的内容。
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  form.name = ''
  form.protocol = 'socks5'
  form.host = ''
  form.port = 1080
  form.username = ''
  form.password = ''
  form.enabled = true
  modalOpen.value = true
}

function openEdit(row: Proxy) {
  editing.value = row
  form.name = row.name
  form.protocol = row.protocol
  form.host = row.host
  form.port = row.port
  form.username = row.username
  form.password = ''
  form.enabled = row.enabled
  modalOpen.value = true
}

async function save() {
  if (!form.name.trim()) {
    message.warning('请填写代理名称')
    return
  }
  if (!form.host.trim()) {
    message.warning('请填写代理地址')
    return
  }
  saving.value = true
  try {
    const body: Record<string, unknown> = {
      name: form.name.trim(),
      protocol: form.protocol,
      host: form.host.trim(),
      port: Number(form.port) || 0,
      username: form.username.trim(),
      enabled: form.enabled
    }
    // 密码三态：编辑时留空 = 不改（不传这个字段），非空 = 设为新密码。
    // 这里刻意不传空串 —— 那会被后端理解成「清空密码」
    if (form.password) body.password = form.password
    if (editing.value) {
      await api.put('/proxies/' + editing.value.id, body)
      message.success('已保存')
    } else {
      await api.post('/proxies', body)
      message.success('已添加')
    }
    modalOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

function remove(row: Proxy) {
  Modal.confirm({
    centered: true,
    title: '删除代理「' + row.name + '」？',
    content: '已经有渠道在用它时删不掉 —— 要先让那些渠道改用别的代理或直连。',
    okText: '删除',
    okType: 'danger',
    cancelText: '取消',
    async onOk() {
      try {
        await api.del('/proxies/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        message.error(e.message)
      }
    }
  })
}

/** 测试已保存的代理：结果会写回该行，所以测完要重新拉列表 */
async function testRow(row: Proxy) {
  testing.value = row.id
  try {
    const res = await api.post<ProxyTestResult>('/proxies/' + row.id + '/test', {})
    reportTest(res, row.name)
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    testing.value = null
  }
}

/** 测试弹窗里这份还没保存的配置 */
async function testDraft() {
  if (!form.host.trim()) {
    message.warning('请先填写代理地址')
    return
  }
  testing.value = -1
  try {
    const res = await api.post<ProxyTestResult>('/proxies/test', {
      id: editing.value?.id || 0,
      protocol: form.protocol,
      host: form.host.trim(),
      port: Number(form.port) || 0,
      username: form.username.trim(),
      password: form.password
    })
    reportTest(res, form.name || '该配置')
  } catch (e: any) {
    message.error(e.message)
  } finally {
    testing.value = null
  }
}

function reportTest(res: ProxyTestResult, who: string) {
  if (res.ok) {
    message.success(who + ' 连通正常，耗时 ' + res.latency_ms + ' ms')
  } else {
    // 失败原因用 Modal 而不是 message：错误信息往往有几十个字，
    // 一闪而过的提示根本读不完，用户还得再点一次才能看到
    Modal.error({
      title: who + ' 连通失败',
      content: res.error || '未知错误（耗时 ' + res.latency_ms + ' ms）'
    })
  }
}

const statusMeta: Record<string, { color: string; text: string }> = {
  ok: { color: 'green', text: '正常' },
  fail: { color: 'red', text: '不通' },
  unknown: { color: 'default', text: '未测试' }
}

function statusOf(row: Proxy) {
  return statusMeta[row.last_status] || statusMeta.unknown
}

function addressOf(row: Proxy) {
  return row.host + ':' + row.port
}

function testedAt(row: Proxy) {
  if (!row.last_tested_at) return '-'
  // 与全站统一口径（原来是 YYYY/MM/DD HH:mm，又一副长相）
  return fmtTime(row.last_tested_at)
}

const activeCount = computed(() => rows.value.filter((r) => r.enabled).length)

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建代理</a-button>
        </div>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">共 {{ rows.length }} 个代理，启用 {{ activeCount }} 个</span>
      </div>

      <!-- DataState 的契约是 error / hasData / loading / title / hint / empty / emptyText。
           这里原来传的是 :empty 与 empty-text（连字符写法）—— 两个都不是它认识的
           prop，而且 DataState 的根节点是 Fragment，属性没法透传下来，
           于是 Vue 每次挂载都会报 "Extraneous non-props attributes"，
           想显示的「还没有配置代理」也从未生效（一路退到 antd 的「暂无数据」）。 -->
      <DataState
        :loading="loading"
        :error="loadError"
        :has-data="rows.length > 0"
        :empty="!loading && !loadError && rows.length === 0"
        empty-text="还没有配置代理"
      >
        <!-- scroll.x 必须不小于各列宽度之和（名称 130 + 协议 80 + 地址 210
             + 状态 200 + 启用 70 + 操作 104 = 794）：
             声明偏大偏小都不行 —— 偏小会让右侧固定的「操作」列压住左邻列，
             偏大则 antd 会把多出来的宽度摊到各列上，量出来的数与声明对不上。
             操作 190 -> 104 是图标按钮那一次（见下方操作列的注释）。
             核对脚本：scripts/check-table-widths.mjs -->
        <a-table
          :data-source="rows"
          row-key="id"
          size="small"
          :pagination="false"
          :scroll="{ x: 794 }"
        >
          <a-table-column title="名称" :width="130">
            <template #default="{ record }">
              <span class="proxy-name">{{ record.name }}</span>
            </template>
          </a-table-column>
          <a-table-column title="协议" :width="80">
            <template #default="{ record }">
              <a-tag>{{ record.protocol }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="地址" :width="210">
            <template #default="{ record }">
              <span class="proxy-addr">{{ addressOf(record) }}</span>
              <div v-if="record.username" class="sub-text">用户：{{ record.username }}</div>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="200">
            <template #default="{ record }">
              <a-tag :color="statusOf(record).color">
                {{ statusOf(record).text }}
                <template v-if="record.last_status === 'ok'"> {{ record.last_latency_ms }}ms</template>
              </a-tag>
              <!-- 测试时间与失败原因都铺在状态下面：单看一个红标签，
                   用户不知道是「刚测的」还是「三天前的」，也不知道为什么不通 -->
              <div v-if="record.last_tested_at" class="sub-text">{{ testedAt(record) }}</div>
              <div
                v-if="record.last_status === 'fail' && record.last_error"
                class="sub-text fail-text"
                :title="record.last_error"
              >
                {{ record.last_error }}
              </div>
            </template>
          </a-table-column>
          <a-table-column title="启用" :width="70">
            <template #default="{ record }">
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="104" fixed="right">
            <template #default="{ record }">
              <!-- 三个动作改成图标按钮，与渠道页同一个写法（.table-icon-btn）。
                   带文字的 link 按钮实测占 181px，列宽因此写着 190；
                   图标按钮每个 28px，三个加间距共 92px，列宽 104 就够。
                   测试按钮保留转圈反馈（:loading），与渠道页一致。
                   tooltip 与 aria-label 是图标按钮的动作名，缺一不可。 -->
              <a-space :size="4">
                <a-tooltip title="测试连通性：用当前配置连一次">
                  <a-button
                    class="table-icon-btn"
                    type="text"
                    size="small"
                    :loading="testing === record.id"
                    :disabled="testing !== null && testing !== record.id"
                    :aria-label="'测试代理 ' + record.name"
                    @click="testRow(record)"
                  >
                    <ThunderboltOutlined />
                  </a-button>
                </a-tooltip>
                <a-tooltip title="编辑代理">
                  <a-button
                    class="table-icon-btn"
                    type="text"
                    size="small"
                    :aria-label="'编辑代理 ' + record.name"
                    @click="openEdit(record)"
                  >
                    <EditOutlined />
                  </a-button>
                </a-tooltip>
                <a-tooltip title="删除代理">
                  <a-button
                    class="table-icon-btn is-danger"
                    type="text"
                    size="small"
                    :aria-label="'删除代理 ' + record.name"
                    @click="remove(record)"
                  >
                    <DeleteOutlined />
                  </a-button>
                </a-tooltip>
              </a-space>
            </template>
          </a-table-column>
        </a-table>
      </DataState>
    </section>

    <a-modal
      v-model:open="modalOpen"
      :title="editing ? '编辑代理' : '新建代理'"
      :confirm-loading="saving"
      :width="'min(560px, 94vw)'"
      centered
      @ok="save"
    >
      <a-form layout="vertical">
        <a-form-item label="名称">
          <a-input v-model:value="form.name" placeholder="例如：日本节点" />
        </a-form-item>
        <a-form-item label="协议">
          <a-select v-model:value="form.protocol" :options="protocolOptions" @change="onProtocolChange" />
          <div class="field-hint">
            HTTPS 表示「用 TLS 连到代理本身」，不是「代理转发 HTTPS」——后者 HTTP 协议也能做。
          </div>
        </a-form-item>
        <div class="two-col">
          <a-form-item label="地址">
            <a-input v-model:value="form.host" placeholder="127.0.0.1 或 proxy.example.com" />
          </a-form-item>
          <a-form-item label="端口">
            <a-input-number v-model:value="form.port" :min="1" :max="65535" style="width: 100%" />
          </a-form-item>
        </div>
        <div class="two-col">
          <a-form-item label="用户名（可选）">
            <a-input v-model:value="form.username" autocomplete="off" />
          </a-form-item>
          <a-form-item label="密码（可选）">
            <a-input-password
              v-model:value="form.password"
              autocomplete="new-password"
              :placeholder="editing && editing.has_password ? '留空表示不修改' : ''"
            />
          </a-form-item>
        </div>
        <a-form-item label="启用">
          <a-switch v-model:checked="form.enabled" />
          <span class="field-hint inline">停用后，指向它的渠道会直接连不上（不会自动改成直连）</span>
        </a-form-item>
      </a-form>
      <template #footer>
        <div class="modal-footer">
          <a-button :loading="testing === -1" @click="testDraft">
            <ThunderboltOutlined /> 测试连通性
          </a-button>
          <span class="footer-right">
            <a-button @click="modalOpen = false">取消</a-button>
            <a-button type="primary" :loading="saving" @click="save">保存</a-button>
          </span>
        </div>
      </template>
    </a-modal>
  </div>
</template>

<style scoped>
/* 工具条与表格的容器样式跟分组管理页保持一致 ——
   各页各写一套的话，工具栏高度、间距会一页一个样 */
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
.proxy-name { font-weight: 500; }
.proxy-addr { font-family: var(--font-family-mono); font-size: 12px; }
/* 变量名按 styles/theme.css 里真实存在的写：
   那里没有 --color-danger / --color-text-tertiary 这两个名字，
   写了不会报错，只是颜色悄悄失效、退回默认色 */
.sub-text { color: var(--color-text-secondary); font-size: 12px; margin-top: 2px; }
.fail-text {
  color: var(--text-red);
  /* 上游错误动辄几 KB，不截断会把整行撑得很高；完整内容放 title 悬停可见 */
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
}
.disabled { color: var(--color-text-secondary); cursor: not-allowed; }
.field-hint { color: var(--color-text-secondary); font-size: 12px; margin-top: 4px; }
.field-hint.inline { margin-left: 8px; }
.two-col { display: grid; grid-template-columns: 1fr 140px; gap: 12px; }
.modal-footer { display: flex; align-items: center; justify-content: space-between; }
.footer-right { display: flex; gap: 8px; }
</style>

