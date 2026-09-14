<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, DeleteOutlined, EditOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import DataState from '@/components/DataState.vue'
import GroupTag from '@/components/GroupTag.vue'
import { groupStyle } from '@/utils/groupStyle'
import { STRATEGIES, type ChannelGroup } from '@/api/types'

// 常用色板：分组颜色是给「一眼分辨哪个分组」用的，
// 给几个对比度够、色相拉得开的预设，比让人从取色器里随便挑更实用。
const COLOR_PRESETS = [
  '#1677ff', '#13c2c2', '#52c41a', '#faad14',
  '#fa541c', '#eb2f96', '#722ed1', '#8c8c8c'
]

// 路由策略选项统一取自 api/types（原来这里自己抄了一份，两处 label 已经不一致）。
// 后端也会把不认识的策略值收敛到 failover，所以过渡期的老数据不会显示出空白。
const STRATEGY_OPTIONS = STRATEGIES

const loading = ref(false)
const rows = ref<ChannelGroup[]>([])
const loadError = ref('')

const modalOpen = ref(false)
const editing = ref<ChannelGroup | null>(null)
const saving = ref(false)

const form = reactive({
  name: '',
  remark: '',
  strategy: 'failover',
  is_default: false,
  enabled: true,
  color: '',
  rpm: 0,
  tpm: 0
})

// 预览用的样式：表单里改颜色时，胶囊要立刻跟着变
const previewStyle = computed(() => groupStyle(form.name || '分组名', form.color))
const previewVars = computed(() => {
  const s = previewStyle.value
  return s.auto
    ? { '--gt-h': String(Math.round(s.hue)), '--gt-c': String(s.chroma) }
    : { '--gt-color': s.color }
})

/** 原生取色器回调：value 一定是 #rrggbb，直接写回表单 */
function onPickColor(e: Event) {
  form.color = (e.target as HTMLInputElement).value
}

/** 每分钟额度在列表里的写法：两个都为 0（默认）时明确写「不限制」 */
function quotaText(row: ChannelGroup) {
  const parts: string[] = []
  if (row.rpm > 0) parts.push('RPM ' + row.rpm)
  if (row.tpm > 0) parts.push('TPM ' + row.tpm)
  return parts.length ? parts.join(' / ') : '不限制'
}

const title = computed(() => (editing.value ? '编辑分组' : '新建分组'))

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const res = await api.get<{ items: ChannelGroup[] }>('/groups')
    rows.value = res.items || []
  } catch (e: any) {
    // 失败时仍然清空列表：旧数据配上错误提示容易被当成「当前真实的分组」，
    // 清空后由 DataState 统一呈现「加载失败 + 重试」，不会退化成「暂无数据」
    rows.value = []
    loadError.value = e.message || '加载失败'
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

// 后端返回的策略标识转中文标签，遇到未知取值时原样显示
function strategyLabel(v: string) {
  const found = STRATEGY_OPTIONS.find((s) => s.value === v)
  return found ? found.label : v
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    remark: '',
    strategy: 'failover',
    is_default: false,
    enabled: true,
    color: '',
    rpm: 0,
    tpm: 0
  })
  modalOpen.value = true
}

function openEdit(row: ChannelGroup) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    remark: row.remark || '',
    strategy: row.strategy || 'failover',
    is_default: !!row.is_default,
    enabled: !!row.enabled,
    color: row.color || '',
    rpm: row.rpm || 0,
    tpm: row.tpm || 0
  })
  modalOpen.value = true
}

async function save() {
  if (!form.name.trim()) {
    message.warning('分组名称必填')
    return
  }
  saving.value = true
  try {
    const body = {
      name: form.name.trim(),
      remark: form.remark.trim(),
      strategy: form.strategy,
      is_default: form.is_default,
      enabled: form.enabled,
      // 空串是有效值（恢复自动配色），必须原样传，不能省成 undefined
      color: form.color || '',
      rpm: Number(form.rpm) || 0,
      tpm: Number(form.tpm) || 0
    }
    if (editing.value) {
      await api.put('/groups/' + editing.value.id, body)
      message.success('更新成功')
    } else {
      await api.post('/groups', body)
      message.success('创建成功')
    }
    modalOpen.value = false
    await load()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

function confirmDelete(row: ChannelGroup) {
  Modal.confirm({
    title: '确认删除分组',
    content: '删除后不可恢复。若分组下仍有渠道或模板，后端会拒绝删除并说明原因。',
    okType: 'danger',
    async onOk() {
      try {
        await api.del('/groups/' + row.id)
        message.success('已删除')
        await load()
      } catch (e: any) {
        // 409 表示分组仍被渠道或模板占用：后端给的中文说明原样展示，不要改写
        message.error(e.message, e.status === 409 ? 6 : 3)
      }
    }
  })
}

onMounted(load)
</script>

<template>
  <div class="manage-container">
    <section class="panel manage-panel">
      <div class="manage-toolbar">
        <div class="toolbar-left">
          <a-button type="primary" @click="openCreate"><PlusOutlined /> 新建分组</a-button>
          <a-button :loading="loading" @click="load"><ReloadOutlined /> 刷新</a-button>
        </div>
        <div class="toolbar-spacer" />
        <span class="toolbar-hint">共 {{ rows.length }} 个分组</span>
      </div>

      <DataState
        :error="loadError"
        :has-data="rows.length > 0"
        :loading="loading"
        title="分组列表加载失败"
        @retry="load"
      >
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="false"
        row-key="id"
        size="small"
        :scroll="{ x: 1040 }"
      >
        <a-table-column title="名称" :width="200">
          <template #default="{ record }">
            <GroupTag :name="record.name" :color="record.color" />
          </template>
        </a-table-column>
        <a-table-column title="备注" :width="220" ellipsis>
          <template #default="{ record }">
            <span v-if="record.remark">{{ record.remark }}</span>
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <a-table-column title="路由策略" :width="120">
          <template #default="{ record }">
            <a-tag>{{ strategyLabel(record.strategy) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="每分钟额度" :width="170">
          <template #default="{ record }">
            <span :class="{ muted: !record.rpm && !record.tpm }">{{ quotaText(record) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="是否默认" :width="100">
          <template #default="{ record }">
            <a-tag v-if="record.is_default" color="blue">默认</a-tag>
            <span v-else class="muted">否</span>
          </template>
        </a-table-column>
        <a-table-column title="是否启用" :width="100">
          <template #default="{ record }">
            <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="150" fixed="right">
          <template #default="{ record }">
            <a-space>
              <a @click="openEdit(record)"><EditOutlined /> 编辑</a>
              <a class="danger-link" @click="confirmDelete(record)"><DeleteOutlined /> 删除</a>
            </a-space>
          </template>
        </a-table-column>
        <template #emptyText>
          <a-empty description="还没有分组，点「新建分组」创建第一个" />
        </template>
      </a-table>
      </DataState>
    </section>

    <a-modal v-model:open="modalOpen" :title="title" :confirm-loading="saving" width="560px" @ok="save">
      <a-form layout="vertical">
        <a-form-item label="分组名称" required>
          <a-input v-model:value="form.name" placeholder="例如 高优先级" />
        </a-form-item>
        <a-form-item label="备注">
          <a-input v-model:value="form.remark" placeholder="选填，说明这个分组的用途" />
        </a-form-item>
        <a-form-item label="路由策略">
          <a-select v-model:value="form.strategy" :options="STRATEGY_OPTIONS" />
        </a-form-item>
        <a-form-item label="胶囊颜色">
          <div class="color-row">
            <!-- 预览就是最终效果：列表里、日志里用的都是这个胶囊 -->
            <span class="group-tag" :class="{ 'is-custom': !previewStyle.auto }" :style="previewVars">
              {{ previewStyle.label }}
            </span>
            <!-- 用原生取色器而不是 a-color-picker：当前依赖的
                 ant-design-vue 4.2.6 里根本没有 ColorPicker 组件
                 （es/ 下没有 color-picker，components.d.ts 里也没有导出）。
                 写 <a-color-picker> 不会报错，只会被当成未知标签原样渲染出来 ——
                 界面上什么都不显示，看起来像「样式没跟上」，实测确认过。
                 原生 input[type=color] 是浏览器自带的取色器，零依赖且够用。 -->
            <input
              class="color-input"
              type="color"
              :value="form.color || '#1677ff'"
              title="选择颜色"
              @input="onPickColor"
            />
            <a-button size="small" :disabled="!form.color" @click="form.color = ''">自动配色</a-button>
          </div>
          <div class="field-hint">
            留空则按分组名自动配色（同一个名字永远同一种颜色）。色板：
            <span class="swatches">
              <button
                v-for="c in COLOR_PRESETS"
                :key="c"
                class="swatch"
                :style="{ background: c }"
                :title="c"
                @click="form.color = c"
              />
            </span>
          </div>
        </a-form-item>
        <a-form-item label="每分钟额度">
          <a-row :gutter="8">
            <a-col :span="12">
              <a-input-number v-model:value="form.rpm" :min="0" style="width: 100%" addon-before="RPM" />
            </a-col>
            <a-col :span="12">
              <a-input-number v-model:value="form.tpm" :min="0" style="width: 100%" addon-before="TPM" />
            </a-col>
          </a-row>
          <div class="field-hint">
            0 表示不限制（默认）。RPM 统计发往上游的请求数（含重试），
            TPM 统计上游回报的实际 token 数 —— 因此 TPM 会在越过额度后的下一个请求才拦住。
          </div>
        </a-form-item>
        <a-row :gutter="8">
          <a-col :span="12">
            <a-form-item label="设为默认分组">
              <a-switch v-model:checked="form.is_default" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="启用">
              <a-switch v-model:checked="form.enabled" />
            </a-form-item>
          </a-col>
        </a-row>
      </a-form>
    </a-modal>
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
}
.toolbar-left { display: flex; gap: var(--gap); }
.toolbar-spacer { flex: 1; }
.toolbar-hint { color: var(--color-text-secondary); font-size: 13px; }
.muted { color: var(--color-text-secondary); }
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.color-row { display: flex; align-items: center; gap: 8px; }
.color-input {
  width: 28px;
  height: 24px;
  padding: 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: none;
  cursor: pointer;
}
.swatches { display: inline-flex; gap: 4px; vertical-align: middle; margin-left: 2px; }
.swatch {
  width: 16px;
  height: 16px;
  padding: 0;
  border: 1px solid var(--color-border);
  border-radius: 3px;
  cursor: pointer;
}
.swatch:hover { transform: scale(1.15); }

/* 预览胶囊：与 components/GroupTag.vue 同一套变量与算法。
   这里不能直接用 GroupTag 组件 —— 它读的是「已保存的分组」，
   而表单要预览的是「还没保存的颜色」，所以只共用变量约定。 */
.group-tag {
  --gt-l: 0.47;
  --gt-base: oklch(var(--gt-l) var(--gt-c) var(--gt-h));
  display: inline-block;
  max-width: 200px;
  padding: 1px 8px;
  border-radius: var(--radius-control);
  border: 1px solid color-mix(in oklab, var(--gt-base) 32%, transparent);
  background: color-mix(in oklab, var(--gt-base) 13%, transparent);
  color: var(--gt-base);
  font-size: 12px;
  font-weight: 500;
  line-height: 20px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.group-tag.is-custom { --gt-base: var(--gt-color); }
:root[data-theme='dark'] .group-tag { --gt-l: 0.80; }
:root[data-theme='dark'] .group-tag.is-custom { --gt-base: color-mix(in oklab, var(--gt-color) 62%, white); }
.danger-link { color: var(--color-red); }
</style>
