<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined, DownloadOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import GroupTag from '@/components/GroupTag.vue'
import { liveConnected, onLive } from '@/composables/useLive'
import type { ChannelGroup, Paged, RequestLog } from '@/api/types'

// 分组表：日志里的模型、密钥、分组三处标签共用该请求所属分组的颜色。
//
// 为什么日志要按分组着色而不是按模型名（原来是后者）：
// 分组是用户自己配的边界（哪个密钥能走哪批渠道），日志里要一眼看出
// 「这条请求走的是哪个分组」；模型名着色对排障没有帮助，反而多一套颜色规则。
const groups = ref<ChannelGroup[]>([])

function groupOf(id: number) {
  return groups.value.find((g) => g.id === id)
}

/**
 * 胶囊的颜色参数：同一个分组下的模型 / 密钥 / 分组三个标签共用它。
 *
 * colorFrom 必须传分组名 —— 三个标签的展示名各不相同，
 * 若让它们各自按自己的名字派色，同一行会出现三种颜色（实测踩过）。
 */
function tagColorOf(id: number) {
  const g = groupOf(id)
  // 找不到分组时（分组已删、或历史日志里 group_id=0）统一用一个固定的来源名：
  // 否则同一行的三个胶囊会各按自己的名字派色，看起来像三个不同分组
  return { color: g?.color, colorFrom: g ? g.name : '未知分组' }
}

function groupName(id: number) {
  return groupOf(id)?.name ?? (id ? String(id) : '—')
}

const loading = ref(false)
const rows = ref<RequestLog[]>([])
const total = ref(0)
const detailOpen = ref(false)
const current = ref<RequestLog | null>(null)

const query = reactive({
  page: 1,
  page_size: 50,
  model: '',
  trace_id: '',
  // '' 表示不限；'success' / 'error' 走状态码区间，其余按精确状态码
  status: '',
  // '' 表示不限时间范围
  range: 'today'
})

// 「今天」按本地零点算，而不是「最近 24 小时」：
// 后者在早上看会把昨天的调用也算进来，与看板上的今日口径对不上 ——
// 两个页面显示同一个上午的请求数却不一样，是最容易被当成 bug 的那种不一致。
const rangeOptions = [
  { value: '1h', label: '近 1 小时' },
  { value: 'today', label: '今天' },
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
  if (range === 'today') {
    // 本地零点，再转成绝对时刻 —— 与看板的「今天」用同一套边界
    const d = new Date()
    d.setHours(0, 0, 0, 0)
    return d.toISOString()
  }
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

// loadGroups 只在首次加载时取一次：分组是低频变更的配置，
// 跟着每次翻页/刷新去拉一份纯属浪费（日志页刷新很频繁）
async function loadGroups() {
  try {
    const res = await api.get<{ items: ChannelGroup[] }>('/groups')
    groups.value = res.items || []
  } catch {
    // 拿不到分组不影响看日志：标签会退回按名字派生的颜色
  }
}

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

// 详情直接用列表行数据：列表接口返回的字段已经完整，
// 报文相关展示移除后，也不必再为它请求 /logs/:id。
function openDetail(row: RequestLog) {
  current.value = row
  detailOpen.value = true
}

// 耗时分级：5 秒内绿色、6-15 秒橙黄、15 秒以上红色。
//
// 分档按「用户体感」定：5 秒内是正常对话该有的速度，
// 5-15 秒已经明显在等，超过 15 秒基本可以判定这次调用有问题（上游慢或重试）。
// 边界取 <=5000 / <=15000 而不是 5000~6000 之间留缝隙：
// 中间的毫秒必须落进某一档，否则会出现「不着色」的空档。
//
// fmtMs 对 0 与空值都返回 '-'，那种情况不着色，避免把「没有数据」显示成「很快」。
function latencyClass(ms: number | null | undefined) {
  if (!ms) return 'lat-none'
  if (ms <= 5000) return 'lat-fast'
  if (ms <= 15000) return 'lat-mid'
  return 'lat-slow'
}

/** 悬停说明这一档的判据：颜色本身不该是唯一的信息来源 */
function latencyTitle(ms: number | null | undefined) {
  if (!ms) return '没有记录到耗时'
  if (ms <= 5000) return '5 秒内'
  if (ms <= 15000) return '5-15 秒'
  return '超过 15 秒'
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
  return ((row.cached_tokens / denom) * 100).toFixed(2) + '%'
}

// 固定成 YYYY-MM-DD HH:mm:ss —— 与参考站日志列表一致。
// 原来用 toLocaleString('zh-CN')，出来的是 2026/9/12 13:06:02：
// 斜杠分隔、月日不补零，同一列里宽度还会随月份变化而抖动。
// 手工补零而不是再用一次 toLocale*，是为了不受运行环境区域设置影响。
function pad2(n: number) {
  return n < 10 ? '0' + n : String(n)
}
function fmtTime(t: string) {
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d.getTime())) return t
  return (
    d.getFullYear() +
    '-' + pad2(d.getMonth() + 1) +
    '-' + pad2(d.getDate()) +
    ' ' + pad2(d.getHours()) +
    ':' + pad2(d.getMinutes()) +
    ':' + pad2(d.getSeconds())
  )
}

function fmtMs(v: number) {
  if (!v) return '-'
  return v >= 1000 ? (v / 1000).toFixed(2) + 's' : v + 'ms'
}

function fmtCost(v: string) {
  const n = Number(v)
  return n > 0 ? '$' + n.toFixed(6) : '-'
}

// 费用为什么是这么多：把当时生效的倍率摊在金额旁边。
// 时段倍率按服务器本地时间命中，用户核对账单时最常问的就是「这笔怎么贵了」。
function costMultiplierTag(row: RequestLog) {
  const s = row.pricing_snapshot
  if (!s || s.multiplier == null) return { peak: '', fixed: '' }
  const m = Number(s.multiplier)
  if (!m || m === 1) return { peak: '', fixed: '' }
  if (s.peak_applied) {
    return { peak: '×' + m + ' ' + (s.peak_label || '时段倍率'), fixed: '' }
  }
  if (s.multiplier_source === 'fixed') {
    return { peak: '', fixed: '×' + m + ' 固定倍率' }
  }
  return { peak: '', fixed: '' }
}

// 计价一行：把快照里的单价与来源写清楚，便于与「价格试算」对照
function multiplierSourceText(row: RequestLog) {
  const s = row.pricing_snapshot
  if (!s) return ''
  // 库里可能是 {}（早期版本对这个字段写过空对象）：没有单价就没有「计价」可讲，
  // 不判断的话界面上会出现「输入 $undefined」
  if (s.input_per_1m == null && s.input == null) return ''
  const parts: string[] = []
  parts.push('输入 $' + (s.input_per_1m ?? s.input) + ' / 输出 $' + (s.output_per_1m ?? s.output) + ' 每 1M')
  if (Number(s.fixed_multiplier) && Number(s.fixed_multiplier) !== 1) {
    parts.push('固定倍率 ×' + s.fixed_multiplier)
  }
  if (s.peak_applied) {
    parts.push('命中时段：' + (s.peak_label || '未命名') + ' ×' + s.multiplier)
  } else if (s.multiplier_source === 'fixed') {
    parts.push('按固定倍率 ×' + s.multiplier)
  } else {
    parts.push('原价')
  }
  if (s.resolved_at) parts.push('计价时刻 ' + s.resolved_at)
  return parts.join('；')
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

// 实时插入：服务端每秒查一次新日志（id 增量），有就推过来。
// 只在「看的是第一页且没有筛选」时插进去 —— 翻了页或筛了模型时，
// 新来的日志不一定属于当前视图，硬插会让列表与筛选条件对不上。
const liveTail = ref(true)

onLive('logs', (items: RequestLog[]) => {
  if (!liveTail.value || !Array.isArray(items) || !items.length) return
  if (query.page !== 1) return
  if (query.model.trim() || query.trace_id.trim() || query.status) return
  // 新日志的时间一定落在当前时间范围里（今天/近 1 小时……），
  // 只有「不限时间」之外的范围需要担心，而边界只差几毫秒，不值得再过滤一次
  const fresh = items.filter((it) => !rows.value.some((r) => r.id === it.id))
  if (!fresh.length) return
  rows.value = [...fresh.reverse(), ...rows.value].slice(0, query.page_size)
  total.value += fresh.length
})

onMounted(() => {
  // 分组必须先加载：模型/密钥/分组三列的颜色都取自它，
  // 拿不到就会退回「按名字派生」，三列出现三种颜色（实测踩过）
  loadGroups()
  load()
})
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
          <a-button :loading="exporting" @click="exportCsv"><DownloadOutlined /> 导出</a-button>
          <!-- 实时插入可以关掉：正在盯着某一行排障时，
               不断有新行从上面顶进来会看串行 -->
          <a-button :type="liveTail ? 'primary' : 'default'" ghost @click="liveTail = !liveTail">
            <span class="live-dot" :class="{ on: liveConnected && liveTail }" />
            {{ liveTail ? '实时插入中' : '实时已暂停' }}
          </a-button>
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
        :scroll="{ x: 1274 }"
      >
        <template #emptyText>
          <a-empty description="当前筛选条件下没有日志，可放宽筛选条件：把时间范围改成「近 7 天」，或清空模型 / trace_id" />
        </template>
        <a-table-column title="请求时间" :width="155" fixed="left">
          <template #default="{ record }">{{ fmtTime(record.created_at) }}</template>
        </a-table-column>
        <a-table-column title="模型" :width="155">
          <template #default="{ record }">
            <!-- 模型、密钥、分组三处用的是同一个组件与同一个颜色：
                 它们描述的是「这次请求属于哪个分组」，颜色因此必须一致 -->
            <GroupTag :name="record.model_requested" v-bind="tagColorOf(record.group_id)" />
            <div
              v-if="record.model_upstream && record.model_upstream !== record.model_requested"
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
        <a-table-column title="密钥" :width="120" ellipsis>
          <template #default="{ record }">
            <GroupTag v-if="record.api_key_name" :name="record.api_key_name" v-bind="tagColorOf(record.group_id)" />
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <a-table-column title="分组" :width="110" ellipsis>
          <template #default="{ record }">
            <GroupTag :name="groupName(record.group_id)" v-bind="tagColorOf(record.group_id)" />
          </template>
        </a-table-column>
        <a-table-column title="词元（输入/输出/缓存）" :width="180">
          <template #default="{ record }">
            <span class="token-cell">
              <span class="tk tk-in">{{ record.prompt_tokens }}</span>
              <span class="tk-sep">/</span>
              <span class="tk tk-out">{{ record.completion_tokens }}</span>
              <span class="tk-sep">/</span>
              <span class="tk tk-cache">{{ record.cached_tokens }}</span>
            </span>
          </template>
        </a-table-column>
        <a-table-column title="缓存命中" :width="110">
          <template #default="{ record }">
            <span class="cache-hit">{{ cacheRate(record) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="首字耗时" :width="100">
          <template #default="{ record }">
            <span :class="latencyClass(record.first_byte_ms)" :title="latencyTitle(record.first_byte_ms)">
              {{ fmtMs(record.first_byte_ms) }}
            </span>
          </template>
        </a-table-column>
        <a-table-column title="总共耗时" :width="100">
          <template #default="{ record }">
            <span :class="latencyClass(record.total_ms)" :title="latencyTitle(record.total_ms)">
              {{ fmtMs(record.total_ms) }}
            </span>
          </template>
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
        <a-descriptions-item label="请求模型">
          <GroupTag :name="current.model_requested" v-bind="tagColorOf(current.group_id)" />
        </a-descriptions-item>
        <a-descriptions-item label="上游模型">
          <GroupTag v-if="current.model_upstream" :name="current.model_upstream" v-bind="tagColorOf(current.group_id)" />
          <template v-else>-</template>
        </a-descriptions-item>
        <a-descriptions-item label="分组">
          <GroupTag :name="groupName(current.group_id)" v-bind="tagColorOf(current.group_id)" />
        </a-descriptions-item>
        <a-descriptions-item label="入站 → 出站协议">
          {{ current.inbound_protocol }} → {{ current.upstream_protocol || '-' }}
        </a-descriptions-item>
        <a-descriptions-item label="渠道">{{ current.channel_name || '-' }}</a-descriptions-item>
        <a-descriptions-item label="密钥">{{ current.api_key_name }}</a-descriptions-item>
        <a-descriptions-item label="客户端 IP">{{ current.client_ip }}</a-descriptions-item>
        <a-descriptions-item label="流式">{{ current.stream ? '是' : '否' }}</a-descriptions-item>
        <a-descriptions-item label="词元明细">
          输入 {{ current.prompt_tokens }} · 输出 {{ current.completion_tokens }} ·
          缓存命中 {{ current.cached_tokens }} · 缓存写入 {{ current.cache_creation_tokens }} ·
          推理 {{ current.reasoning_tokens }} · 命中率 {{ cacheRate(current) }}
        </a-descriptions-item>
        <a-descriptions-item label="重试">
          {{ current.retry_count > 0 ? '重试 ' + current.retry_count + ' 次' : '无' }}
        </a-descriptions-item>
        <a-descriptions-item label="耗时">
          首字 {{ fmtMs(current.first_byte_ms) }} · 上游握手 {{ fmtMs(current.upstream_ms) }} ·
          总共 {{ fmtMs(current.total_ms) }}
        </a-descriptions-item>
        <a-descriptions-item label="费用">
          ${{ Number(current.estimated_cost).toFixed(8) }}
          <a-tag v-if="current.usage_estimated" color="orange" style="margin-left: 6px">用量为估算值</a-tag>
          <!-- 金额为什么是这个数：把当时生效的倍率与来源摊开，省得去猜 -->
          <a-tag v-if="costMultiplierTag(current).peak" color="orange" style="margin-left: 6px">
            {{ costMultiplierTag(current).peak }}
          </a-tag>
          <a-tag v-else-if="costMultiplierTag(current).fixed" style="margin-left: 6px">
            {{ costMultiplierTag(current).fixed }}
          </a-tag>
        </a-descriptions-item>
        <a-descriptions-item v-if="multiplierSourceText(current)" label="计价">
          {{ multiplierSourceText(current) }}
        </a-descriptions-item>
        <a-descriptions-item v-if="current.error" label="错误">
          <pre class="err-box">{{ current.error }}</pre>
        </a-descriptions-item>
      </a-descriptions>

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
.live-dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  margin-right: 6px;
  background: currentColor;
  opacity: 0.35;
}
.live-dot.on {
  background: var(--color-green);
  opacity: 1;
  animation: live-pulse 2s ease-in-out infinite;
}
@keyframes live-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.3; }
}
.sub-text { font-size: 12px; color: var(--color-text-secondary); }
.token-cell { font-variant-numeric: tabular-nums; }

/* 模型、密钥、分组三列各是一个胶囊，用的是同一个组件（components/GroupTag.vue）
   与同一个颜色 —— 该请求所属分组的颜色。

   原来的分工是「模型按模型名着色、密钥用中性色」：那套规则在排障时没用，
   日志里真正要回答的是「这条请求走的是哪个分组」。三者同色之后，
   扫一眼就能按颜色把同一分组的请求归到一起，也不必再记住两套配色规则。
   颜色不是唯一线索：三列里都写着名字。 */
/* 词元三段各自的颜色（变量定义见 theme.css，深色主题自动换档） */
.tk-in { color: var(--token-input); }
.tk-out { color: var(--token-output); }
.tk-cache { color: var(--token-cache); }
.tk-sep { color: var(--color-border); margin: 0 3px; }

/* 耗时分级 */
.lat-fast { color: var(--latency-fast); }
.lat-mid { color: var(--latency-mid); }
.lat-slow { color: var(--latency-slow); font-weight: 500; }
.lat-none { color: var(--color-text-secondary); }

/* 缓存命中率用缓存那段的颜色，与词元列第三个数是同一个语义 */
.cache-hit { color: var(--token-cache); font-variant-numeric: tabular-nums; }
/* 错误原文：详情里唯一保留的代码块（排障时最常看的就是上游报错） */
.err-box {
  font-family: var(--font-family-mono);
  max-height: 160px;
  overflow: auto;
  padding: 8px;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  color: var(--color-red);
}
</style>
