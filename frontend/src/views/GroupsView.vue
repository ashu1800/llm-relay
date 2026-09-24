<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, ReloadOutlined, DeleteOutlined, EditOutlined } from '@ant-design/icons-vue'
import { api } from '@/api/client'
import { useFormValidate } from '@/composables/useFormValidate'
import DataState from '@/components/DataState.vue'
import GroupTag from '@/components/GroupTag.vue'
import { groupStyle } from '@/utils/groupStyle'
import { moneyText } from '@/utils/money'
import { STRATEGIES, type ChannelGroup } from '@/api/types'

// 表单校验（2026-09-24 UI 审评补）：原来名称的 required 只是个视觉星号，
// 失焦不校验、填漏了只弹一句 message
import type { FormInstance, Rule } from 'ant-design-vue/es/form'

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
  tpm: 0,
  // 日预算按币种各一格（项目目前就 CNY/USD 两种记账币种，见渠道表单的
  // CURRENCY_OPTIONS）。null = 该币种不限；两个都空 = 不设预算
  budgetCny: null as number | null,
  budgetUsd: null as number | null
})

// ---- 表单校验（2026-09-24 UI 审评补）----
//
// 名称是这一页唯一的必填项，但 required 以前只是个视觉星号：
// a-form 没绑 :model、字段没绑 name、表单没绑 :rules。补上之后
// 名称失焦即校验、错误显示在字段下方，提交前再兜一次底。
const formRef = ref<FormInstance>()

const formRules: Record<string, Rule[]> = {
  name: [
    { required: true, message: '给分组起个名字：渠道与密钥都按它划分路由与额度', trigger: 'blur' },
    { max: 64, message: '名字最长 64 个字符', trigger: 'blur' }
  ]
}

// 校验失败的统一收尾（滚动 + 聚焦）在 composables/useFormValidate.ts，
// 四个视图共用同一份
const { validateForm } = useFormValidate(formRef)

// 预算表单 -> 提交值：把两个输入框收敛成按币种的 map，空的不带。
// 两个都空时提交空对象（= 清除预算），而不是省略字段 —— 省略在编辑接口
// 里是「保持原值」，用户删掉两个数字再保存，期望的显然是删掉预算
function budgetPayload(): Record<string, number> {
  const out: Record<string, number> = {}
  if (form.budgetCny != null && form.budgetCny > 0) out.CNY = form.budgetCny
  if (form.budgetUsd != null && form.budgetUsd > 0) out.USD = form.budgetUsd
  return out
}

/** 预算列的金额显示：走 utils/money 的符号表与小数位规则。
 *  这里原来是 local 的 `sym + v.toFixed(2)` —— 同一笔钱在日志里六位、
 *  在这里两位，用户拿预算数字去对日志明细时对不上（2026-09-24 UI 审评）。
 *  预算额度量级在「元」，四位小数对它是冗余的，但**一致性比省字符重要**：
 *  分档规则由 money.ts 统一决定，这里不再自己拍。 */
function budgetMoney(v: number, cur: string) {
  return moneyText(v, cur)
}

/** 预算列的一格：配了预算才有内容 —— 花费/限额 + 占比，80% 变黄、100% 变红 */
function budgetCell(row: ChannelGroup) {
  const limit = row.daily_budget
  const spent = row.today_spent
  if (!limit || !Object.keys(limit).length) return null
  return Object.entries(limit).map(([cur, cap]) => {
    const sp = spent?.[cur] ?? 0
    const ratio = cap > 0 ? sp / cap : 0
    const tone = ratio >= 1 ? 'is-over' : ratio >= 0.8 ? 'is-warn' : ''
    const pct = Math.round(ratio * 100)
    return { cur, cap, sp, ratio, tone, pct }
  })
}

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
    // 不清空列表：DataState 修复后（alert 与 slot 同渲染），已有数据时
    // 刷新失败会在表格上方常驻错误提示 + 重试按钮，旧数据与新错误
    // 不会混淆。清空反而把用户正在看的内容抹掉，与其它页面行为不一致。
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
    tpm: 0,
    budgetCny: null,
    budgetUsd: null
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
    tpm: row.tpm || 0,
    budgetCny: row.daily_budget?.CNY ?? null,
    budgetUsd: row.daily_budget?.USD ?? null
  })
  modalOpen.value = true
}

async function save() {
  // 原实现失焦与提交都不校验，只在提交时弹一句 message；
  // 现在交给 rules：名称失焦即校验，这里再兜一次底
  if (!(await validateForm())) return
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
      tpm: Number(form.tpm) || 0,
      // 恒带上：空对象表示清除预算。省略字段在编辑接口里是「保持原值」，
      // 用户清空两个输入框保存的期望是删掉预算，两者必须分开
      daily_budget: budgetPayload()
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
    centered: true,
    title: '确认删除分组',
    content: '删除后不可恢复。若分组下仍有渠道、或仍被密钥的分组白名单引用，后端会拒绝删除并说明原因。',
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
      <!-- scroll.x 必须不小于各列宽度之和（名称 200 + 备注 220 + 路由策略 120
           + 每分钟额度 170 + 日预算 170 + 是否默认 100 + 是否启用 100 + 操作 84 = 1164）：
           声明偏小时右侧固定的「操作」列会盖住左边最后一列，
           表现为表头被截断、单元格内容被压住，而且不报错。
           原来写的是 1040、少了 20（那是「编辑 / 删除」还是文字链接的时候）。
           操作 150 -> 84 是图标按钮那一次，见下方操作列上方的注释。
           +170 日预算是「分组日预算」这一次，见上方该列的注释。
           核对脚本：scripts/check-table-widths.mjs -->
      <a-table
        :data-source="rows"
        :loading="loading"
        :pagination="false"
        row-key="id"
        size="small"
        :scroll="{ x: 1164 }"
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
        <!-- 日预算：花费/限额 + 占比。80% 变黄、100% 变红 ——
             颜色只在真的逼近/越过时才出现，常态是安静的正文色 -->
        <a-table-column title="日预算" :width="170">
          <template #default="{ record }">
            <template v-if="budgetCell(record)">
              <div v-for="cell in budgetCell(record)" :key="cell.cur" class="budget-cell" :class="cell.tone">
                {{ budgetMoney(cell.sp, cell.cur) }} / {{ budgetMoney(cell.cap, cell.cur) }}
                <span class="budget-pct">{{ cell.pct }}%</span>
              </div>
            </template>
            <span v-else class="muted">—</span>
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
        <!-- 两个动作改成图标按钮，与渠道页同一个写法（.table-icon-btn）。
             编辑与删除是通用约定，不额外配文字；tooltip 与 aria-label
             仍然各给一份 —— 图标按钮没有可见文字，读屏只能靠它。 -->
        <a-table-column title="操作" :width="84" fixed="right">
          <template #default="{ record }">
            <a-space :size="4">
              <a-tooltip title="编辑分组">
                <a-button
                  class="table-icon-btn"
                  type="text"
                  size="small"
                  :aria-label="'编辑分组 ' + record.name"
                  @click="openEdit(record)"
                >
                  <EditOutlined />
                </a-button>
              </a-tooltip>
              <a-tooltip title="删除分组">
                <a-button
                  class="table-icon-btn is-danger"
                  type="text"
                  size="small"
                  :aria-label="'删除分组 ' + record.name"
                  @click="confirmDelete(record)"
                >
                  <DeleteOutlined />
                </a-button>
              </a-tooltip>
            </a-space>
          </template>
        </a-table-column>
        <template #emptyText>
          <a-empty description="还没有分组，点「新建分组」创建第一个" />
        </template>
      </a-table>
      </DataState>
    </section>

    <a-modal v-model:open="modalOpen" :title="title" :confirm-loading="saving" :width="'min(560px, 94vw)'" centered @ok="save">
      <a-form ref="formRef" :model="form" :rules="formRules" layout="vertical">
        <a-form-item label="分组名称" name="name">
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
        <a-form-item label="日预算（按币种）">
          <a-row :gutter="8">
            <a-col :span="12">
              <a-input-number v-model:value="form.budgetCny" :min="0" :step="10" style="width: 100%" addon-before="¥ CNY" placeholder="不限" />
            </a-col>
            <a-col :span="12">
              <a-input-number v-model:value="form.budgetUsd" :min="0" :step="10" style="width: 100%" addon-before="$ USD" placeholder="不限" />
            </a-col>
          </a-row>
          <div class="field-hint">
            该分组每日花费上限，按币种分别设额、互不折算。今天用到 80% / 100%
            时会各提醒一次（每天最多提醒一次），只提醒不拦截请求。两个都留空 = 不设预算。
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

/* 日预算一格：花费/限额 占比。常态走正文色 —— 它是每天要扫的内容，
   不是告警；只有逼近（80%）和越过（100%）时才借 warn/over 的颜色喊话 */
.budget-cell {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.budget-pct { color: var(--color-text-secondary); }
.budget-cell.is-warn { color: var(--text-amber); font-weight: 500; }
.budget-cell.is-over { color: var(--color-red); font-weight: 600; }
.budget-cell.is-over .budget-pct { color: var(--color-red); }
.field-hint { margin-top: 4px; font-size: 12px; color: var(--color-text-secondary); }
.color-row { display: flex; align-items: center; gap: 8px; }
.color-input {
  width: 28px;
  height: 24px;
  padding: 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: none;
  cursor: var(--cursor-hand);
}
.swatches { display: inline-flex; gap: 4px; vertical-align: middle; margin-left: 2px; }
.swatch {
  width: 16px;
  height: 16px;
  padding: 0;
  border: 1px solid var(--color-border);
  border-radius: 3px;
  cursor: var(--cursor-hand);
}
.swatch:hover { transform: scale(1.15); }
/* 触屏把命中区撑到 44px：用透明伪元素外扩（视觉尺寸完全不变）——
   直接加大 min-width 会让色块本身变大。28/16px 的原生命中区低于
   WCAG 2.5.8 的 24×24，更够不着触摸标准；其它图标按钮在
   MainLayout 的 coarse 规则里已有同款处理 */
@media (pointer: coarse) {
  .color-input,
  .swatch {
    position: relative;
  }
  .color-input::after {
    content: '';
    position: absolute;
    inset: -10px;
  }
  .swatch::after {
    content: '';
    position: absolute;
    inset: -14px;
  }
}

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
/* 压暗比例必须与 GroupTag 的浅色 50% 一致：原来这里原色直出，
   自定义色在浅色主题下「预览即最终效果」不成立（暗色分支对齐过，
   浅色漏了 —— 两边都改过一次，值各抄一份迟早再漂） */
.group-tag.is-custom { --gt-base: color-mix(in oklab, var(--gt-color) 50%, black); }
:root[data-theme='dark'] .group-tag { --gt-l: 0.80; }
/* 混白比例必须与 GroupTag 的 45% 一致（那边实测过十三个预设的最差对比度）：
   这里曾经是 62%，同一自定义色在表单预览与列表里颜色不一样，
   「预览就是最终效果」不成立 */
:root[data-theme='dark'] .group-tag.is-custom { --gt-base: color-mix(in oklab, var(--gt-color) 45%, white); }
</style>

