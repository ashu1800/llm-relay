<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  ReloadOutlined, DeleteOutlined,
  DownloadOutlined, UploadOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'

const loading = ref(false)
// 这一页加载的是「多项设置」而不是列表，没有 length 可数，
// 于是用「是否成功加载过一次」来区分首次加载失败与刷新失败
const loaded = ref(false)
// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
// 计数卡与运行参数会显示成 0 和空值，被读成「本来就没有数据」
const loadError = ref('')
const runtime = ref<Record<string, any>>({})
const counts = ref<Record<string, number>>({})
const span = ref<Record<string, string | null>>({})
const cleaning = ref(false)

const exporting = ref(false)
const importing = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const reportOpen = ref(false)
const report = ref<{ created: Record<string, number>; skipped: Record<string, number>; warnings: string[] }>({
  created: {}, skipped: {}, warnings: []
})

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
  loadError.value = ''
  try {
    const res = await api.get<any>('/settings')
    runtime.value = res.runtime || {}
    counts.value = res.counts || {}
    span.value = res.log_span || {}
    loaded.value = true
  } catch (e: any) {
    loadError.value = e.message || '加载失败'
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

// 导出走 fetch 而不是直接开新标签页，这样才能把失败原因显示出来
async function exportConfig() {
  exporting.value = true
  try {
    const res = await fetch('/api/admin/backup/export')
    if (!res.ok) throw new Error('导出失败 ' + res.status)
    const blob = await res.blob()
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = 'llm-relay-backup-' + new Date().toISOString().slice(0, 19).replace(/[:T]/g, '') + '.json'
    a.click()
    URL.revokeObjectURL(a.href)
    message.success('已导出')
  } catch (e: any) {
    message.error(e.message)
  } finally {
    exporting.value = false
  }
}

function pickFile() {
  fileInput.value?.click()
}

async function onFilePicked(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = '' // 允许连续导入同一个文件
  if (!file) return
  importing.value = true
  try {
    const text = await file.text()
    const res = await api.post<any>('/backup/import', JSON.parse(text))
    report.value = {
      created: res.created || {},
      skipped: res.skipped || {},
      warnings: res.warnings || []
    }
    reportOpen.value = true
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    importing.value = false
  }
}

function sumOf(m: Record<string, number>) {
  return Object.values(m).reduce((a, b) => a + b, 0)
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
          <a-button danger :loading="cleaning" @click="confirmCleanup"><DeleteOutlined /> 清理过期数据</a-button>
        </a-space>
      </div>
    </section>

    <DataState
      :error="loadError"
      :has-data="loaded"
      :loading="loading"
      title="系统设置加载失败"
      hint="这一页的数据来自后端 /settings 接口，请确认后端服务是否正常，然后重试。"
      @retry="load"
    >
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
        <template #emptyText>
          <a-empty description="运行参数列表为空，请确认后端版本与前端的参数项一致，然后点「刷新」重试" />
        </template>
      </a-table>

      <div class="db-line">
        <a-tag :color="runtime.database_ok ? 'green' : 'red'">
          {{ runtime.database_ok ? '数据库正常' : '数据库不可用' }}
        </a-tag>
        <span class="mono dim">{{ runtime.database_version }}</span>
      </div>
    </section>

    <section class="panel">
      <div class="panel-title">配置备份</div>
      <div class="note">
        导出渠道、模型、绑定、模板、密钥与手工定价，用于换机或重装后快速恢复。
        调用日志与统计属于运行数据，请直接备份数据库卷。
      </div>
      <div class="backup-note">
        渠道密钥在备份里始终是<strong>密文</strong>，只有同一把
        <span class="mono">RELAY_SECRET</span> 才能解出原文。备份文件里记有主密钥指纹，
        导入到不同密钥的实例时会明确提示需要重填上游密钥。
      </div>
      <a-space style="margin-top: 12px">
        <a-button :loading="exporting" @click="exportConfig"><DownloadOutlined /> 导出配置</a-button>
        <a-button :loading="importing" @click="pickFile"><UploadOutlined /> 导入配置</a-button>
        <input ref="fileInput" type="file" accept="application/json,.json" style="display: none" @change="onFilePicked" />
      </a-space>
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
    </DataState>

    <a-modal v-model:open="reportOpen" title="导入结果" :footer="null" width="520px">
      <a-descriptions :column="1" bordered size="small">
        <a-descriptions-item label="新增">
          {{ sumOf(report.created) }} 项
          <span v-if="sumOf(report.created)" class="dim">
            （<span v-for="(v, k) in report.created" :key="k">{{ k }} {{ v }} </span>）
          </span>
        </a-descriptions-item>
        <a-descriptions-item label="已存在跳过">
          {{ sumOf(report.skipped) }} 项
          <span v-if="sumOf(report.skipped)" class="dim">
            （<span v-for="(v, k) in report.skipped" :key="k">{{ k }} {{ v }} </span>）
          </span>
        </a-descriptions-item>
      </a-descriptions>
      <a-alert
        v-for="(w, i) in report.warnings"
        :key="i"
        type="warning"
        show-icon
        :message="w"
        style="margin-top: 8px"
      />
      <div class="note" style="margin-top: 12px">
        已存在的条目一律保留本机版本不覆盖。密钥导入后即可继续使用，无需重新签发。
      </div>
    </a-modal>

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
.note { font-size: 13px; line-height: 1.9; color: var(--color-text-secondary); }
.backup-note {
  margin-top: 8px; padding: 10px 12px; border-radius: 8px;
  background: var(--color-bg); font-size: 12px; line-height: 1.8;
  color: var(--color-text-secondary);
}
</style>
