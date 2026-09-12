<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, DownloadOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import type { Paged, RequestLog } from '@/api/types'

const loading = ref(false)
const rows = ref<RequestLog[]>([])
const total = ref(0)
const detailOpen = ref(false)
const current = ref<RequestLog | null>(null)
interface LogPayload {
  request_body?: string
  response_body?: string
  request_headers?: Record<string, string>
  response_headers?: Record<string, string>
}
const payload = ref<LogPayload | null>(null)
// 简略 / 详细 切换，对应参考站的 segmented 控件
const dense = ref<string>('brief')

const query = reactive({
  page: 1,
  page_size: 50,
  model: '',
  trace_id: '',
  // '' 表示不限；'success' / 'error' 走状态码区间，其余按精确状态码
  status: '',
  // '' 表示不限时间范围
  range: '24h'
})

const rangeOptions = [
  { value: '1h', label: '近 1 小时' },
  { value: '24h', label: '近 24 小时' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
  { value: '', label: '不限时间' }
]

const statusOptions = [
  { value: '', label: '全部状态' },
  { value: 'success', label: '仅成功' },
  { value: 'error', label: '仅失败' },
  { value: '429', label: '429 限流' },
  { value: '502', label: '502 上游错误' }
]

// rangeToSince 把「近 N 小时/天」换算成绝对时刻。
// 不用「N 小时前的此刻」之外的写法：后端按 created_at 过滤，
// 相对区间每次请求都变会导致翻页时结果漂移，所以要固定成绝对时间。
function rangeToSince(range: string): string {
  if (!range) return ''
  const hours: Record<string, number> = { '1h': 1, '24h': 24, '7d': 24 * 7, '30d': 24 * 30 }
  const h = hours[range]
  if (!h) return ''
  return new Date(Date.now() - h * 3600 * 1000).toISOString()
}

// buildParams 在列表与导出之间共用：两边条件必须完全一致，
// 否则「导出的」和「看到的」不是一回事，排障时最容易被误导。
function buildParams(includePaging: boolean): URLSearchParams {
  const params = new URLSearchParams()
  if (includePaging) {
    params.set('page', String(query.page))
    params.set('page_size', String(query.page_size))
  }
  if (query.model.trim()) params.set('model', query.model.trim())
  if (query.trace_id.trim()) params.set('trace_id', query.trace_id.trim())
  if (query.status === 'success' || query.status === 'error') {
    params.set('status_class', query.status)
  } else if (query.status.trim()) {
    params.set('status', query.status.trim())
  }
  const since = rangeToSince(query.range)
  if (since) params.set('since', since)
  return params
}

// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 表格紧接着显示「暂无数据」，用户会以为这段时间本来就没有调用
const loadError = ref('')

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<Paged<RequestLog>>('/logs?' + buildParams(true).toString())
    rows.value = res.items || []
    total.value = res.total || 0
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  load()
}

// 结构化报文格式化后展示；SSE 流不是合法 JSON，原样返回
function pretty(s?: string) {
  if (!s) return '（空）'
  const t = s.trim()
  if (!t.startsWith('{') && !t.startsWith('[')) return s
  try {
    return JSON.stringify(JSON.parse(t), null, 2)
  } catch {
    return s
  }
}

function prettyHeaders(h?: Record<string, string>) {
  if (!h) return '（无）'
  return Object.entries(h)
    .map(([k, v]) => k + ': ' + v)
    .sort()
    .join('\n')
}

async function openDetail(row: RequestLog) {
  current.value = row
  payload.value = null
  detailOpen.value = true
  try {
    const res = await api.get<{ log: RequestLog; payload: any }>('/logs/' + row.id)
    payload.value = res.payload || null
  } catch {
    // 报文未留存时静默处理，不影响查看基础信息
  }
}

function statusColor(code: number) {
  if (code >= 200 && code < 300) return 'green'
  if (code === 429) return 'orange'
  if (code >= 400) return 'red'
  return 'default'
}

// 命中率分母为全部输入 = 未命中 + 命中 + 缓存写入，与看板口径保持一致
function cacheRate(row: RequestLog) {
  const denom = row.prompt_tokens + row.cached_tokens + row.cache_creation_tokens
  if (denom <= 0) return '-'
  return ((row.cached_tokens / denom) * 100).toFixed(1) + '%'
}

function fmtTime(t: string) {
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

function fmtMs(v: number) {
  if (!v) return '-'
  return v >= 1000 ? (v / 1000).toFixed(2) + 's' : v + 'ms'
}

function fmtCost(v: string) {
  const n = Number(v)
  return n > 0 ? '$' + n.toFixed(6) : '-'
}

// exportCsv 导出当前筛选条件下的**全部**日志。
//
// 原来只把当前页（默认 50 条）拼成 CSV，用户点「导出」拿到的文件却像是全部记录；
// 这种「看起来成功、实际只有一小部分」的问题不会报错，只会让人得出错误结论。
// 现在由服务端按同一套筛选条件全量导出，并把实际条数回报给用户。
const exporting = ref(false)

async function exportCsv() {
  exporting.value = true
  try {
    const res = await fetch('/api/admin/logs/export?' + buildParams(false).toString())
    if (!res.ok) {
      const text = await res.text()
      let msg = '导出失败 ' + res.status
      try {
        msg = JSON.parse(text)?.error?.message || msg
      } catch {
        // 非 JSON 错误体，保留默认文案
      }
      throw new Error(msg)
    }
    const blob = await res.blob()
    const exported = Number(res.headers.get('X-Exported-Count') || 0)
    const total = Number(res.headers.get('X-Total-Count') || 0)
    const truncated = res.headers.get('X-Exported-Truncated') === 'true'

    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'request-logs.csv'
    a.click()
    URL.revokeObjectURL(a.href)

    if (truncated) {
      // 被截断时必须说出来，否则用户会以为拿到了全部
      message.warning('已导出 ' + exported + ' 条，但符合条件的有 ' + total + ' 条，结果被截断', 6)
    } else {
      message.success('已导出 ' + exported + ' 条')
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    exporting.value = false
  }
}

const pagination = computed(() => ({
  current: query.page,
  pageSize: query.page_size,
  total: total.value,
  showSizeChanger: true,
  pageSizeOptions: ['20', '50', '100'],
  showTotal: (t: number) => '共 ' + t + ' 条',
  onChange: (p: number, ps: number) => {
    query.page = p
    query.page_size = ps
    load()
  }
}))

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
          <a-button :loading="exporting" @click="exportCsv"><DownloadOutlined /> 导出</a-button>
        </div>
        <a-input
          v-model:value="query.model"
          placeholder="按模型筛选"
          allow-clear
          style="width: 170px"
          @press-enter="search"
        >
          <template #prefix><SearchOutlined /></template>
        </a-input>
        <a-input
          v-model:value="query.trace_id"
          placeholder="trace_id 精确查找"
          allow-clear
          style="width: 200px"
          @press-enter="search"
        />
        <a-select
          v-model:value="query.status"
          :options="statusOptions"
          style="width: 130px"
          @change="search"
        />
        <a-select
          v-model:value="query.range"
          :options="rangeOptions"
          style="width: 130px"
          @change="search"
        />
        <a-button type="primary" @click="search">查询</a-button>
        <div class="toolbar-spacer" />
        <a-segmented
          v-model:value="dense"
          :options="[{ label: '简略', value: 'brief' }, { label: '详细', value: 'full' }]"
        />
      </div>

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="请求日志加载失败"
        @retry="load"
      >
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="pagination"
        row-key="id"
        size="small"
        :scroll="{ x: 1140 }"
      >
        <template #emptyText>
          <a-empty description="当前筛选条件下没有日志，可放宽筛选条件：把时间范围改成「近 7 天」，或清空模型 / trace_id" />
        </template>
        <a-table-column title="请求时间" :width="155" fixed="left">
          <template #default="{ record }">{{ fmtTime(record.created_at) }}</template>
        </a-table-column>
        <a-table-column title="模型" :width="165">
          <template #default="{ record }">
            <div>{{ record.model_requested }}</div>
            <div
              v-if="dense === 'full' && record.model_upstream !== record.model_requested"
              class="sub-text"
            >
              上游：{{ record.model_upstream }}
            </div>
          </template>
        </a-table-column>
        <a-table-column title="状态" :width="72">
          <template #default="{ record }">
            <a-tag :color="statusColor(record.status_code)">{{ record.status_code }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="密钥" data-index="api_key_name" :width="105" ellipsis />
        <a-table-column title="渠道" :width="140">
          <template #default="{ record }">
            <span>{{ record.channel_name || '-' }}</span>
            <a-tag v-if="record.retry_count > 0" color="orange" style="margin-left: 4px">
              重试{{ record.retry_count }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="Token（输入/输出/缓存）" :width="180">
          <template #default="{ record }">
            <span class="token-cell">
              {{ record.prompt_tokens }} / {{ record.completion_tokens }} / {{ record.cached_tokens }}
            </span>
            <div v-if="dense === 'full'" class="sub-text">
              命中率 {{ cacheRate(record) }}
              <template v-if="record.cache_creation_tokens > 0">
                · 写入 {{ record.cache_creation_tokens }}
              </template>
              <template v-if="record.reasoning_tokens > 0">· 推理 {{ record.reasoning_tokens }}</template>
            </div>
          </template>
        </a-table-column>
        <a-table-column title="响应延迟" :width="100">
          <template #default="{ record }">{{ fmtMs(record.first_byte_ms) }}</template>
        </a-table-column>
        <a-table-column title="完成时长" :width="100">
          <template #default="{ record }">{{ fmtMs(record.total_ms) }}</template>
        </a-table-column>
        <a-table-column title="费用" :width="100">
          <template #default="{ record }">{{ fmtCost(record.estimated_cost) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="72" fixed="right">
          <template #default="{ record }">
            <a @click="openDetail(record)">详情</a>
          </template>
        </a-table-column>
      </a-table>
      </DataState>
    </section>

    <a-drawer v-model:open="detailOpen" title="调用详情" width="720">
      <a-descriptions v-if="current" :column="1" bordered size="small">
        <a-descriptions-item label="Trace ID">{{ current.trace_id }}</a-descriptions-item>
        <a-descriptions-item label="请求模型">{{ current.model_requested }}</a-descriptions-item>
        <a-descriptions-item label="上游模型">{{ current.model_upstream || '-' }}</a-descriptions-item>
        <a-descriptions-item label="入站 → 出站协议">
          {{ current.inbound_protocol }} → {{ current.upstream_protocol || '-' }}
        </a-descriptions-item>
        <a-descriptions-item label="渠道">{{ current.channel_name || '-' }}</a-descriptions-item>
        <a-descriptions-item label="密钥">{{ current.api_key_name }}</a-descriptions-item>
        <a-descriptions-item label="客户端 IP">{{ current.client_ip }}</a-descriptions-item>
        <a-descriptions-item label="流式">{{ current.stream ? '是' : '否' }}</a-descriptions-item>
        <a-descriptions-item label="Token 明细">
          输入 {{ current.prompt_tokens }} · 输出 {{ current.completion_tokens }} ·
          缓存命中 {{ current.cached_tokens }} · 缓存写入 {{ current.cache_creation_tokens }} ·
          推理 {{ current.reasoning_tokens }} · 命中率 {{ cacheRate(current) }}
        </a-descriptions-item>
        <a-descriptions-item label="延迟">
          首包 {{ fmtMs(current.first_byte_ms) }} · 上游握手 {{ fmtMs(current.upstream_ms) }} ·
          完成 {{ fmtMs(current.total_ms) }}
        </a-descriptions-item>
        <a-descriptions-item label="费用">
          ${{ Number(current.estimated_cost).toFixed(8) }}
          <a-tag v-if="current.usage_estimated" color="orange" style="margin-left: 6px">用量为估算值</a-tag>
        </a-descriptions-item>
        <a-descriptions-item v-if="current.error" label="错误">
          <pre class="err-box">{{ current.error }}</pre>
        </a-descriptions-item>
      </a-descriptions>

      <template v-if="payload">
        <h4 class="section-title">请求报文</h4>
        <pre class="code-box">{{ pretty(payload.request_body) }}</pre>
        <h4 class="section-title">响应报文</h4>
        <pre class="code-box">{{ pretty(payload.response_body) }}</pre>
        <a-collapse ghost class="hdr-collapse">
          <a-collapse-panel key="req" header="请求头（凭据字段已隐藏）">
            <pre class="code-box small">{{ prettyHeaders(payload.request_headers) }}</pre>
          </a-collapse-panel>
          <a-collapse-panel key="resp" header="响应头">
            <pre class="code-box small">{{ prettyHeaders(payload.response_headers) }}</pre>
          </a-collapse-panel>
        </a-collapse>
      </template>
      <a-alert
        v-else
        type="info"
        show-icon
        class="no-payload"
        message="本次调用未留存报文"
        description="留存模式为 errors 时只保留出错的调用。想查看全部调用，把部署配置里的 RELAY_PAYLOAD_STORAGE_MODE 改为 all 后重新部署即可。"
      />
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
  flex-wrap: wrap;
}
.toolbar-left { display: flex; gap: var(--gap); }
.toolbar-spacer { flex: 1; }
.sub-text { font-size: 12px; color: var(--color-text-secondary); }
.token-cell { font-variant-numeric: tabular-nums; }
.section-title { margin: 16px 0 8px; font-size: 14px; }
.code-box, .err-box {
  font-family: var(--font-family-mono);
  max-height: 320px;
  overflow: auto;
  padding: 8px;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
}
.err-box { color: var(--color-red); max-height: 160px; }
.code-box.small { max-height: 200px; font-size: 11px; }
.hdr-collapse { margin-top: 12px; border-top: 1px solid var(--color-border); }
.hdr-collapse :deep(.ant-collapse-header) { padding-left: 0; font-size: 13px; }
.hdr-collapse :deep(.ant-collapse-content-box) { padding: 0 0 8px; }
.no-payload { margin-top: 16px; }
</style>
