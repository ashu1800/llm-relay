<script lang="ts">
// RateRule 是时段倍率规则：在 days 指定的星期（0=周日）里，
// [start, end) 这段时间按 multiplier 计费。end 早于 start 表示跨午夜。
export interface RateRule {
  days: number[]
  start: string
  end: string
  multiplier: number
  label?: string
}

// PriceConfig 是一个渠道模型的价格。金额用字符串传：
// 单价的精度是 numeric(18,8)，走 JSON 浮点会把 0.15 变成 0.14999999999999999。
// 字段都可选：没配过价的模型（以及价格功能上线前建的渠道）读回来就是没有这些字段，
// 「空」与「0」在界面上必须能区分开，否则用户看不出哪些模型漏配了价
export interface PriceConfig {
  input_per_1m?: string
  output_per_1m?: string
  cache_read_per_1m?: string
  cache_write_per_1m?: string
  multiplier?: number
  peak_rules?: RateRule[]
}

/**
 * 从任意带价格字段的对象里取出价格。
 *
 * 读回来的绑定要经过这一步再进表单：漏掉它的话，保存时提交的 items 里
 * 没有价格字段，而后端把「空」当成 0 —— 表现为「编辑一次渠道，价格全没了」，
 * 界面上还提示保存成功。
 */
export function pickPrice(src: {
  input_per_1m?: string | null
  output_per_1m?: string | null
  cache_read_per_1m?: string | null
  cache_write_per_1m?: string | null
  multiplier?: number | null
  peak_rules?: RateRule[] | null
}): PriceConfig {
  return {
    input_per_1m: src.input_per_1m ?? '',
    output_per_1m: src.output_per_1m ?? '',
    cache_read_per_1m: src.cache_read_per_1m ?? '',
    cache_write_per_1m: src.cache_write_per_1m ?? '',
    multiplier: src.multiplier ?? 1,
    peak_rules: (src.peak_rules || []).map((r) => ({ ...r, days: [...(r.days || [])] }))
  }
}

export function emptyPrice(): PriceConfig {
  return {
    input_per_1m: '',
    output_per_1m: '',
    cache_read_per_1m: '',
    cache_write_per_1m: '',
    multiplier: 1,
    peak_rules: []
  }
}

/** 有没有配过价：四个单价全空且没有时段规则就是没配 */
export function hasPrice(p?: PriceConfig | null): boolean {
  if (!p) return false
  if (p.input_per_1m || p.output_per_1m || p.cache_read_per_1m || p.cache_write_per_1m) return true
  return (p.peak_rules || []).length > 0
}

/**
 * 列表里显示的一行摘要。
 *
 * currency 由调用方（所属渠道）传入：单价的币种是渠道属性，不是价格自己的
 * 属性 —— 同一条价格换个渠道就是另一种钱，所以这里没有默认符号。
 */
export function priceSummary(p?: PriceConfig | null, currency?: string): string {
  if (!hasPrice(p)) return '未定价'
  const q = p as PriceConfig
  const sym = symbolOf(currency)
  let s = sym + (q.input_per_1m || '0') + ' / ' + sym + (q.output_per_1m || '0')
  const rules = q.peak_rules || []
  if (rules.length) {
    s += ' · 时段×' + rules[0].multiplier + (rules.length > 1 ? ' 等' + rules.length + '条' : '')
  } else if (q.multiplier && q.multiplier !== 1) {
    s += ' · ×' + q.multiplier
  }
  return s
}
</script>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { symbolOf } from '@/utils/money'
import { message } from 'ant-design-vue'
import { PlusOutlined, DeleteOutlined } from '@ant-design/icons-vue'

const props = defineProps<{
  open: boolean
  modelName: string
  value: PriceConfig | null
  /** 所属渠道的记账币种：单价的单位按它显示（见 model.Channel.Currency） */
  currency?: string
}>()
const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'save', v: PriceConfig): void
}>()

const form = ref<PriceConfig>(emptyPrice())

watch(
  () => props.open,
  (open) => {
    if (open) form.value = { ...emptyPrice(), ...(props.value || {}), peak_rules: (props.value?.peak_rules || []).map((r) => ({ ...r, days: [...(r.days || [])] })) }
  }
)

const WEEK = ['日', '一', '二', '三', '四', '五', '六']

function toggleDay(rule: RateRule, day: number) {
  const i = rule.days.indexOf(day)
  if (i >= 0) rule.days.splice(i, 1)
  else rule.days.push(day)
}

// 规则数组在表单里始终存在（emptyPrice 给了空数组），但类型上它是可选的，
// 所以读写都过一层 ?? []，避免「模板里 v-for 一个 undefined」
function rules(): RateRule[] {
  if (!form.value.peak_rules) form.value.peak_rules = []
  return form.value.peak_rules
}

function addRule() {
  rules().push({ days: [], start: '09:00', end: '12:00', multiplier: 2, label: '' })
}

function removeRule(index: number) {
  rules().splice(index, 1)
}

// 一键预设：手填四条规则既慢又容易填错，而填错的后果是「永不命中」——
// 界面上看不出任何异常，只会觉得「配了双倍却没生效」
function presetWorkdayPeak() {
  form.value.peak_rules = [
    { days: [1, 2, 3, 4, 5], start: '09:00', end: '12:00', multiplier: 2, label: '工作日高峰' },
    { days: [1, 2, 3, 4, 5], start: '14:00', end: '18:00', multiplier: 2, label: '工作日下午高峰' }
  ]
}

function presetNightDiscount() {
  form.value.peak_rules = [{ days: [], start: '22:00', end: '06:00', multiplier: 0.5, label: '夜间五折' }]
}

const timeRe = /^([01]\d|2[0-3]):[0-5]\d$/

function submit() {
  for (const [i, r] of rules().entries()) {
    if (!timeRe.test(r.start) || !timeRe.test(r.end)) {
      message.error(`第 ${i + 1} 条时段的时间格式应为 HH:MM`)
      return
    }
    if (r.start === r.end) {
      // 起止相同意味着窗口长度为零，永远不会命中
      message.error(`第 ${i + 1} 条时段的开始与结束时间相同，这条规则永远不会生效`)
      return
    }
    if (!(r.multiplier > 0)) {
      message.error(`第 ${i + 1} 条时段的倍率要大于 0`)
      return
    }
  }
  for (const [label, v] of [
    ['输入单价', form.value.input_per_1m],
    ['输出单价', form.value.output_per_1m],
    ['缓存读单价', form.value.cache_read_per_1m],
    ['缓存写单价', form.value.cache_write_per_1m]
  ] as [string, string][]) {
    if (v !== '' && !(Number(v) >= 0)) {
      message.error(label + '要填一个不小于 0 的数字')
      return
    }
  }
  emit('save', JSON.parse(JSON.stringify(form.value)))
  emit('update:open', false)
}
</script>

<template>
  <a-modal
    :open="open"
    :title="'定价 · ' + modelName"
    :width="'min(640px, 94vw)'"
    centered
    @update:open="(v: boolean) => emit('update:open', v)"
  >
    <a-form layout="vertical">
      <div class="price-grid">
        <a-form-item :label="'输入价（每 100 万 token，' + (symbolOf(currency) || '原币') + '）'">
          <a-input v-model:value="form.input_per_1m" placeholder="0.15" />
        </a-form-item>
        <a-form-item label="输出价">
          <a-input v-model:value="form.output_per_1m" placeholder="0.6" />
        </a-form-item>
        <a-form-item label="缓存读价">
          <a-input v-model:value="form.cache_read_per_1m" placeholder="0.003" />
        </a-form-item>
        <a-form-item label="缓存写价">
          <a-input v-model:value="form.cache_write_per_1m" placeholder="0" />
        </a-form-item>
      </div>
      <div class="field-hint currency-hint">
        单价按所属渠道的币种录入：{{ symbolOf(currency) || '原币' }}{{ currency ? '（' + currency + '）' : '' }}。
        改渠道币种不会自动折算已有单价，需要自己重填。
      </div>
      <a-form-item label="固定倍率（1 = 原价，可以填 0.5 表示打折）">
        <a-input-number
          :value="form.multiplier ?? 1"
          :min="0"
          :step="0.1"
          :precision="2"
          style="width: 160px"
          @update:value="(v: number | null) => (form.multiplier = v ?? 1)"
        />
      </a-form-item>

      <a-form-item>
        <template #label>
          <span>时段倍率（命中时段的请求按窗口里的倍率算，优先于固定倍率）</span>
        </template>
        <div class="rule-actions">
          <a-button size="small" @click="presetWorkdayPeak">工作日高峰预设</a-button>
          <a-button size="small" @click="presetNightDiscount">夜间五折预设</a-button>
          <a-button size="small" type="dashed" @click="addRule"><PlusOutlined /> 加一条</a-button>
        </div>
        <div v-for="(rule, i) in rules()" :key="i" class="rule-row">
          <div class="rule-days">
            <!-- 用原生 button 而不是 span+@click：没有 tabindex 的 span
                 键盘永远聚焦不到（WCAG 2.1.1 A 级），键盘用户配不了时段规则 -->
            <button
              v-for="(w, d) in WEEK"
              :key="d"
              type="button"
              class="day-chip"
              :class="{ on: rule.days.includes(d) }"
              :aria-pressed="rule.days.includes(d)"
              @click="toggleDay(rule, d)"
              >{{ w }}</button
            >
          </div>
          <input v-model="rule.start" type="time" class="time-input" />
          <span class="rule-sep">→</span>
          <input v-model="rule.end" type="time" class="time-input" />
          <span class="rule-sep">×</span>
          <a-input-number v-model:value="rule.multiplier" :min="0" :step="0.1" :precision="2" size="small" style="width: 88px" />
          <a-input v-model:value="rule.label" size="small" placeholder="备注" style="width: 110px" />
          <a-button type="text" danger size="small" @click="removeRule(i)"><DeleteOutlined /></a-button>
        </div>
        <div class="rule-hint">
          不选星期表示每天都算。结束时间早于开始时间表示跨午夜（如 22:00 → 06:00）。时段按服务器本地时间判断。
        </div>
      </a-form-item>
    </a-form>

    <template #footer>
      <a-button @click="emit('update:open', false)">取消</a-button>
      <a-button type="primary" @click="submit">保存</a-button>
    </template>
  </a-modal>
</template>

<style scoped>
.price-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0 12px; }
.rule-actions { display: flex; gap: 8px; margin-bottom: 8px; }
.rule-row { display: flex; align-items: center; gap: 6px; margin-bottom: 6px; flex-wrap: wrap; }
.rule-days { display: flex; gap: 2px; }
.day-chip {
  width: 22px; height: 22px; line-height: 22px; text-align: center;
  border: 1px solid var(--color-border); border-radius: 4px;
  font-size: 12px; cursor: var(--cursor-hand); color: var(--color-text-secondary);
  background: transparent; padding: 0;
}
.day-chip:focus-visible { outline: 2px solid var(--color-icon); outline-offset: 1px; }
/* 选中态如实心按钮：用 --solid-primary-* 这一对，而不是
   `background: var(--color-primary); color: #fff`。
   后者是 #c87864 + 白字，只有 3.32:1，而「周一」这些字样是 12px 正文，
   按 AA 需 4.5:1。这一对在浅色下是深底白字（4.84:1）、
   深色下是浅底深字（6.98:1），两套主题都达标。 */
.day-chip.on { background: var(--solid-primary-bg); border-color: var(--solid-primary-bg); color: var(--solid-primary-fg); }
.time-input {
  border: 1px solid var(--color-border); border-radius: var(--radius-control);
  padding: 1px 4px; font-family: var(--font-family-mono); font-size: 12px;
  background: transparent; color: inherit;
}
.rule-sep { color: var(--color-text-secondary); font-size: 12px; }
.rule-hint { color: var(--color-text-secondary); font-size: 12px; margin-top: 4px; }
.currency-hint { margin: -4px 0 12px; color: var(--color-text-secondary); font-size: 12px; line-height: 1.7; }
</style>
