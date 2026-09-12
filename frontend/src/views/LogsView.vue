<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, DownloadOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import type { Paged, RequestLog } from '@/api/types'

const loading = ref(false)
const rows = ref<RequestLog[]>([])
const total = ref(0)
const detailOpen = ref(false)
const current = ref<RequestLog | null>(null)
const payload = ref<{ request_body?: string; response_body?: string } | null>(null)
// 简略 / 详细 切换，对应参考站的 segmented 控件
const dense = ref<string>('brief')

const query = reactive({ page: 1, page_size: 50, model: '', status: '' })

async function load() {
  loading.value = true
  try {
    const params = new URLSearchParams()
    params.set('page', String(query.page))
    params.set('page_size', String(query.page_size))
    if (query.model.trim()) params.set('model', query.model.trim())
    if (query.status.trim()) params.set('status', query.status.trim())
    const res = await api.get<Paged<RequestLog>>('/logs?' + params.toString())
    rows.value = res.items || []
    total.value = res.total || 0
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function search() {
  query.page = 1
  load()
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

// 缓存命中率分母为全部输入（未命中 + 命中）
function cacheRate(row: RequestLog) {
  const denom = row.prompt_tokens + row.cached_tokens
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

function exportCsv() {
  const header = [
    '请求时间', '模型', '状态', '密钥', '渠道',
    '输入Token', '输出Token', '缓存命中', '缓存写入', '推理Token',
    '首包延迟', '完成时长', '费用USD'
  ]
  const lines = rows.value.map((r) =>
    [
      fmtTime(r.created_at), r.model_requested, r.status_code, r.api_key_name, r.channel_name,
      r.prompt_tokens, r.completion_tokens, r.cached_tokens, r.cache_creation_tokens,
      r.reasoning_tokens, r.first_byte_ms, r.total_ms, r.estimated_cost
    ].join(',')
  )
  const csv = [header.join(','), ...lines].join(String.fromCharCode(10))
  // 加 BOM 以便 Excel 正确识别 UTF-8
  const blob = new Blob([String.fromCharCode(0xfeff) + csv], { type: 'text/csv;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = 'request-logs.csv'
  a.click()
  URL.revokeObjectURL(a.href)
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
          <a-button @click="exportCsv"><DownloadOutlined /> 导出</a-button>
        </div>
        <a-input
          v-model:value="query.model"
          placeholder="按模型筛选"
          allow-clear
          style="width: 180px"
          @press-enter="search"
        >
          <template #prefix><SearchOutlined /></template>
        </a-input>
        <a-input
          v-model:value="query.status"
          placeholder="状态码"
          allow-clear
          style="width: 110px"
          @press-enter="search"
        />
        <a-button type="primary" @click="search">查询</a-button>
        <div class="toolbar-spacer" />
        <a-segmented
          v-model:value="dense"
          :options="[{ label: '简略', value: 'brief' }, { label: '详细', value: 'full' }]"
        />
      </div>

      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="pagination"
        row-key="id"
        size="small"
        :scroll="{ x: 1140 }"
      >
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

      <template v-if="payload && payload.request_body">
        <h4 class="section-title">请求报文</h4>
        <pre class="code-box">{{ payload.request_body }}</pre>
      </template>
      <template v-if="payload && payload.response_body">
        <h4 class="section-title">响应报文</h4>
        <pre class="code-box">{{ payload.response_body }}</pre>
      </template>
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
</style>
