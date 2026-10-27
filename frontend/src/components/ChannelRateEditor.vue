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

// ChannelRate 是**渠道**的定价属性：只有倍率与时段规则。
//
// 2026-10-27：单价已迁到模型级（见 components/modelPricing.ts 与
// views/ModelPricingView.vue）。这里刻意只留两件事 —— 它们表达的正是
// 「这条渠道比标价贵/便宜多少」，与「某个模型值多少钱」是两个问题，
// 混在一个编辑器里正是原来那个 740 行组件的来由。
export interface ChannelRate {
  multiplier?: number
  peak_rules?: RateRule[]
}

/**
 * 从任意带倍率字段的对象里取出渠道定价属性。
 *
 * 读回来的绑定要经过这一步再进表单：漏掉它的话，保存时提交的 items 里
 * 没有这些字段，而后端把「空」当成 0 —— 表现为「编辑一次渠道，倍率全没了」，
 * 界面上还提示保存成功。
 */
export function pickRate(
  src:
    | {
        multiplier?: number | null
        peak_rules?: RateRule[] | null
      }
    | null
    | undefined
): ChannelRate {
  if (!src) return emptyRate()
  return {
    multiplier: src.multiplier ?? 1,
    peak_rules: (src.peak_rules || []).map((r) => ({ ...r, days: [...(r.days || [])] }))
  }
}

export function emptyRate(): ChannelRate {
  return { multiplier: 1, peak_rules: [] }
}

/** 这条渠道有没有配过非默认的定价：倍率 ≠ 1 或有任何时段规则 */
export function hasRate(p?: ChannelRate | null): boolean {
  if (!p) return false
  if (p.multiplier && p.multiplier !== 1) return true
  return (p.peak_rules || []).length > 0
}

/**
 * 列表里显示的一行摘要。
 *
 * 不带币种符号：倍率是无量纲的乘数，与币种无关 ——
 * 币种和单价一起住在模型定价页。
 */
export function rateSummary(p?: ChannelRate | null): string {
  if (!hasRate(p)) return '原价'
  const q = p as ChannelRate
  const rules = q.peak_rules || []
  if (rules.length) {
    return '时段×' + multiplierText(rules[0].multiplier) + (rules.length > 1 ? ' 等' + rules.length + '条' : '')
  }
  return '×' + multiplierText(q.multiplier)
}

/**
 * 倍率的展示写法（摘要行、确认弹窗里用）。
 *
 * 不用裸的 `String(m)`：倍率要经过 multiply/divide 之类的运算才到这里，
 * 浮点尾数会让 0.1875 显示成 0.18749999999999997 —— 那串数字会直接被
 * 用户当成「系统算错了」。toFixed(4) 之后再去掉尾零，既能盖住尾数，
 * 又不会把 0.5 显示成 0.5000。
 */
export function multiplierText(v: number | null | undefined): string {
  const n = Number(v)
  if (!Number.isFinite(n)) return String(v ?? '')
  return String(Number(n.toFixed(MULTIPLIER_PRECISION)))
}

// MAX_MULTIPLIER 与后端 pricing.MaxMultiplier 必须一致：
// 前端放行而后端拒绝，用户会看到「填得进去、保存报错」。
export const MAX_MULTIPLIER = 100

// 倍率的小数位数（2026-09-24）。
//
// 这里以前是 2，于是**填不进 0.1875 这种四位小数**：antd 的
// a-input-number 会在失焦时按 precision 四舍五入，用户键入 0.1875、
// 松开后输入框自己变成 0.19 —— 保存下去的是 0.19，而他以为自己填的是
// 0.1875。这类「界面悄悄改掉你的值」比直接报错难查得多：账单算出来不对，
// 回头核对时看到的是 0.19，怎么看都「没错」。
//
// 为什么是 4 位：数据库列就是 numeric(10,4)，4 位是存储的真上限。
export const MULTIPLIER_PRECISION = 4

// 步进键的步长。取 0.0001 而不是 0.1：既然允许四位小数，
// 步长就该能一格一格走到任意一位。
export const MULTIPLIER_STEP = 0.0001

const timeRe = /^([01]\d|2[0-3]):[0-5]\d$/

/**
 * 倍率的小数位是否在存储精度以内（MULTIPLIER_PRECISION 位）。
 *
 * 判据用字符串而不是数字：JS 里 0.1 + 0.2 这种浮点误差会让
 * 「× 10000 后取整」的做法把 0.1875 判成 1874.9999… 而误报。
 */
export function multiplierPrecisionError(v: unknown): string | null {
  const s = String(v ?? '').trim()
  if (!s) return null
  const n = Number(s)
  if (!Number.isFinite(n)) return `倍率「${s}」不是合法数字`
  const plain = n.toString()
  if (plain.includes('e') || plain.includes('E')) {
    return `倍率 ${s} 太小，超出 ${MULTIPLIER_PRECISION} 位小数的精度`
  }
  const dot = plain.indexOf('.')
  const decimals = dot < 0 ? 0 : plain.length - dot - 1
  if (decimals > MULTIPLIER_PRECISION) {
    return `倍率最多 ${MULTIPLIER_PRECISION} 位小数（${s} 会被截断成 ${n.toFixed(MULTIPLIER_PRECISION)}）`
  }
  return null
}

// validatePeakRules 校验时段规则，返回错误文案（无错返回 null）。
// idx 非空时在文案前加「第 N 条」——导入路径要指出是数组里哪一条。
export function validatePeakRules(rules: RateRule[], idx?: number): string | null {
  const at = idx === undefined ? '' : `第 ${idx + 1} 条`
  for (const r of rules) {
    if (!timeRe.test(r.start || '') || !timeRe.test(r.end || '')) {
      return `${at}时段的时间格式应为 HH:MM`
    }
    if (r.start === r.end) {
      // 起止相同意味着窗口长度为零，永远不会命中
      return `${at}时段的开始与结束时间相同，这条规则永远不会生效`
    }
    if (!(Number(r.multiplier) > 0)) {
      return `${at}时段的倍率要大于 0`
    }
    const precErr = multiplierPrecisionError(r.multiplier)
    if (precErr) return `${at}时段${precErr}`
  }
  return null
}
</script>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { modalWidth } from '@/utils/modalWidth'
import { message } from '@/utils/feedback'

// 渠道倍率编辑器：固定倍率 + 时段规则。
//
// 与原来的 ModelPricingEditor（740 行，含四个单价 + 币种 + 导入导出 +
// 清零确认）相比，这里只保留真正的渠道属性。单价搬到模型定价页之后，
// 那一整块（单价输入、币种符号、按币种清零确认）都不再属于渠道。
const props = defineProps<{
  open: boolean
  modelName: string
  value?: ChannelRate | null
}>()

const emit = defineEmits<{
  (e: 'update:open', v: boolean): void
  (e: 'save', cfg: ChannelRate): void
}>()

const form = ref<ChannelRate>(emptyRate())

watch(
  () => props.open,
  (open) => {
    if (open) form.value = pickRate(props.value)
  },
  { immediate: true }
)

function close() {
  emit('update:open', false)
}

const multiplierErr = computed(() => {
  const v = form.value.multiplier
  if (v === undefined || v === null || v === 1) return ''
  if (Number(v) < 0) return '倍率不能为负（1 表示原价，0.5 表示五折）'
  if (Number(v) > MAX_MULTIPLIER) return `倍率不能超过 ${MAX_MULTIPLIER}`
  return multiplierPrecisionError(v) || ''
})

const rulesErr = computed(() => validatePeakRules(form.value.peak_rules || []))

function addRule() {
  const rules = form.value.peak_rules || []
  form.value.peak_rules = [...rules, { days: [], start: '09:00', end: '18:00', multiplier: 1 }]
}

function removeRule(i: number) {
  const rules = [...(form.value.peak_rules || [])]
  rules.splice(i, 1)
  form.value.peak_rules = rules
}

function save() {
  if (multiplierErr.value || rulesErr.value) {
    message.error(multiplierErr.value || rulesErr.value || '')
    return
  }
  emit('save', {
    multiplier: form.value.multiplier ?? 1,
    peak_rules: form.value.peak_rules || []
  })
  close()
}

const WEEKDAYS = ['日', '一', '二', '三', '四', '五', '六']

function parseMinutes(t: string): number {
  const [h, m] = (t || '').split(':').map(Number)
  if (isNaN(h)) return 0
  return Math.min(1440, Math.max(0, h * 60 + (isNaN(m) ? 0 : m)))
}

interface HeatSegment {
  left: number
  width: number
  kind: 'is-peak' | 'is-valley' | 'is-normal'
}

const heatSegments = computed<HeatSegment[]>(() => {
  const rules = form.value.peak_rules || []
  const list: HeatSegment[] = []
  for (const r of rules) {
    const s = parseMinutes(r.start)
    const e = parseMinutes(r.end)
    const m = Number(r.multiplier) || 1
    const kind = m > 1 ? 'is-peak' : m < 1 ? 'is-valley' : 'is-normal'
    if (e > s) {
      list.push({ left: (s / 1440) * 100, width: ((e - s) / 1440) * 100, kind })
    } else if (e < s) {
      list.push({ left: (s / 1440) * 100, width: ((1440 - s) / 1440) * 100, kind })
      list.push({ left: 0, width: (e / 1440) * 100, kind })
    }
  }
  return list
})
</script>

<template>
  <a-modal
    :open="open"
    :title="'渠道倍率：' + (modelName || '未命名模型')"
    :width="modalWidth(520)"
    ok-text="保存"
    cancel-text="取消"
    destroy-on-close
    @update:open="(v: boolean) => emit('update:open', v)"
    @ok="save"
  >
    <p class="tip">
      倍率表达「这条渠道比标价贵/便宜多少」：1 = 按模型定价原价，0.5 = 五折，2 = 两倍。
      <strong>单价本身在「模型定价」页按模型设置。</strong>
    </p>

    <a-form layout="vertical">
      <a-form-item label="固定倍率">
        <a-input-number
          v-model:value="form.multiplier"
          :min="0"
          :max="MAX_MULTIPLIER"
          :step="MULTIPLIER_STEP"
          :precision="MULTIPLIER_PRECISION"
          style="width: 160px"
        />
        <div v-if="multiplierErr" class="field-err">{{ multiplierErr }}</div>
      </a-form-item>

      <a-form-item label="时段倍率规则（命中时优先于固定倍率）">
        <div v-if="!(form.peak_rules || []).length" class="muted">没有时段规则，全天按固定倍率。</div>
        <div v-for="(r, i) in form.peak_rules || []" :key="i" class="rule">
          <a-select
            v-model:value="r.days"
            mode="multiple"
            placeholder="每天"
            style="width: 100%"
            :options="WEEKDAYS.map((d, idx) => ({ value: idx, label: '周' + d }))"
          />
          <div class="rule-line">
            <a-input v-model:value="r.start" placeholder="09:00" style="width: 84px" />
            <span class="muted">至</span>
            <a-input v-model:value="r.end" placeholder="18:00" style="width: 84px" />
            <span class="muted">倍率</span>
            <a-input-number
              v-model:value="r.multiplier"
              :min="0"
              :max="MAX_MULTIPLIER"
              :step="MULTIPLIER_STEP"
              :precision="MULTIPLIER_PRECISION"
              style="width: 110px"
            />
            <a-button danger size="small" @click="removeRule(i)">
              <template #icon><DeleteOutlined /></template>
            </a-button>
          </div>
        </div>
        <a-button size="small" @click="addRule">
          <template #icon><PlusOutlined /></template>
          添加时段规则
        </a-button>

        <!-- 24 小时昼夜能量热力光带（方案 4） -->
        <div v-if="(form.peak_rules || []).length > 0" class="heat-ribbon-wrap" aria-hidden="true">
          <div class="heat-ribbon-bar">
            <span
              v-for="(seg, idx) in heatSegments"
              :key="idx"
              class="heat-segment"
              :class="seg.kind"
              :style="{ left: seg.left + '%', width: seg.width + '%' }"
            />
          </div>
          <div class="heat-ribbon-labels">
            <span>00:00</span>
            <span>06:00</span>
            <span>12:00</span>
            <span>18:00</span>
            <span>24:00</span>
          </div>
        </div>

        <div v-if="rulesErr" class="field-err">{{ rulesErr }}</div>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<style scoped>
.tip {
  margin: 0 0 12px;
  font-size: var(--font-size-sm);
  line-height: 1.7;
  color: var(--color-text-secondary);
}

.heat-ribbon-wrap {
  margin-top: 12px;
  padding: 8px 10px;
  background: color-mix(in oklab, var(--color-bg) 60%, var(--color-fg));
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
}

.heat-ribbon-bar {
  position: relative;
  height: 8px;
  border-radius: var(--radius-pill);
  background: color-mix(in oklab, var(--color-text-secondary) 15%, transparent);
  overflow: hidden;
}

.heat-segment {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: var(--radius-pill);
}

.heat-segment.is-peak {
  background: var(--color-orange);
  box-shadow: 0 0 6px var(--color-orange);
}

.heat-segment.is-valley {
  background: var(--color-blue);
  box-shadow: 0 0 6px var(--color-blue);
}

.heat-segment.is-normal {
  background: var(--color-green);
}

.heat-ribbon-labels {
  display: flex;
  justify-content: space-between;
  margin-top: 4px;
  font-family: var(--font-family-mono);
  font-size: var(--font-size-2xs);
  color: var(--color-text-tertiary);
}

.muted {
  color: var(--color-text-secondary);
}

.rule {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px;
  margin-bottom: 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
}

.rule-line {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.field-err {
  margin-top: 2px;
  font-size: var(--font-size-sm);
  color: var(--text-red);
}
</style>
