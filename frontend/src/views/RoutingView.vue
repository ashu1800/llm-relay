<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import EChart from '@/components/EChart.vue'

interface ChannelRow {
  channel_id: number
  channel_name: string
  weight: number
  // 占比一律按原始数值返回，格式化统一放在前端
  expected_share: number
  actual_share: number
  deviation: number
  requests: number
  errors: number
  retries: number
  success_rate: number
  avg_ms: number
  avg_first_byte_ms: number
  cache_hit_rate: number
  tokens: number
  cost: string
  idle: boolean
}

interface Incident {
  trace_id: string
  model: string
  channel_name: string
  status_code: number
  retry_count: number
  error: string
  total_ms: number
  created_at: string
}

const loading = ref(false)
const range = ref('7d')
const summary = ref<Record<string, any>>({})
const channels = ref<ChannelRow[]>([])
const models = ref<any[]>([])
const incidents = ref<Incident[]>([])
const shareOption = ref<Record<string, any>>({})

const ranges = [
  { value: 'today', label: '今天' },
  { value: '3d', label: '近 3 天' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' }
]

function pct(v: number | string | undefined) {
  const n = typeof v === 'string' ? parseFloat(v) : v
  if (n === undefined || !isFinite(n)) return '0%'
  return (n * 100).toFixed(1) + '%'
}

// 偏差为正说明实际分流多于权重预期，为负说明少于预期。
// 阈值取 2 个百分点，权重本来就只是概率期望，小幅波动属正常。
function deviationColor(v: number) {
  if (!isFinite(v) || Math.abs(v) < 0.02) return 'default'
  return v > 0 ? 'blue' : 'orange'
}

function deviationText(v: number) {
  if (!isFinite(v)) return '-'
  return (v >= 0 ? '+' : '') + (v * 100).toFixed(1) + '%'
}

async function load() {
  loading.value = true
  try {
    const res = await api.get<any>('/routing/analysis?range=' + range.value)
    summary.value = res.summary || {}
    channels.value = res.channels || []
    models.value = res.models || []
    incidents.value = res.incidents || []
    renderShare()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function renderShare() {
  const names = channels.value.map((c) => c.channel_name)
  shareOption.value = {
    grid: { left: 8, right: 16, top: 34, bottom: 4, containLabel: true },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    legend: { data: ['期望占比', '实际占比'], right: 0, top: 0, itemWidth: 12, itemHeight: 8 },
    xAxis: { type: 'value', axisLabel: { formatter: (v: number) => (v * 100).toFixed(0) + '%' } },
    yAxis: { type: 'category', data: names, axisLabel: { width: 110, overflow: 'truncate' } },
    series: [
      {
        name: '期望占比',
        type: 'bar',
        data: channels.value.map((c) => c.expected_share || 0),
        itemStyle: { color: '#d9c9b6' },
        barMaxWidth: 12
      },
      {
        name: '实际占比',
        type: 'bar',
        data: channels.value.map((c) => c.actual_share || 0),
        itemStyle: { color: '#c87864' },
        barMaxWidth: 12
      }
    ]
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <section class="panel toolbar-panel">
      <div class="toolbar">
        <a-radio-group v-model:value="range" button-style="solid" @change="load">
          <a-radio-button v-for="r in ranges" :key="r.value" :value="r.value">{{ r.label }}</a-radio-button>
        </a-radio-group>
        <div class="spacer" />
        <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
      </div>
    </section>

    <div class="stat-grid">
      <div class="panel stat-card">
        <div class="stat-value">{{ summary.requests ?? 0 }}</div>
        <div class="stat-label">请求总数</div>
        <div class="stat-sub">失败 {{ summary.errors ?? 0 }} 次</div>
      </div>
      <div class="panel stat-card">
        <div class="stat-value">{{ pct(summary.retry_rate) }}</div>
        <div class="stat-label">重试率</div>
        <div class="stat-sub">重试 {{ summary.retries ?? 0 }} 次</div>
      </div>
      <div class="panel stat-card">
        <div class="stat-value">{{ summary.avg_ms ?? 0 }} ms</div>
        <div class="stat-label">平均耗时</div>
        <div class="stat-sub">首字节 {{ summary.avg_first_byte_ms ?? 0 }} ms</div>
      </div>
      <div class="panel stat-card">
        <div class="stat-value">{{ pct(summary.cache_hit_rate) }}</div>
        <div class="stat-label">缓存命中率</div>
        <div class="stat-sub">预估 &#36;{{ summary.cost ?? '0' }}</div>
      </div>
    </div>

    <section class="panel chart-panel">
      <div class="panel-title">权重与实际分流对比</div>
      <EChart :option="shareOption" height="240px" />
      <div class="hint">
        期望占比按已启用渠道的权重计算，实际占比取区间内真实流量。两者偏差大说明有渠道在失败重试，或被策略改写了排序。
      </div>
    </section>

    <section class="panel table-panel">
      <div class="panel-title">渠道明细</div>
      <a-table :data-source="channels" :loading="loading" :pagination="false" row-key="channel_id" size="small">
        <a-table-column title="渠道" data-index="channel_name" :width="180" />
        <a-table-column title="权重" data-index="weight" :width="70" />
        <a-table-column title="期望占比" :width="95">
          <template #default="{ record }">{{ pct(record.expected_share) }}</template>
        </a-table-column>
        <a-table-column title="实际占比" :width="95">
          <template #default="{ record }">{{ pct(record.actual_share) }}</template>
        </a-table-column>
        <a-table-column title="偏差" :width="95">
          <template #default="{ record }">
            <a-tag :color="deviationColor(record.deviation)">{{ deviationText(record.deviation) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="请求" data-index="requests" :width="80" />
        <a-table-column title="成功率" :width="90">
          <template #default="{ record }">{{ pct(record.success_rate) }}</template>
        </a-table-column>
        <a-table-column title="重试" data-index="retries" :width="70" />
        <a-table-column title="平均耗时" :width="100">
          <template #default="{ record }">{{ record.avg_ms }} ms</template>
        </a-table-column>
        <a-table-column title="首字节" :width="95">
          <template #default="{ record }">{{ record.avg_first_byte_ms }} ms</template>
        </a-table-column>
        <a-table-column title="缓存命中" :width="95">
          <template #default="{ record }">{{ pct(record.cache_hit_rate) }}</template>
        </a-table-column>
        <a-table-column title="预估费用" :width="110">
          <template #default="{ record }">&#36;{{ record.cost }}</template>
        </a-table-column>
        <a-table-column title="状态" :width="80">
          <template #default="{ record }">
            <a-tag v-if="record.idle" color="default">无流量</a-tag>
            <a-tag v-else color="green">在用</a-tag>
          </template>
        </a-table-column>
      </a-table>
    </section>

    <section class="panel table-panel">
      <div class="panel-title">模型 → 渠道分布</div>
      <a-table :data-source="models" :pagination="false" row-key="model" size="small">
        <a-table-column title="模型" data-index="model" :width="200" />
        <a-table-column title="请求" data-index="requests" :width="90" />
        <a-table-column title="走过的渠道">
          <template #default="{ record }">
            <a-space wrap>
              <a-tag v-for="c in record.channels" :key="c.channel_name">
                {{ c.channel_name }} · {{ c.requests }}
              </a-tag>
            </a-space>
          </template>
        </a-table-column>
      </a-table>
    </section>

    <section class="panel table-panel">
      <div class="panel-title">重试与失败记录（近 30 条）</div>
      <a-table :data-source="incidents" :pagination="false" row-key="trace_id" size="small" :scroll="{ x: 900 }">
        <a-table-column title="时间" data-index="created_at" :width="160" />
        <a-table-column title="模型" data-index="model" :width="160" ellipsis />
        <a-table-column title="渠道" data-index="channel_name" :width="140" ellipsis />
        <a-table-column title="状态" :width="80">
          <template #default="{ record }">
            <a-tag :color="record.status_code >= 400 ? 'red' : 'green'">{{ record.status_code }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="重试" data-index="retry_count" :width="70" />
        <a-table-column title="耗时" :width="90">
          <template #default="{ record }">{{ record.total_ms }} ms</template>
        </a-table-column>
        <a-table-column title="错误" data-index="error" ellipsis />
      </a-table>
      <a-empty v-if="!incidents.length" description="区间内没有重试或失败记录" />
    </section>
  </div>
</template>

<style scoped>
.page { padding: var(--gap); display: flex; flex-direction: column; gap: var(--gap); }
.toolbar-panel { padding: 0; }
.toolbar { display: flex; align-items: center; padding: var(--gap); min-height: 64px; }
.spacer { flex: 1; }
.stat-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: var(--gap); }
.stat-card { padding: 16px 18px; }
.stat-value { font-size: 24px; font-weight: 600; color: var(--color-primary); }
.stat-label { margin-top: 4px; font-size: 13px; color: var(--color-text-secondary); }
.stat-sub { margin-top: 2px; font-size: 12px; color: var(--color-text-secondary); }
.chart-panel, .table-panel { padding: 16px 18px; }
.panel-title { font-weight: 600; margin-bottom: 8px; }
.hint { margin-top: 8px; font-size: 12px; color: var(--color-text-secondary); line-height: 1.7; }
</style>
