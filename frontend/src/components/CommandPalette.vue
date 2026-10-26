<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  SearchOutlined,
  DashboardOutlined,
  KeyOutlined,
  ApiOutlined,
  ClusterOutlined,
  GlobalOutlined,
  SettingOutlined,
  BgColorsOutlined,
  ThunderboltOutlined,
  EnterOutlined,
  ArrowUpOutlined,
  ArrowDownOutlined
} from '@ant-design/icons-vue'
import { modalWidth } from '@/utils/modalWidth'
import { THEMES } from '@/utils/themes'
import { useThemeStore } from '@/stores/theme'
import { api } from '@/api/client'
import type { Channel } from '@/api/types'
import {
  CATEGORY_LABELS,
  filterCommands,
  type CommandCategory,
  type CommandItem
} from '@/components/commandPalette'

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'update:open', val: boolean): void
}>()

const router = useRouter()
const themeStore = useThemeStore()

const query = ref('')
const activeIndex = ref(0)
const inputRef = ref<{ focus?: () => void } | null>(null)
const listRef = ref<HTMLElement | null>(null)

// 缓存从渠道列表拉取的白名单模型
const cachedModels = ref<string[]>([])
let modelsLoadedAt = 0

async function loadModelsIfNeeded() {
  if (Date.now() - modelsLoadedAt < 60000 && cachedModels.value.length > 0) return
  try {
    const res = await api.get<Channel[]>('/channels')
    const set = new Set<string>()
    for (const ch of res || []) {
      for (const m of ((ch as any).models || [])) {
        if (typeof m === 'string') set.add(m)
        else if (m && typeof m === 'object' && m.public_name) set.add(m.public_name)
      }
    }
    cachedModels.value = Array.from(set).sort()
    modelsLoadedAt = Date.now()
  } catch {
    // 忽略加载错误
  }
}

// 静态命令集合：页面导航 + 快捷操作 + 主题切换
const baseCommands = computed<CommandItem[]>(() => {
  const items: CommandItem[] = [
    // 页面导航
    {
      id: 'nav-dashboard',
      category: 'nav',
      title: '数据看板',
      subtitle: '系统监控概览与实时请求日志',
      keywords: ['dashboard', 'kanban', 'shuju', 'index'],
      action: () => router.push('/console/dashboard')
    },
    {
      id: 'nav-keys',
      category: 'nav',
      title: '密钥信息',
      subtitle: 'API Key 与分组/模型调用权限配置',
      keywords: ['keys', 'token', 'miyao', 'auth'],
      action: () => router.push('/console/keys')
    },
    {
      id: 'nav-channels',
      category: 'nav',
      title: '渠道管理',
      subtitle: '上游端点、协议、白名单与健康检测',
      keywords: ['channels', 'qudao', 'upstream', 'provider'],
      action: () => router.push('/console/channels')
    },
    {
      id: 'nav-groups',
      category: 'nav',
      title: '分组管理',
      subtitle: '多渠道调度策略、配额与预算',
      keywords: ['groups', 'fenzu', 'strategy', 'quota'],
      action: () => router.push('/console/groups')
    },
    {
      id: 'nav-proxies',
      category: 'nav',
      title: '代理管理',
      subtitle: '出站 SOCKS5 / HTTP 代理与连通性',
      keywords: ['proxies', 'daili', 'network'],
      action: () => router.push('/console/proxies')
    },
    {
      id: 'nav-settings',
      category: 'nav',
      title: '系统设置',
      subtitle: '报文留存策略、版本更新与配置备份',
      keywords: ['settings', 'shezhi', 'backup', 'update', 'config'],
      action: () => router.push('/console/system')
    },

    // 快捷排障
    {
      id: 'action-error-logs',
      category: 'action',
      title: '排障：只看失败请求日志',
      subtitle: '跳转至看板并应用 ?status_class=error 深链',
      keywords: ['error', 'fail', 'shibai', 'debug', 'status_class'],
      action: () => router.push('/console/dashboard?status_class=error')
    },
    {
      id: 'action-refresh',
      category: 'action',
      title: '刷新当前页面数据',
      subtitle: '强制重载前台数据',
      keywords: ['refresh', 'shuaxin', 'reload'],
      action: () => window.location.reload()
    }
  ]

  // 12 套主题切换命令
  for (const t of THEMES) {
    items.push({
      id: 'theme-' + t.id,
      category: 'theme',
      title: `切换为 ${t.name} 主题`,
      subtitle: `${t.tone === 'dark' ? '深色' : '浅色'} · ${t.desc}`,
      keywords: ['theme', 'zhuti', t.id, t.name, t.upstream],
      action: () =>
        themeStore.setTheme(t.id, {
          x: window.innerWidth / 2,
          y: window.innerHeight / 2
        })
    })
  }

  // 动态白名单模型直达命令
  for (const m of cachedModels.value) {
    items.push({
      id: 'model-' + m,
      category: 'model',
      title: `模型：${m}`,
      subtitle: '跳转至渠道管理并定位支持该模型的渠道',
      keywords: ['model', 'moxing', m],
      action: () => router.push('/console/channels?q=' + encodeURIComponent(m))
    })
  }

  return items
})

const filteredItems = computed(() => filterCommands(query.value, baseCommands.value))

watch(
  () => props.open,
  (val) => {
    if (val) {
      query.value = ''
      activeIndex.value = 0
      loadModelsIfNeeded()
      nextTick(() => {
        inputRef.value?.focus?.()
      })
    }
  }
)

watch(filteredItems, () => {
  activeIndex.value = 0
})

function close() {
  emit('update:open', false)
}

function execute(item: CommandItem) {
  close()
  item.action()
}

function onKeydown(e: KeyboardEvent) {
  if (!props.open || e.isComposing) return
  const len = filteredItems.value.length
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (len > 0) {
      activeIndex.value = (activeIndex.value + 1) % len
      scrollToActive()
    }
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (len > 0) {
      activeIndex.value = (activeIndex.value - 1 + len) % len
      scrollToActive()
    }
  } else if (e.key === 'Enter') {
    e.preventDefault()
    const active = filteredItems.value[activeIndex.value]
    if (active) execute(active)
  }
}

function scrollToActive() {
  nextTick(() => {
    const el = listRef.value?.querySelector(`.palette-item.is-active`) as HTMLElement | null
    el?.scrollIntoView({ block: 'nearest' })
  })
}
</script>

<template>
  <a-modal
    :open="open"
    :width="modalWidth(560)"
    :footer="null"
    :closable="false"
    centered
    destroy-on-close
    class="palette-modal"
    @cancel="close"
  >
    <div class="palette-container" @keydown="onKeydown">
      <div class="palette-search-box">
        <SearchOutlined class="palette-search-icon" aria-hidden="true" />
        <a-input
          ref="inputRef"
          v-model:value="query"
          placeholder="输入页面、模型、主题或操作..."
          :bordered="false"
          class="palette-input"
          allow-clear
          aria-label="命令面板搜索框"
        />
        <kbd class="palette-kbd">ESC</kbd>
      </div>

      <div ref="listRef" class="palette-list" role="listbox">
        <template v-if="filteredItems.length">
          <div
            v-for="(item, idx) in filteredItems"
            :key="item.id"
            class="palette-item"
            :class="{ 'is-active': idx === activeIndex }"
            role="option"
            :aria-selected="idx === activeIndex"
            @click="execute(item)"
            @mouseenter="activeIndex = idx"
          >
            <div class="palette-item-icon">
              <DashboardOutlined v-if="item.id === 'nav-dashboard'" />
              <KeyOutlined v-else-if="item.id === 'nav-keys'" />
              <ApiOutlined v-else-if="item.id === 'nav-channels' || item.category === 'model'" />
              <ClusterOutlined v-else-if="item.id === 'nav-groups'" />
              <GlobalOutlined v-else-if="item.id === 'nav-proxies'" />
              <SettingOutlined v-else-if="item.id === 'nav-settings'" />
              <BgColorsOutlined v-else-if="item.category === 'theme'" />
              <ThunderboltOutlined v-else />
            </div>
            <div class="palette-item-content">
              <div class="palette-item-title">{{ item.title }}</div>
              <div v-if="item.subtitle" class="palette-item-sub">{{ item.subtitle }}</div>
            </div>
            <div class="palette-item-tag">
              {{ CATEGORY_LABELS[item.category] }}
            </div>
          </div>
        </template>
        <div v-else class="palette-empty">
          未找到匹配的命令或模型
        </div>
      </div>

      <div class="palette-footer">
        <span class="palette-footer-tip">
          <ArrowUpOutlined class="tip-icon" />
          <ArrowDownOutlined class="tip-icon" /> 切换
        </span>
        <span class="palette-footer-tip">
          <EnterOutlined class="tip-icon" /> 确认
        </span>
        <span class="palette-footer-tip">ESC 退出</span>
      </div>
    </div>
  </a-modal>
</template>

<style scoped>
:deep(.ant-modal-content) {
  padding: 0 !important;
  border-radius: var(--radius-panel) !important;
  overflow: hidden;
  border: 1px solid var(--color-border);
  box-shadow: 0 16px 40px rgba(0, 0, 0, 0.22);
}

.palette-container {
  display: flex;
  flex-direction: column;
}

.palette-search-box {
  display: flex;
  align-items: center;
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border);
  gap: 10px;
}

.palette-search-icon {
  font-size: var(--font-size-base);
  color: var(--color-icon);
}

.palette-input {
  flex: 1;
  font-size: var(--font-size-panel);
  color: var(--color-text);
  padding: 0;
}

.palette-kbd {
  font-size: var(--font-size-2xs);
  padding: 2px 6px;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-text-secondary) 12%, transparent);
  color: var(--color-text-secondary);
  border: 1px solid var(--color-border);
  line-height: 1;
}

.palette-list {
  max-height: 360px;
  overflow-y: auto;
  padding: 6px;
}

.palette-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 10px;
  border-radius: var(--radius-control);
  cursor: pointer;
  transition: background 0.1s ease;
}

.palette-item.is-active {
  background: color-mix(in oklab, var(--color-primary) 12%, transparent);
}

.palette-item-icon {
  font-size: var(--font-size-base);
  color: var(--color-icon);
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
}

.palette-item-content {
  flex: 1;
  min-width: 0;
}

.palette-item-title {
  font-size: var(--font-size-panel);
  font-weight: 500;
  color: var(--color-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.palette-item-sub {
  font-size: var(--font-size-xs);
  color: var(--color-text-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.palette-item-tag {
  font-size: var(--font-size-2xs);
  color: var(--color-text-tertiary);
  padding: 2px 6px;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-text-secondary) 8%, transparent);
}

.palette-empty {
  padding: 36px 0;
  text-align: center;
  font-size: var(--font-size-panel);
  color: var(--color-text-tertiary);
}

.palette-footer {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 8px 16px;
  border-top: 1px solid var(--color-border);
  background: color-mix(in oklab, var(--color-bg) 40%, var(--color-fg));
}

.palette-footer-tip {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: var(--font-size-xs);
  color: var(--color-text-tertiary);
}

.tip-icon {
  font-size: var(--font-size-xs);
}
</style>
