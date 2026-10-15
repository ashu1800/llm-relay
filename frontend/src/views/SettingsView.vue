<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { message, modal } from '@/utils/feedback'
import {
  ReloadOutlined, DeleteOutlined,
  DownloadOutlined, UploadOutlined
} from '@ant-design/icons-vue'
import { api } from '@/api/client'
import { versionApi, type UpdateConfig } from '@/api/version'
import { clientAliasApi } from '@/api/clientAliases'
import { modalWidth } from '@/utils/modalWidth'
import DataState from '@/components/DataState.vue'
import EmptyState from '@/components/EmptyState.vue'
import LogFxPreview from '@/components/LogFxPreview.vue'
import ThemePicker from '@/components/ThemePicker.vue'
import ClientIcon from '@/components/ClientIcon.vue'
import { CLIENT_META, ICON_LIBRARY, clientIconFile } from '@/components/clientMeta'
import { aliasIconSpec, type ClientIconSpec } from '@/components/clientDisplay'
import { useClientAliases } from '@/composables/useClientAliases'
import { useLogFxStore } from '@/stores/logFx'
import { useThemeStore } from '@/stores/theme'
import { useVersionStore } from '@/stores/version'
import { fmtTime } from '@/utils/fmtTime'

// 请求日志的入场动效：这一个不是只读的运行参数，而是可以在这里改的浏览器本地偏好
// （存 localStorage，与主题、每页条数同一口径）。档位表与持久化都在 utils/effects.ts，
// store 是看板那一块与这一页共用的同一个实例，所以在这里点一下，回看板立刻生效。
const logFx = useLogFxStore()

// 界面主题（2026-09-30 起是 12 套配色，不再只有「深色质感档位」两档）：
// 数据来自 theme store（看板那枚按钮与这里是同一个实例），
// 选择器本体是 components/ThemePicker.vue —— 看板弹层用的是同一个组件的紧凑形态。
const themeStore = useThemeStore()

// ---- 版本更新设置 ----
//
// 这里改的是「检测更新」这个行为本身：开关、发布源、出站代理、GitHub token。
// 保存后要让版本 store 重新拉一次状态（徽标的可用性可能变了），
// 并清掉已缓存的检测结果 —— 换了仓库却还显示旧仓库的结论会很误导。
const versionStore = useVersionStore()
const updateCfg = ref<UpdateConfig | null>(null)
const updateSaving = ref(false)
const updateMsg = ref('')
const updateForm = ref({ enabled: true, repo: '', proxy: '', token: '' })

async function loadUpdateCfg() {
  try {
    const cfg = await versionApi.getConfig()
    updateCfg.value = cfg
    updateForm.value = {
      enabled: cfg.enabled,
      repo: cfg.repo,
      proxy: cfg.proxy,
      // token 永不回显：后端只回 has_token 这个布尔位。
      // 留空表示「不修改」，而不是「清空」—— 两者的区别见下面 clearUpdateToken
      token: ''
    }
  } catch {
    // 更新服务可能整体不可用（例如二进制/源码部署时被关掉）。
    // 这里不报错：这一页还有别的设置，为一个可选功能打断整页不合理
    updateCfg.value = null
  }
}

async function saveUpdateCfg() {
  updateSaving.value = true
  updateMsg.value = ''
  try {
    await versionApi.saveConfig({
      enabled: updateForm.value.enabled,
      repo: updateForm.value.repo.trim(),
      proxy: updateForm.value.proxy.trim(),
      // 空串不发出去：后端按「字段缺失 = 不修改」处理，这样
      // 「没动 token 输入框」就不会把已配置的 token 清掉
      ...(updateForm.value.token.trim() ? { token: updateForm.value.token.trim() } : {})
    })
    updateMsg.value = '已保存'
    await loadUpdateCfg()
    // 换了仓库或代理之后，之前那份检测结果已经不适用了
    versionStore.clearCheck()
    void versionStore.fetchInfo()
    message.success('版本更新设置已保存')
  } catch (e: any) {
    updateMsg.value = ''
    message.error(e?.message || '保存失败')
  } finally {
    updateSaving.value = false
  }
}

async function clearUpdateToken() {
  updateSaving.value = true
  updateMsg.value = ''
  try {
    // 显式传空串表达「清掉它」。
    //
    // 这里必须用空串而不是 null：Go 把 JSON 的 null 和「字段不存在」都
    // 解析成 nil 指针，服务端区分不了，于是 null 会被当成「不修改」，
    // 清除操作静默失效 —— 界面上会看到「已清除」的提示，而 token 还在。
    await versionApi.saveConfig({
      enabled: updateForm.value.enabled,
      repo: updateForm.value.repo.trim(),
      proxy: updateForm.value.proxy.trim(),
      token: ''
    })
    updateForm.value.token = ''
    updateMsg.value = 'Token 已清除'
    await loadUpdateCfg()
    message.success('Token 已清除')
  } catch (e: any) {
    message.error(e?.message || '清除失败')
  } finally {
    updateSaving.value = false
  }
}

// ---- 客户端显示映射（对照表）----
//
// 行由后端在日志观测时自动登记（含启动回填），这里只负责配名字与图标。
// 与日志面板共用同一份共享状态（useClientAliases 是模块级单例），
// 这里保存完，回看板的客户端列立刻按新叫法显示。
const { aliases: aliasRows, load: loadAliases } = useClientAliases()

// 每行的编辑草稿。保存后整体重取（服务端回读的行会带新的 updated_at），
// watch 触发重建 —— 未保存的改动只活在草稿里，重取即丢（与渠道表单
// 的脏检查不同：这里的行小而多，逐行「未保存提示」比直接覆盖更吵）。
const aliasDrafts = ref<Record<number, { name: string; icon: string; saving: boolean }>>({})
watch(aliasRows, (rows) => {
  const next: typeof aliasDrafts.value = {}
  for (const r of rows) next[r.id] = { name: r.name, icon: r.icon, saving: false }
  aliasDrafts.value = next
}, { immediate: true })

// 行的三种状态：待设置（内置表也没有，列上显示的是原始 token —— 真正
// 要人来的）、已映射（用户配过）、内置叫法（CLIENT_META 认识、没配映射，
// 显示本来就好，配不配随意）。后端的排序只把「没配名字」的放前面，
// 这里再按状态重排：真正可操作的（待设置）浮到最前，纯噪音（内置未配）
// 沉底 —— 40 多个内置客户端都自动登记，不重排的话 deepseek-harness
// 这种要配的行会埋在里面。
function aliasStatus(row: { key: string; name: string }): 'pending' | 'mapped' | 'builtin' {
  if (row.name) return 'mapped'
  if (CLIENT_META[row.key]) return 'builtin'
  return 'pending'
}

const sortedAliasRows = computed(() => {
  const order = { pending: 0, mapped: 1, builtin: 2 } as const
  return [...aliasRows.value].sort(
    (a, b) =>
      order[aliasStatus(a)] - order[aliasStatus(b)] ||
      b.updated_at.localeCompare(a.updated_at)
  )
})
const pendingAliasCount = computed(() => aliasRows.value.filter((r) => aliasStatus(r) === 'pending').length)

// 面板里的图标预览：配了就按配置渲染，没配按「这一行现在实际显示的
// 图标」渲染（内置品牌图标 / 默认图标）—— 预览必须与日志列同源，
// 否则这里看到的效果和列上不一样。
function aliasPreviewSpec(row: { key: string; icon: string }): ClientIconSpec {
  return aliasIconSpec(row.icon) ?? { kind: 'builtin', file: clientIconFile(row.key) }
}

// 图标下拉的选项：文件名去掉 .svg 后缀更好读，value 保留全名（落库的
// 就是它，与 clientDisplay 的识别口径一致）
const iconOptions = ICON_LIBRARY.map((f) => ({ value: f, label: f.replace(/\.svg$/, '') }))

async function saveAlias(row: { id: number; key: string }) {
  const d = aliasDrafts.value[row.id]
  if (!d) return
  d.saving = true
  try {
    await clientAliasApi.update(row.id, { name: d.name.trim(), icon: d.icon.trim() })
    await loadAliases(true)
    message.success('已保存，日志列已按新叫法显示')
  } catch (e: any) {
    message.error(e?.message || '保存失败')
  } finally {
    d.saving = false
  }
}

function removeAlias(row: { id: number; key: string }) {
  modal.confirm({
    centered: true,
    title: '删除这条映射',
    content:
      '删除后「' + row.key + '」回落到内置叫法 / 原始名显示；它下次被观测到时会重新登记一行待设置。',
    okType: 'danger',
    async onOk() {
      try {
        await clientAliasApi.remove(row.id)
        await loadAliases(true)
        message.success('已删除')
      } catch (e: any) {
        message.error(e?.message || '删除失败')
      }
    }
  })
}

const loading = ref(false)
// 这一页加载的是「多项设置」而不是列表，没有 length 可数，
// 于是用「是否成功加载过一次」来区分首次加载失败与刷新失败
const loaded = ref(false)// 加载失败必须留下痕迹：只弹一个转瞬即逝的 message 的话，
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
  { key: 'console_auth_enabled', label: '管理台登录鉴权' },
  { key: 'session_ttl_hours', label: '登录会话有效期（小时）' },
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
  { key: 'default_rpm', label: '默认每分钟请求上限' },
  { key: 'retry_same_upstream_delay_ms', label: '同上游重试等待（毫秒）' }
]

// listen 的提示是特殊的一条：它由 SERVER_HOST/SERVER_PORT 决定，
// 改了要重启服务才生效，所以单独说明。
const LISTEN_HINT = 'SERVER_HOST / SERVER_PORT（在 deploy/.env 中修改后需重启服务）'

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
  modal.confirm({
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

onMounted(() => {
  void load()
  // 更新设置独立加载：更新服务可能整体不可用（源码部署等），
  // 那种情况下其余设置仍应正常显示
  void loadUpdateCfg()
  // 显示映射独立加载并已降级（失败只回落内置显示），不挡这一页
  void loadAliases(true)
})
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
      <h2 class="panel-title">数据概览</h2>
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
      <h2 class="panel-title">界面主题</h2>
      <div class="note">
        12 套内置配色：浅色 4 套、深色 8 套（深色里含为 OLED 屏准备的「深空纯黑」）。
        本页与<strong>数据看板右上角那枚按钮</strong>用的是同一个选择器，选哪边都一样。
        每套配色的正文、次要文字、占位文字、语义色与实心按钮都按 WCAG AA（4.5:1）实测过，
        数值由 <span class="mono">scripts/check-theme-contrast.mjs</span> 在 CI 里重算。
        配色取自公开项目（Catppuccin / Nord / Dracula / Tokyo Night / Gruvbox / Solarized，
        均 MIT 许可），出处与偏差记在 <span class="mono">styles/theme.css</span> 各主题块的注释里。
      </div>
      <!-- 选择器是共享组件：看板弹层用的是同一个实现的紧凑形态，
           所以「弹层里选中的」与「这一页选中的」永远说的是同一件事 -->
      <ThemePicker />
      <div class="span-line">
        与入场动效一样是浏览器本地偏好，不进配置备份。
        当前主题：<span class="mono">{{ themeStore.current.name }}（{{ themeStore.current.upstream }}）</span>
      </div>
    </section>

    <section class="panel">
      <h2 class="panel-title">界面动效</h2>
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
      <h2 class="panel-title">
        客户端显示映射
        <span v-if="pendingAliasCount > 0" class="alias-badge">{{ pendingAliasCount }} 个待设置</span>
      </h2>
      <div class="note">
        请求日志「客户端」列的名字与图标按
        <strong>这里的映射 &gt; 内置叫法 &gt; 识别原名</strong> 显示。
        行由日志自动登记：新客户端第一次被观测到就会出现在这里；配好后历史行与新行
        一起换新叫法（库里仍保留识别当时的原名作快照，CSV 导出同样按映射后的名字）。
        图标可选内置库，或粘贴 data URI / 图片地址 / 单个字符。
      </div>
      <div v-if="sortedAliasRows.length === 0" class="span-line">
        还没有观测到任何客户端 —— 有请求进来后会自动出现在这里。
      </div>
      <!-- 行编辑器（与渠道白名单编辑器同一形态）：一行 = 一个客户端，
           直接行内保存。列头用普通 div 而不是 a-table：行内是多种控件
           混排，表格的列语义（排序/对齐）在这里没有收益。 -->
      <div v-else class="alias-wrap" role="table" aria-label="客户端显示映射">
        <div class="alias-row alias-head" aria-hidden="true">
          <span>客户端</span><span>显示名</span><span>内置图标</span><span>自定义图标</span>
          <span class="alias-ops-head"></span>
        </div>
        <div v-for="(row, i) in sortedAliasRows" :key="row.id" class="alias-row">
          <div class="alias-key">
            <ClientIcon :spec="aliasPreviewSpec(row)" :size="18" />
            <span class="mono alias-key-text" :title="row.key">{{ row.key }}</span>
            <!-- 待设置 = 内置表也没有它，列上显示的是原始 token（要人来的行）；
                 内置 = 没配映射但显示本来就好（可配可不配） -->
            <span v-if="aliasStatus(row) === 'pending'" class="alias-pill is-pending">待设置</span>
            <span v-else-if="aliasStatus(row) === 'builtin'" class="alias-pill is-builtin">内置</span>
          </div>
          <a-input
            v-model:value="aliasDrafts[row.id].name"
            :maxlength="64"
            :aria-label="`第 ${i + 1} 行显示名`"
            placeholder="如 DSH"
            @press-enter="saveAlias(row)"
          />
          <a-select
            :value="ICON_LIBRARY.includes(aliasDrafts[row.id].icon) ? aliasDrafts[row.id].icon : undefined"
            :options="iconOptions"
            :aria-label="`第 ${i + 1} 行内置图标`"
            placeholder="默认图标"
            allow-clear
            @change="(v: any) => (aliasDrafts[row.id].icon = v ?? '')"
          />
          <a-input
            :value="ICON_LIBRARY.includes(aliasDrafts[row.id].icon) ? '' : aliasDrafts[row.id].icon"
            :aria-label="`第 ${i + 1} 行自定义图标`"
            placeholder="data:… / https://… / 单字符"
            @update:value="(v: string) => (aliasDrafts[row.id].icon = v)"
          />
          <span class="alias-ops">
            <a-button
              size="small"
              :loading="aliasDrafts[row.id].saving"
              :disabled="aliasDrafts[row.id].name === row.name && aliasDrafts[row.id].icon === row.icon"
              @click="saveAlias(row)"
            >保存</a-button>
            <a-button size="small" danger @click="removeAlias(row)">删除</a-button>
          </span>
        </div>
      </div>
    </section>

    <section class="panel">
      <h2 class="panel-title">运行参数</h2>
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
          <EmptyState size="sm" text="运行参数列表为空，请确认后端版本与前端的参数项一致，然后点「刷新」重试" />
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
      <h2 class="panel-title">登录认证</h2>
      <!-- 登录鉴权的现状总览。密钥永远不回显（后端只存哈希、接口只出布尔位），
           这里回答的是三件事：开没开、会话多久、想改去哪里改 -->
      <div class="auth-line">
        <a-tag :color="runtime.console_auth_enabled ? 'green' : 'red'">
          {{ runtime.console_auth_enabled ? '已启用' : '未启用' }}
        </a-tag>
        <span v-if="runtime.console_auth_enabled" class="dim">
          会话有效期 <span class="mono">{{ runtime.session_ttl_hours }}</span> 小时，登录限流为 15 分钟内错 5 次锁定
        </span>
        <span v-else class="dim">
          管理接口没有任何登录门槛，仅绑定回环（127.0.0.1）时方可接受；公网部署必须启用
        </span>
      </div>
      <div class="note">
        本站采用<strong>无账号的密钥登录</strong>：不设用户名、没有注册与找回，全站只有一把管理密钥。
        它来自服务器环境变量 <span class="mono">RELAY_ADMIN_KEY</span>（长度至少 16 位），只在启动时取哈希，
        不落库、不进配置备份。登录成功后会话保存在 HttpOnly Cookie 中；
        <strong>轮换密钥并重启后，所有已登录会话立即失效</strong>，这是无状态会话唯一的吊销手段。
        会话时长可用 <span class="mono">RELAY_SESSION_TTL</span>（Go duration 写法，默认 168h，上限 720h）调整。
        经 HTTPS 反代部署时登录 Cookie 自动附加 Secure 标志。
      </div>
    </section>

    <section class="panel">
      <h2 class="panel-title">版本更新</h2>
      <!-- 更新检测的来源与凭据。侧栏那枚版本徽标是操作入口，
           这里是它背后的设置 —— 为什么需要设置见下面的说明文字。 -->
      <div v-if="!updateCfg" class="dim">正在加载…</div>
      <template v-else>
        <div class="auth-line">
          <a-tag :color="updateCfg.enabled ? 'green' : 'default'">
            {{ updateCfg.enabled ? '检测已启用' : '检测已关闭' }}
          </a-tag>
          <a-tag :color="updateCfg.has_token ? 'green' : 'orange'">
            {{ updateCfg.has_token ? '已配 Token' : '未配 Token（限额 60 次/时）' }}
          </a-tag>
          <span class="mono dim">{{ updateCfg.repo }}</span>
        </div>

        <a-form layout="vertical" class="upd-form" @submit.prevent="saveUpdateCfg">
          <a-form-item label="检测新版本">
            <a-switch v-model:checked="updateForm.enabled" />
            <span class="dim upd-hint">关闭后不发任何外部请求，版本徽标只显示当前版本</span>
          </a-form-item>

          <a-form-item label="发布源仓库">
            <a-input v-model:value="updateForm.repo" :placeholder="updateCfg.repo_default" class="mono" />
            <span class="dim upd-hint">形如 owner/name。换成本仓库的 fork 时填这里</span>
          </a-form-item>

          <a-form-item label="出站代理（可选）">
            <a-input v-model:value="updateForm.proxy" placeholder="socks5://127.0.0.1:1080 或 http://…" class="mono" />
            <span class="dim upd-hint">
              留空即直连。国内网络访问 GitHub 不稳定时填它，更新时的下载也走同一个代理。
              若「代理管理」里已有勾选「用于自动更新」的启用代理，则优先用它，这里的地址退为备用
            </span>
          </a-form-item>

          <a-form-item label="GitHub Token（可选）">
            <a-input-password
              v-model:value="updateForm.token"
              :placeholder="updateCfg.has_token ? '已配置，留空则不修改' : 'ghp_… 或 github_pat_…'"
              class="mono"
            />
            <span class="dim upd-hint">
              只为提高 API 限额（匿名每小时 60 次）。它<strong>只发往 api.github.com</strong>：
              请求被重定向到别的域名时会被主动剥掉，因此不必担心它跟着跳到别处
            </span>
          </a-form-item>

          <div class="upd-actions">
            <a-button type="primary" :loading="updateSaving" @click="saveUpdateCfg">保存</a-button>
            <a-button
              v-if="updateCfg.has_token"
              danger
              :loading="updateSaving"
              @click="clearUpdateToken"
            >
              清除 Token
            </a-button>
            <span v-if="updateMsg" class="dim">{{ updateMsg }}</span>
          </div>
        </a-form>
      </template>
    </section>

    <section class="panel">
      <h2 class="panel-title">配置备份</h2>
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
      <h2 class="panel-title">关于报文留存</h2>
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

    <a-modal v-model:open="reportOpen" title="导入结果" :footer="null" :width="modalWidth(520)" centered>
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
/* 面板标题是 <h2>（2026-10-10 标题层级收口）：文档结构变成
   h1（MainLayout 的 .sr-only 页面名）→ h2（本页八个面板）→ h3（子块）。
   字号与字重显式写出来 —— h2 的浏览器默认样式是 1.5em + 上下 margin，
   不覆盖的话这八个标题会从 16px 跳到 24px 并把面板撑高。 */
.panel-title { margin: 0 0 12px; font-size: var(--font-size-base); font-weight: 600; }
.head-panel { padding: 0; }
.head {
  display: flex; align-items: center; justify-content: space-between;
  gap: var(--gap); padding: 16px 18px; flex-wrap: wrap;
}
.head-title { font-size: var(--font-size-base); font-weight: 600; }
.head-sub { margin-top: 4px; font-size: var(--font-size-sm); color: var(--color-text-secondary); }
.count-grid { display: grid; grid-template-columns: repeat(7, 1fr); gap: 12px; }
/* 窄屏退档：7 列在窄视口下每格只剩几十像素，四五个字的标签（「已定价模型」）
   换行、卡高参差；对照看板四卡的两档退档。
   断点走 theme.css 的刻度（640 / 768），不再用 900 / 600 ——
   刻度那一节写明 900 只属于「侧栏自动折叠」这一个功能断点
   （MainLayout 的 JS 常量），面板内栅格向刻度对齐。2026-10-10 收口。 */
@media (max-width: 768px) {
  .count-grid { grid-template-columns: repeat(4, 1fr); }
}
@media (max-width: 640px) {
  .count-grid { grid-template-columns: repeat(2, 1fr); }
}
.count-item { text-align: center; padding: 10px 4px; border-radius: 8px; background: var(--color-bg); }
/* 数值是正文，用 ink 版；--color-primary 在 --color-bg 上只有 3.05:1 */
.count-value { font-size: var(--font-size-display-sm); font-weight: 600; color: var(--text-primary-ink); }
.count-label { margin-top: 4px; font-size: var(--font-size-sm); color: var(--color-text-secondary); }
.span-line { margin-top: 12px; font-size: var(--font-size-sm); color: var(--color-text-secondary); line-height: 1.9; }

/* 界面动效那三张卡：一格一张，整格可点（label 包着 radio）。
   选中的那一张用主色描边 + 主色浅底 —— 与 StatCard 的 tone 底同一手法
   （color-mix 派生，不写死色值，深色主题自动换档）。 */
.fx-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-top: 12px; }
/* 断点同上面的 count-grid：向 theme.css 的刻度对齐（原为 900 / 600） */
@media (max-width: 768px) {
  .fx-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 640px) {
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
   把焦点环画到卡片上。
   这是全站唯一不能直接用 theme.css 那条 :focus-visible 的地方 ——
   被聚焦的元素是不可见的（1px 透明），环必须由它的容器承担；
   值仍取同一套令牌，颜色与偏移因此与别处一致（2026-09-24 P1-7）。 */
.fx-item:focus-within {
  outline: var(--focus-ring);
  outline-offset: var(--focus-ring-offset);
  box-shadow: var(--focus-ring-halo-shadow);
}
.fx-name { font-size: var(--font-size-md); font-weight: 600; color: var(--text-primary-ink); display: flex; align-items: center; gap: 6px; }
.fx-item:not(.is-active) .fx-name { color: var(--color-text); }
/* 「默认」小标：告诉用户这一档是没人选过时的行为，换档之后想改回来是哪一个 */
.fx-default {
  font-size: var(--font-size-2xs);
  font-weight: 500;
  line-height: 1;
  padding: 3px 5px;
  border-radius: 4px;
  color: var(--color-text-secondary);
  background: var(--color-border);
}
/* 勾选标记：图标也要 ≥3:1（WCAG 1.4.11）。--color-primary 在
   solarized-light 上恰好 3.00:1，贴线没有余量，改用主色的文字档
   （同一色相压深/提亮后的那一档，各主题都留了余量）。 */
.fx-check { color: var(--text-primary-ink); }
.fx-desc { font-size: var(--font-size-sm); line-height: 1.7; color: var(--color-text-secondary); }

/* ---- 主题预览色样（2026-09-27 立，2026-09-30 随多主题迁走）----
   原来这里按「深色质感档位」写死两套色值（.ds-swatch.sw-classic / .sw-oled）。
   12 套主题之后这份色样表搬到了 theme.css 的 [data-swatch='<id>'] ——
   与主题块放在同一个文件里，check-contracts.mjs 才能逐项比对
   「卡片上看到的样子」与「点下去得到的样子」是否一致。 */
/* 等宽片段用全站那一套等宽字族，而不是就地写死一串：
   写死的 ui-monospace/monospace 没有中文回退，正文里「[已隐藏]」这种带中文的
   片段会落到浏览器给 monospace 配的中文字体（Windows 上又是宋体），
   于是又出现一种和左侧菜单不一致的中文字。变量里已经补好了中文回退。 */
.mono { font-family: var(--font-family-mono); }
.muted { color: var(--color-text-secondary); }
.dim { color: var(--color-text-secondary); font-size: var(--font-size-sm); }
.db-line { margin-top: 12px; display: flex; align-items: center; gap: 8px; }
.auth-line { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.note-panel .note { font-size: var(--font-size-md); line-height: 1.9; color: var(--color-text-secondary); }
.note { font-size: var(--font-size-md); line-height: 1.9; color: var(--color-text-secondary); }
.backup-note {
  margin-top: 8px; padding: 10px 12px; border-radius: 8px;
  background: var(--color-bg); font-size: var(--font-size-sm); line-height: 1.8;
  color: var(--color-text-secondary);
}

/* ---- 版本更新设置 ---- */
/* 表单限制最大宽度：这几个输入框（代理地址、token）都很长，
   在宽屏上让它们横跨整个面板既难读，也让人以为要填很多东西 */
.upd-form { max-width: 560px; }
.upd-form :deep(.ant-form-item) { margin-bottom: 14px; }
.upd-form :deep(.ant-form-item-label) { padding-bottom: 2px; }
/* 提示文字要单独占一行。它跟在控件后面，而 .dim 是 12px 的行内元素 ——
   不换行的话会紧贴在输入框右侧，与它的说明对象脱开 */
.upd-hint { display: block; margin-top: 4px; line-height: 1.7; }
.upd-actions { display: flex; align-items: center; gap: 10px; margin-top: 4px; }

/* ---- 客户端显示映射 ----
   行编辑器（与渠道白名单编辑器同构）：CSS grid 定列，列宽随内容类别固定 ——
   客户端标识最宽（token 可能长），其余输入控件统一档位。窄屏放不下时
   整体横向滚动（与日志表同一策略），控件列不折行。 */
.alias-badge {
  margin-left: 10px; padding: 1px 8px; border-radius: 10px;
  font-size: var(--font-size-sm); font-weight: 500;
  background: color-mix(in oklab, var(--color-orange) 15%, transparent);
  color: var(--text-amber);
  vertical-align: middle;
}
.alias-wrap { margin-top: 8px; overflow-x: auto; }
.alias-row {
  display: grid;
  grid-template-columns: minmax(180px, 1.4fr) 150px 140px 190px auto;
  gap: 8px; align-items: center; padding: 6px 0;
  border-bottom: 1px solid var(--color-border, rgba(128, 128, 128, 0.18));
  min-width: 760px;
}
.alias-head {
  font-size: var(--font-size-sm); color: var(--color-text-secondary);
  border-bottom-width: 1px; padding-top: 0;
}
.alias-ops { display: inline-flex; gap: 6px; }
.alias-ops-head { width: 108px; }
.alias-key { display: flex; align-items: center; gap: 6px; min-width: 0; }
.alias-key-text {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--font-size-sm);
}
/* 状态徽标：待设置是真正要人来的（内置表也没有它），内置是「显示本来
   就好、配不配随意」—— 颜色上也区分主次，避免 40 多个内置行看起来
   全都欠配置。配色沿用 StatCard tone-orange 的配方（15% 橙底 + amber
   墨色，对比度落在主题已实测过的档位上）。 */
.alias-pill {
  flex: 0 0 auto; padding: 0 6px; border-radius: 8px;
  font-size: var(--font-size-sm); line-height: 18px;
}
.alias-pill.is-pending {
  background: color-mix(in oklab, var(--color-orange) 15%, transparent);
  color: var(--text-amber);
}
.alias-pill.is-builtin { background: var(--color-bg); color: var(--color-text-secondary); }
</style>
