<script setup lang="ts">
import { onMounted, ref } from 'vue'
import {
  ApiOutlined,
  DollarOutlined,
  ThunderboltOutlined,
  CheckCircleOutlined,
  ReloadOutlined
} from '@ant-design/icons-vue'
import PageToolbar from '@/components/PageToolbar.vue'
import PanelCard from '@/components/PanelCard.vue'
import StatCard from '@/components/StatCard.vue'

type Health = { status: string; uptime: string }
type SystemInfo = { version: string; port: number; pricing_sync: number; payload_store: string }

const health = ref<Health | null>(null)
const info = ref<SystemInfo | null>(null)
const error = ref('')
const loading = ref(false)

const ranges = [
  { key: 'today', label: '今天' },
  { key: '3d', label: '近3天' },
  { key: '7d', label: '近7天' },
  { key: '30d', label: '近30天' }
]
const range = ref('today')

async function load() {
  loading.value = true
  try {
    const [h, i] = await Promise.all([
      fetch('/healthz').then((r) => r.json()),
      fetch('/api/admin/system/info').then((r) => r.json())
    ])
    health.value = h
    info.value = i
    error.value = ''
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="dashboard">
    <!-- 工具栏：时间范围 + 刷新（对齐参考站 dashboard-toolbar） -->
    <PageToolbar label="时间范围">
      <button
        v-for="r in ranges"
        :key="r.key"
        class="pill-btn"
        :class="{ active: range === r.key }"
        @click="range = r.key"
      >
        {{ r.label }}
      </button>
      <template #right>
        <button class="pill-btn" :disabled="loading" @click="load">
          <ReloadOutlined /> 刷新
        </button>
      </template>
    </PageToolbar>

    <a-alert v-if="error" type="error" :message="error" show-icon style="margin-bottom: 8px" />

    <!-- 概览四卡 -->
    <section class="overview-row">
      <div class="summary-grid">
        <StatCard label="请求数量" :value="0" tone="purple">
          <template #icon><ApiOutlined /></template>
        </StatCard>
        <StatCard label="预估金额" value="0.0000" tone="orange" hint="按官方单价折算">
          <template #icon><DollarOutlined /></template>
        </StatCard>
        <StatCard label="Token 量" :value="0" tone="blue">
          <template #icon><ThunderboltOutlined /></template>
        </StatCard>
        <StatCard label="成功率" value="--" tone="green">
          <template #icon><CheckCircleOutlined /></template>
        </StatCard>
      </div>

      <PanelCard title="请求热力图">
        <template #extra>
          <span class="panel-note">0 次请求</span>
        </template>
        <div class="placeholder">等待数据接入</div>
      </PanelCard>
    </section>

    <!-- 图表区 -->
    <section class="chart-grid">
      <PanelCard title="消耗分布">
        <div class="placeholder tall">等待数据接入</div>
      </PanelCard>
      <PanelCard title="消耗趋势">
        <div class="placeholder tall">等待数据接入</div>
      </PanelCard>
      <PanelCard title="模型调用分析">
        <div class="placeholder tall">等待数据接入</div>
      </PanelCard>
      <PanelCard title="模型消耗占比">
        <div class="placeholder tall">等待数据接入</div>
      </PanelCard>
    </section>

    <!-- 运行状态（本站自检，参考站无此项） -->
    <PanelCard title="服务状态">
      <a-descriptions :column="2" size="small">
        <a-descriptions-item label="服务">
          <a-tag :color="health ? 'green' : 'red'">{{ health ? '运行中' : '不可用' }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="运行时长">{{ health?.uptime ?? '-' }}</a-descriptions-item>
        <a-descriptions-item label="监听端口">{{ info?.port ?? '-' }}</a-descriptions-item>
        <a-descriptions-item label="版本">{{ info?.version ?? '-' }}</a-descriptions-item>
        <a-descriptions-item label="定价同步间隔">{{ info?.pricing_sync ?? '-' }} 小时</a-descriptions-item>
        <a-descriptions-item label="报文留存">{{ info?.payload_store ?? '-' }}</a-descriptions-item>
      </a-descriptions>
    </PanelCard>
  </div>
</template>

<style scoped>
/* 概览区与图表区均使用 grid，gap 恒为 8px（实测） */
.overview-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--gap);
  margin-bottom: var(--gap);
}

.summary-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--gap);
}

.chart-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--gap);
  margin-bottom: var(--gap);
}

.pill-btn {
  height: var(--size-menu-item-height);
  min-width: 32px;
  padding: 0 12px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid transparent;
  border-radius: var(--radius-pill);
  background: transparent;
  color: var(--color-text);
  font-family: inherit;
  font-size: var(--font-size-menu);
  cursor: pointer;
  transition: background 0.2s var(--ease-expo), color 0.2s var(--ease-expo);
}

.pill-btn:hover:not(:disabled) {
  color: var(--color-primary);
  background: var(--color-primary-a20);
}

.pill-btn.active {
  color: var(--color-primary);
  background: var(--color-primary-a20);
}

.pill-btn:disabled { opacity: 0.6; cursor: not-allowed; }

.panel-note {
  font-size: 12px;
  color: var(--color-text-secondary);
}

.placeholder {
  height: 99px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--color-text-secondary);
  font-size: 13px;
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-control);
}

.placeholder.tall { height: 288px; }
</style>
