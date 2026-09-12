<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { ReloadOutlined, DeleteOutlined, SyncOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import type { PricingSyncResult } from '@/api/types'

const loading = ref(false)
const runtime = ref<Record<string, any>>({})
const counts = ref<Record<string, number>>({})
const span = ref<Record<string, string | null>>({})
const cleaning = ref(false)
const syncing = ref(false)

// 运行参数只读：它们来自环境变量与 yaml，进程启动后不可变。
// 与其做出改了不生效的假开关，不如直接告诉用户改哪里。
const runtimeRows = [
  { key: 'listen', label: '监听地址', hint: 'deploy/.env 的 BIND_ADDR 与 APP_PORT' },
  { key: 'mode', label: '运行模式', hint: 'GIN_MODE' },
  { key: 'log_level', label: '日志级别', hint: 'RELAY_LOG_LEVEL' },
  { key: 'log_format', label: '日志格式', hint: 'RELAY_LOG_FORMAT' },
  { key: 'upstream_timeout_sec', label: '上游总超时（秒）', hint: 'RELAY_UPSTREAM_TIMEOUT' },
  { key: 'first_byte_timeout_sec', label: '首字节超时（秒）', hint: 'RELAY_FIRST_BYTE_TIMEOUT' },
  { key: 'max_retries', label: '最大重试次数', hint: 'RELAY_MAX_RETRIES' },
  { key: 'max_request_body_mb', label: '请求体上限（MB）', hint: 'RELAY_MAX_REQUEST_BODY_MB' },
  { key: 'log_retention_days', label: '日志保留天数', hint: 'RELAY_LOG_RETENTION_DAYS' },
  { key: 'payload_storage_mode', label: '报文留存模式', hint: 'RELAY_PAYLOAD_STORAGE_MODE' },
  { key: 'payload_max_kb', label: '单条报文上限（KB）', hint: 'RELAY_PAYLOAD_MAX_KB' },
  { key: 'max_concurrency', label: '上游并发上限', hint: 'RELAY_MAX_CONCURRENCY' },
  { key: 'default_rpm', label: '默认每分钟请求上限', hint: 'RELAY_DEFAULT_RPM' },
  { key: 'pricing_interval_hours', label: '价格同步间隔（小时）', hint: 'RELAY_PRICING_INTERVAL_HOURS' },
  { key: 'official_sync_enabled', label: '启用官方价格源', hint: 'RELAY_PRICING_OFFICIAL' },
  { key: 'sync_on_start', label: '启动时同步价格', hint: 'RELAY_PRICING_SYNC_ON_START' },
  { key: 'redis_enabled', label: '启用 Redis', hint: 'REDIS_ENABLED' }
]

const countCards = [
  { key: 'logs', label: '请求日志' },
  { key: 'payloads', label: '报文留存' },
  { key: 'pricings', label: '定价条目' },
  { key: 'channels', label: '渠道' },
  { key: 'models', label: '模型' },
  { key: 'keys', label: '密钥' },
  { key: 'groups', label: '分组' }
]

function fmtBool(v: any) {
  if (v === true) return '是'
  if (v === false) return '否'
  return v
}

function fmtTime(t: string | null | undefined) {
  if (!t) return '—'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

async function load() {
  loading.value = true
  try {
    const res = await api.get<any>('/settings')
    runtime.value = res.runtime || {}
    counts.value = res.counts || {}
    span.value = res.log_span || {}
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function confirmCleanup() {
  const days = runtime.value.log_retention_days
  Modal.confirm({
    title: '清理过期数据',
    content: '将删除 ' + days + ' 天前的请求日志及其报文，此操作不可撤销。',
    okType: 'danger',
    async onOk() {
      cleaning.value = true
      try {
        const res = await api.post<any>('/settings/cleanup', {})
        message.success(
          '已清理日志 ' + res.deleted_logs + ' 条、报文 ' + (res.deleted_payloads + res.deleted_orphans) + ' 条'
        )
        await load()
      } catch (e: any) {
        message.error(e.message)
      } finally {
        cleaning.value = false
      }
    }
  })
}

async function runSync() {
  syncing.value = true
  try {
    const res = await api.post<{ results: PricingSyncResult[] }>('/pricing/sync', {})
    const added = res.results.reduce((s, r) => s + (r.added || 0), 0)
    const updated = res.results.reduce((s, r) => s + (r.updated || 0), 0)
    message.success('同步完成：新增 ' + added + ' 条，更新 ' + updated + ' 条')
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    syncing.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <section class="panel head-panel">
      <div class="head">
        <div>
          <div class="head-title">系统设置</div>
          <div class="head-sub">运行参数来自容器环境变量，页面只读展示；修改后需重新部署生效。</div>
        </div>
        <a-space>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
          <a-button :loading="syncing" @click="runSync"><SyncOutlined /> 立即同步价格</a-button>
          <a-button danger :loading="cleaning" @click="confirmCleanup"><DeleteOutlined /> 清理过期数据</a-button>
        </a-space>
      </div>
    </section>

    <section class="panel">
      <div class="panel-title">数据概览</div>
      <div class="count-grid">
        <div v-for="c in countCards" :key="c.key" class="count-item">
          <div class="count-value">{{ counts[c.key] ?? 0 }}</div>
          <div class="count-label">{{ c.label }}</div>
        </div>
      </div>
      <div class="span-line">
        日志时间跨度：{{ fmtTime(span.earliest) }} ~ {{ fmtTime(span.latest) }}
      </div>
    </section>

    <section class="panel">
      <div class="panel-title">运行参数</div>
      <a-table
        :data-source="runtimeRows"
        :loading="loading"
        :pagination="false"
        row-key="key"
        size="small"
      >
        <a-table-column title="参数" data-index="label" :width="200" />
        <a-table-column title="当前值" :width="200">
          <template #default="{ record }">
            <span class="mono">{{ fmtBool(runtime[record.key]) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="修改位置" data-index="hint" />
      </a-table>

      <div class="db-line">
        <a-tag :color="runtime.database_ok ? 'green' : 'red'">
          {{ runtime.database_ok ? '数据库正常' : '数据库不可用' }}
        </a-tag>
        <span class="mono dim">{{ runtime.database_version }}</span>
      </div>
    </section>

    <section class="panel note-panel">
      <div class="panel-title">关于报文留存</div>
      <div class="note">
        当前模式为 <span class="mono">{{ runtime.payload_storage_mode }}</span>。
        留存的请求与响应原文会写入 <span class="mono">request_payloads</span> 表，
        在「请求日志」页点开单条记录即可查看；凭据类请求头（Authorization、各类 api-key）
        一律以 <span class="mono">[已隐藏]</span> 落库，不会明文保存。
        超过 <span class="mono">{{ runtime.payload_max_kb }}</span> KB 的报文会被截断并标注。
        想保留全部调用可设为 <span class="mono">all</span>，只留出错调用设为
        <span class="mono">errors</span>，完全不留存设为 <span class="mono">none</span>。
      </div>
    </section>
  </div>
</template>

<style scoped>
.page { padding: var(--gap); display: flex; flex-direction: column; gap: var(--gap); }
.panel { padding: 16px 18px; }
.panel-title { font-weight: 600; margin-bottom: 12px; }
.head-panel { padding: 0; }
.head {
  display: flex; align-items: center; justify-content: space-between;
  gap: var(--gap); padding: 16px 18px; flex-wrap: wrap;
}
.head-title { font-size: 16px; font-weight: 600; }
.head-sub { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.count-grid { display: grid; grid-template-columns: repeat(7, 1fr); gap: 12px; }
.count-item { text-align: center; padding: 10px 4px; border-radius: 8px; background: var(--color-bg); }
.count-value { font-size: 20px; font-weight: 600; color: var(--color-primary); }
.count-label { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.span-line { margin-top: 12px; font-size: 12px; color: var(--color-text-secondary); }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.dim { color: var(--color-text-secondary); font-size: 12px; }
.db-line { margin-top: 12px; display: flex; align-items: center; gap: 8px; }
.note-panel .note { font-size: 13px; line-height: 1.9; color: var(--color-text-secondary); }
</style>
