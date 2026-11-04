<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { SearchOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import EmptyState from '@/components/EmptyState.vue'
import { modalWidth } from '@/utils/modalWidth'
import {
  catalogModeLabel,
  trimCatalogNumber,
  type CatalogHit
} from '@/components/modelPricing'

// 参考价库搜索弹窗（2026-11 P1）。
//
// 这是「建议价源」的入口：搜到候选 → 选中回填 → 由父组件把美元档写进
// 编辑表单（用户还要点一次保存才落库）。弹窗本身只读目录，不做任何写操作。
//
// 搜索是远程的（目录有 4500+ 条，全量拉到前端没有意义）；打开时用
// 父组件给的初始词自动搜一次（通常是正在编辑的模型名）。
const props = defineProps<{
  open: boolean
  initialQuery?: string
}>()

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'select', hit: CatalogHit): void
}>()

const q = ref('')
const mode = ref('')
const items = ref<CatalogHit[]>([])
const loading = ref(false)
const error = ref('')
const searched = ref(false)

// 目录新鲜度随搜索响应一起回来（后端 catalogSearch 附带），顺手展示
const builtin = ref(true)
const fetchedAt = ref('')

const MODE_OPTIONS = [
  { value: '', label: '全部类型' },
  { value: 'chat', label: '对话' },
  { value: 'image_generation', label: '文生图' },
  { value: 'audio_transcription', label: '语音转写' },
  { value: 'audio_speech', label: '语音合成' },
  { value: 'embedding', label: '向量化' },
  { value: 'rerank', label: '重排序' }
]

async function search() {
  loading.value = true
  error.value = ''
  try {
    const res = await api.get<{
      items: CatalogHit[] | null
      builtin?: boolean
      fetched_at?: string
    }>(
      `/model-prices/catalog/search?q=${encodeURIComponent(q.value.trim())}` +
        `&mode=${encodeURIComponent(mode.value)}&limit=30`
    )
    items.value = res.items || []
    builtin.value = res.builtin !== false
    fetchedAt.value = res.fetched_at || ''
    searched.value = true
  } catch (e: any) {
    error.value = e?.message || '搜索失败'
  } finally {
    loading.value = false
  }
}

watch(
  () => props.open,
  (v) => {
    if (v) {
      q.value = props.initialQuery || ''
      mode.value = ''
      items.value = []
      searched.value = false
      error.value = ''
      if (q.value) void search()
    }
  }
)

function pick(hit: CatalogHit) {
  emit('select', hit)
  emit('update:open', false)
}

const sourceLabel = computed(() =>
  builtin.value ? '内置快照' : `更新于 ${fetchedAt.value.replace('T', ' ').slice(0, 16)}`
)

function priceLine(entry: CatalogHit['entry']): string {
  const parts: string[] = []
  if (entry.in) parts.push(`入 $${trimCatalogNumber(entry.in)}`)
  if (entry.out) parts.push(`出 $${trimCatalogNumber(entry.out)}`)
  if (entry.cr) parts.push(`缓存 $${trimCatalogNumber(entry.cr)}`)
  if (entry.img) parts.push(`图 $${trimCatalogNumber(entry.img)}/张`)
  if (entry.sec) parts.push(`$${trimCatalogNumber(entry.sec)}/秒`)
  if (entry.chr) parts.push(`$${trimCatalogNumber(entry.chr)}/字符`)
  return parts.join(' · ') || '（无价格信息）'
}
</script>

<template>
  <a-modal
    :open="props.open"
    title="从参考价库填充"
    :width="modalWidth(760)"
    :footer="null"
    destroy-on-close
    @update:open="(v: boolean) => emit('update:open', v)"
  >
    <p class="cat-hint">
      参考价库来自 LiteLLM 上游维护的官方价格表（{{ sourceLabel }}）。
      选中后只回填美元档到编辑表单，人民币档与已填过的档不动，保存前可随意修改。
    </p>

    <div class="cat-bar">
      <a-input
        v-model:value="q"
        placeholder="搜索模型名，例如 gpt-4o、claude-sonnet、whisper"
        allow-clear
        class="cat-input mono"
        @press-enter="search"
      >
        <template #prefix><SearchOutlined /></template>
      </a-input>
      <a-select v-model:value="mode" :options="MODE_OPTIONS" class="cat-mode" />
      <a-button type="primary" :loading="loading" @click="search">搜索</a-button>
    </div>

    <DataState
      :loading="loading"
      :error="error"
      :empty="searched && !items.length"
      empty-text="目录里没有匹配的模型"
      @retry="search"
    >
      <div class="cat-list">
        <div v-for="h in items" :key="h.key" class="cat-row">
          <div class="cat-main">
            <div class="cat-key mono">{{ h.key }}</div>
            <div class="cat-meta">
              <a-tag class="cat-tag">{{ catalogModeLabel(h.entry.mode) }}</a-tag>
              <span class="cat-provider mono">{{ h.entry.provider }}</span>
              <span class="cat-price mono">{{ priceLine(h.entry) }}</span>
            </div>
          </div>
          <a-button size="small" @click="pick(h)">回填</a-button>
        </div>
      </div>
      <template #emptyText>
        <EmptyState text="目录里没有匹配的模型" size="sm" />
      </template>
    </DataState>
  </a-modal>
</template>

<style scoped>
.cat-hint {
  margin: 0 0 12px;
  font-size: var(--font-size-sm);
  line-height: 1.7;
  color: var(--color-text-secondary);
}

.cat-bar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}

.cat-input {
  flex: 1;
  min-width: 0;
}

.cat-mode {
  width: 130px;
  flex: none;
}

.cat-list {
  max-height: 380px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.cat-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-bg) 60%, var(--color-fg));
}

.cat-main {
  flex: 1;
  min-width: 0;
}

.cat-key {
  font-size: var(--font-size-sm);
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cat-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 2px;
  font-size: var(--font-size-xs);
  color: var(--color-text-secondary);
  flex-wrap: wrap;
}

.cat-tag {
  margin: 0;
}

.cat-price {
  color: var(--color-text-secondary);
}

.mono {
  font-family: var(--font-family-mono);
}
</style>
