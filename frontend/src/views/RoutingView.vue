<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import EChart from '@/components/EChart.vue'
import { useChartTheme } from '@/utils/chartTheme'
import DataState from '@/components/DataState.vue'

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
// 图表配色跟着主题走（详见 utils/chartTheme.ts）
const ct = useChartTheme()

// 加载失败必须留下痕迹：这一页所有数字都来自同一个接口，
// 失败后如果只是弹个 message，统计卡会显示成「0 次请求」，被读成「这段时间没有流量」
const loadError = ref('')

// 只要有任意一块统计拿到了数据，就说明「已经有内容可看」：
// 此时刷新失败只在顶部提示，不把用户正在看的图表整块换成错误面板
const hasStats = computed(
  () =>
    (summary.value?.requests ?? 0) > 0 ||
    channels.value.length > 0 ||
    models.value.length > 0 ||
    incidents.value.length > 0
)

const ranges = [
  { value: 'today', label: '今天' },
  { value: '3d', label: '近 3 天' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' }
]

// 与日志页、密钥页保持同一套渲染方式：后端给的是带偏移的 RFC3339，
// new Date 能正确解析，toLocaleString 再按浏览器本地时区显示。
// 原来这一列直接把原始字符串打出来，而后端那串被 to_char 抹掉了时区
// （且按 UTC 渲染），于是同一时刻在这一页比日志页早 8 小时，还看不出原因。
function fmtTime(t: string) {
  if (!t) return '—'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

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
  loadError.value = ''
  try {
    const res = await api.get<any>('/routing/analysis?range=' + range.value)
    summary.value = res.summary || {}
    channels.value = res.channels || []
    models.value = res.models || []
    incidents.value = res.incidents || []
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

// 图表配置改为 computed。
// 原来是 renderShare() 命令式赋值、只在数据加载完调用一次 ——
// 切换主题时不会重算，图例与坐标轴标签会停在旧主题的颜色上
// （暗色下就是深灰字压深色底，整条图例只剩图标）。
const shareOption = computed(() => {
  const names = channels.value.map((c) => c.channel_name)
  // 条形末端标数值：多数字道的占比只有百分之几，柱子短到看不出量级，
  // 光靠柱长读不出信息（与「模型调用分析」同一个处理）。
  // 零值不标 —— 十几行「0.0%」纯属噪音。
  const barLabel = {
    show: true,
    position: 'right',
    fontSize: 11,
    color: ct.value.secondary,
    // 加一圈与面板底色同色的描边：实际占比小于期望占比时，
    // 数值标签会落在期望那根柱子上，不描边就糊在一起看不清
    textBorderColor: ct.value.dark ? '#303030' : '#ffffff',
    textBorderWidth: 2,
    formatter: (p: any) => (p.value > 0 ? (p.value * 100).toFixed(1) + '%' : '')
  }
  return {
    // right 留出条形末端数值标签的位置
    grid: { left: 8, right: 56, top: 34, bottom: 4, containLabel: true },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    legend: {
      data: ['期望占比', '实际占比'],
      right: 0,
      top: 0,
      itemWidth: 12,
      itemHeight: 8,
      textStyle: { color: ct.value.text }
    },
    xAxis: {
      type: 'value',
      axisLabel: {
        color: ct.value.secondary,
        formatter: (v: number) => (v * 100).toFixed(0) + '%'
      }
    },
    yAxis: {
      type: 'category',
      data: names,
      // 130 是按实际渠道名量出来的：最长 truncate-upstream-test 在 11px 下 119px，
      // 原来写死 110 会把它截成 truncate-upstream-...
      //
      // interval: 0 是必需的：类目轴的 interval 默认 'auto'，echarts 觉得排不下
      // 就会**隔一个藏一个**，15 个渠道只剩 8 个名字，剩下的柱子没有标签，
      // 根本认不出是哪条渠道。行高够（202px / 15 ≈ 13.5px，字高 11px）。
      axisLabel: { color: ct.value.secondary, width: 170, overflow: 'truncate', interval: 0 }
    },
    series: [
      {
        name: '期望占比',
        type: 'bar',
        data: channels.value.map((c) => c.expected_share || 0),
        itemStyle: { color: '#d9c9b6' },
        barMaxWidth: 12,
        label: barLabel
      },
      {
        name: '实际占比',
        type: 'bar',
        data: channels.value.map((c) => c.actual_share || 0),
        itemStyle: { color: '#c87864' },
        barMaxWidth: 12,
        label: barLabel
      }
    ]
  }
})

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

    <DataState
      :error="loadError"
      :has-data="hasStats"
      :loading="loading"
      title="路由分析加载失败"
      hint="这一页的统计来自后端 /routing/analysis 接口，请确认后端服务是否正常，然后重试。"
      @retry="load"
    >
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
        <template #emptyText>
          <a-empty description="区间内没有渠道流量，确认渠道已启用，并在这个时间范围内发起过请求" />
        </template>
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
        <template #emptyText>
          <a-empty description="区间内没有模型调用记录，发起一次请求后这里会显示模型走过了哪些渠道" />
        </template>
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
        <template #emptyText>
          <a-empty description="区间内没有重试或失败记录，说明这段时间的调用都成功了" />
        </template>
        <a-table-column title="时间" :width="170">
          <template #default="{ record }">{{ fmtTime(record.created_at) }}</template>
        </a-table-column>
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
    </section>
    </DataState>
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
/* DataState 的错误提示自带左右外边距（为列表页的面板布局设计），
   这里外层 .page 已经有内边距，去掉以免出现双重缩进 */
.page :deep(.ds-alert) { margin: 0 0 var(--gap); }
</style>
