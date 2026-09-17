<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  ReloadOutlined, DeleteOutlined,
  DownloadOutlined, UploadOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import LogFxPreview from '@/components/LogFxPreview.vue'
import { useLogFxStore } from '@/stores/logFx'
import { fmtTime } from '@/utils/fmtTime'

// 请求日志的入场动效：这一个不是只读的运行参数，而是可以在这里改的浏览器本地偏好
// （存 localStorage，与主题、每页条数同一口径）。档位表与持久化都在 utils/effects.ts，
// store 是看板那一块与这一页共用的同一个实例，所以在这里点一下，回看板立刻生效。
const logFx = useLogFxStore()

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
// 每个运行时字段由哪个环境变量决定，由后端 /settings 的 env_keys 提供。
// 之前这份清单在前端手写，13 条里错了 6 条（RELAY_LOG_LEVEL、
// RELAY_UPSTREAM_TIMEOUT 之类根本不存在的变量名），用户照着设完全没有反应，
// 而且不会报错 —— 比不提示更糟。改为以后端为准，两边不会再漂移。
const envKeys = ref<Record<string, string>>({})

const exporting = ref(false)
const importing = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const reportOpen = ref(false)
const report = ref<{ created: Record<string, number>; skipped: Record<string, number>; warnings: string[] }>({
  created: {}, skipped: {}, warnings: []
})

// 运行参数只读：它们来自环境变量与 yaml，进程启动后不可变。
// 与其做出改了不生效的假开关，不如直接告诉用户改哪里。
//
// hint 的来源见上方 envKeys 的说明：不再手写字面量，改从后端拿。
const runtimeRows = [
  { key: 'listen', label: '监听地址' },
  { key: 'mode', label: '运行模式' },
  { key: 'log_level', label: '日志级别' },
  { key: 'log_format', label: '日志格式' },
  { key: 'upstream_timeout_sec', label: '上游总超时（秒）' },
  { key: 'first_byte_timeout_sec', label: '首字节超时（秒）' },
  { key: 'max_retries', label: '最大重试次数' },
  { key: 'max_request_body_mb', label: '请求体上限（MB）' },
  { key: 'log_retention_days', label: '日志保留天数' },
  { key: 'payload_storage_mode', label: '报文留存模式' },
  { key: 'payload_max_kb', label: '单条报文上限（KB）' },
  { key: 'max_concurrency', label: '上游并发上限' },
  { key: 'default_rpm', label: '默认每分钟请求上限' }
]

// listen 的提示是特殊的一条：它由 SERVER_HOST/SERVER_PORT 决定，
// 而容器部署下宿主机侧的端口还多一层 BIND_ADDR 映射，所以单独说明。
const LISTEN_HINT = 'SERVER_HOST / SERVER_PORT（Docker 部署的宿主机端口另由 deploy/.env 的 BIND_ADDR 决定）'

// hintOf 取该字段对应的环境变量名。拿不到时返回 undefined，
// 界面上不显示提示 —— 宁可不说，也不给一个错的变量名。
function hintOf(key: string): string | undefined {
  if (key === 'listen') return LISTEN_HINT
  return envKeys.value[key]
}

const countCards = [
  { key: 'logs', label: '请求日志' },
  { key: 'payloads', label: '报文留存' },
  { key: 'priced', label: '已定价模型' },
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

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<any>('/settings')
    runtime.value = res.runtime || {}
    counts.value = res.counts || {}
    span.value = res.log_span || {}
    envKeys.value = res.env_keys || {}
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
    centered: true,
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
      <div class="panel-title">界面动效</div>
      <div class="note">
        请求日志列表里<strong>实时新增</strong>一条记录时播的入场动画。选中即刻生效，
        回数据看板就能看到；手动刷新、翻页、改筛选时都不会播 —— 那些操作整屏都在换，
        闪一下没有信息量，而「凭空多出来一行」才需要提示。
        没有「完全关闭」这一档：系统开了「减弱动态效果」（各系统的无障碍开关）时，
        全站动画会自动压到几乎瞬时，那才是统一的静音路径。
      </div>
      <!-- 原生 radio 而不是按钮组：它天生可键盘操作、读屏会念「已选中」。
           卡片本身是 label，点哪儿都能选中（含那块迷你预览）。 -->
      <div class="fx-grid" role="radiogroup" aria-label="请求日志新增记录的入场动效">
        <label
          v-for="opt in logFx.options"
          :key="opt.id"
          class="fx-item"
          :class="{ 'is-active': logFx.fx === opt.id }"
        >
          <input
            class="fx-radio"
            type="radio"
            name="log-fx"
            :value="opt.id"
            :checked="logFx.fx === opt.id"
            @change="logFx.setFx(opt.id)"
          />
          <LogFxPreview :fx="opt.id" />
          <span class="fx-name">
            {{ opt.name }}
            <span v-if="opt.id === 'sweep'" class="fx-default">默认</span>
            <span v-if="logFx.fx === opt.id" class="fx-check" aria-hidden="true">✓</span>
          </span>
          <span class="fx-desc">{{ opt.desc }}</span>
        </label>
      </div>
      <div class="span-line">
        这是浏览器本地偏好（存在这台机器的这个浏览器里，与上面的运行参数无关，
        也不进配置备份）；换机器或换浏览器需要重新选一次。
        当前档位：<span class="mono">{{ logFx.options.find((o) => o.id === logFx.fx)?.name }}</span>
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
        <a-table-column title="修改位置" :width="280">
          <template #default="{ record }">
            <!-- 拿不到环境变量名时不显示，而不是显示一个错的 -->
            <span v-if="hintOf(record.key)" class="mono">{{ hintOf(record.key) }}</span>
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
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
        留存的请求与响应原文会写入 <span class="mono">request_payloads</span> 表
        （界面不再展示，需要直接查询数据库）；凭据类请求头（Authorization、各类 api-key）
        一律以 <span class="mono">[已隐藏]</span> 落库，不会明文保存。
        超过 <span class="mono">{{ runtime.payload_max_kb }}</span> KB 的报文会被截断并标注。
        想保留全部调用可设为 <span class="mono">all</span>，只留出错调用设为
        <span class="mono">errors</span>，完全不留存设为 <span class="mono">none</span>。
      </div>
    </section>
    </DataState>

    <a-modal v-model:open="reportOpen" title="导入结果" :footer="null" :width="'min(520px, 94vw)'" centered>
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
/* 窄屏退档：7 列在 <900px 时每格只剩几十像素，四五个字的标签（「已定价模型」）
   换行、卡高参差；对照看板四卡的两档退档 */
@media (max-width: 900px) {
  .count-grid { grid-template-columns: repeat(4, 1fr); }
}
@media (max-width: 600px) {
  .count-grid { grid-template-columns: repeat(2, 1fr); }
}
.count-item { text-align: center; padding: 10px 4px; border-radius: 8px; background: var(--color-bg); }
/* 数值是正文，用 ink 版；--color-primary 在 --color-bg 上只有 3.05:1 */
.count-value { font-size: 20px; font-weight: 600; color: var(--text-primary-ink); }
.count-label { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.span-line { margin-top: 12px; font-size: 12px; color: var(--color-text-secondary); line-height: 1.9; }

/* 界面动效那三张卡：一格一张，整格可点（label 包着 radio）。
   选中的那一张用主色描边 + 主色浅底 —— 与 StatCard 的 tone 底同一手法
   （color-mix 派生，不写死色值，深色主题自动换档）。 */
.fx-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-top: 12px; }
@media (max-width: 900px) {
  .fx-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 600px) {
  .fx-grid { grid-template-columns: 1fr; }
}
.fx-item {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg);
  cursor: var(--cursor-hand);
  transition: border-color 0.15s ease, background-color 0.15s ease;
}
.fx-item:hover { border-color: color-mix(in srgb, var(--color-primary) 45%, var(--color-border)); }
.fx-item.is-active {
  border-color: var(--color-primary);
  background: color-mix(in srgb, var(--color-primary) 8%, var(--color-fg));
}
/* radio 本身藏起来但不能 display:none：那样键盘就聚焦不到它了。
   用 1px + opacity 0 保留可聚焦、可被读屏读到。 */
.fx-radio {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
  pointer-events: none;
}
/* 键盘焦点必须看得见（项目一贯口径）：焦点在藏起来的 radio 上，
   把焦点环画到卡片上 */
.fx-item:focus-within {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}
.fx-name { font-size: 13px; font-weight: 600; color: var(--text-primary-ink); display: flex; align-items: center; gap: 6px; }
.fx-item:not(.is-active) .fx-name { color: var(--color-text); }
/* 「默认」小标：告诉用户这一档是没人选过时的行为，换档之后想改回来是哪一个 */
.fx-default {
  font-size: 10px;
  font-weight: 500;
  line-height: 1;
  padding: 3px 5px;
  border-radius: 4px;
  color: var(--color-text-secondary);
  background: var(--color-border);
}
.fx-check { color: var(--color-primary); }
.fx-desc { font-size: 12px; line-height: 1.7; color: var(--color-text-secondary); }
/* 等宽片段用全站那一套等宽字族，而不是就地写死一串：
   写死的 ui-monospace/monospace 没有中文回退，正文里「[已隐藏]」这种带中文的
   片段会落到浏览器给 monospace 配的中文字体（Windows 上又是宋体），
   于是又出现一种和左侧菜单不一致的中文字。变量里已经补好了中文回退。 */
.mono { font-family: var(--font-family-mono); }
.muted { color: var(--color-text-secondary); }
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
