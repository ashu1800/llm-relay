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

// ---- 定价规则的唯一落点 ----
//
// 下面这些常量与判据被三处消费：submit（保存前）、applyImport（导入时）、
// 以及模板上的受控输入上限。此前每处各写一份数字与文案，加一个价格字段或
// 调一次上限就要改三处 —— 漏一处就是「界面放行、后端报错」或者反过来的
// 静默失真。集中在这里，改一处即全生效。

// MAX_MULTIPLIER 与后端 pricing.MaxMultiplier 必须一致：
// 前端放行而后端拒绝，用户会看到「填得进去、保存报错」。
export const MAX_MULTIPLIER = 100

// PRICE_FIELDS 是四个单价的规范清单（展示名 + 字段名）。
// 校验、清零确认、导入拣字段都从它派生，加字段只改这一处。
export const PRICE_FIELDS: [
  label: string,
  key: 'input_per_1m' | 'output_per_1m' | 'cache_read_per_1m' | 'cache_write_per_1m'
][] = [
  ['输入单价', 'input_per_1m'],
  ['输出单价', 'output_per_1m'],
  ['缓存读单价', 'cache_read_per_1m'],
  ['缓存写单价', 'cache_write_per_1m']
]

const timeRe = /^([01]\d|2[0-3]):[0-5]\d$/

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
  }
  return null
}
</script>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { symbolOf } from '@/utils/money'
import { writeClipboard } from '@/utils/clipboard'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined, DeleteOutlined, CopyOutlined, ImportOutlined } from '@ant-design/icons-vue'

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

// 模板只能看到 <script setup> 作用域里的绑定，模块级 script 块的声明要在这里
// 重新绑定一次才能被模板引用（脚本内部则可以直接用模块级的那个）
const maxMultiplier = MAX_MULTIPLIER

const form = ref<PriceConfig>(emptyPrice())

// 打开弹窗那一刻的表单快照。保存时拿它对比：原有单价「有值 → 空」
// 属于几乎必然是误操作的形态（空串提交后端按 0 落库，调用被记 0 元），
// 值得用一次确认弹窗拦一下 —— 2026-09-20 的输入价清零事故正是这个形态。
const openedWith = ref<PriceConfig | null>(null)

watch(
  () => props.open,
  (open) => {
    if (open) {
      form.value = { ...emptyPrice(), ...(props.value || {}), peak_rules: (props.value?.peak_rules || []).map((r) => ({ ...r, days: [...(r.days || [])] })) }
      openedWith.value = JSON.parse(JSON.stringify(form.value))
    }
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

// validatePrice 单价格式：空串（未配）或 ≥0 的数字
function validPrice(v: unknown): boolean {
  if (v === '' || v === null || v === undefined) return true
  return typeof v === 'string' && Number(v) >= 0 && !Number.isNaN(Number(v))
}

// ---- 复制 / 导入定价参数（站主 2026-09-20 要求）----
//
// 场景：同一家上游的多个模型常常同价（或只差输出价），渠道之间也要搬价。
// 逐格手填既慢又容易错位，错位的单价在账面上看不出来 —— 与「未定价记 0」
// 是同一类静默问题。所以给整套参数一个可搬运的形态：JSON。
//
// 复制的是**当前表单**（不是已保存值）：改了两格想搬到隔壁模型时，
// 不必先保存再复制。导入只回填表单、不自动保存 —— 保存路径上的校验
// 一条不少，导入端先做同口径预检只是为了让错误落在导入这步而不是
// 留到保存时才炸。

async function copyPrice() {
  // 只带价格六字段：value 是整条白名单行（含模型名/代理等），照抄会把
  // 行属性混进「定价参数」—— 导入端虽然会忽略，但复制产物应该名实相符
  const f = form.value
  const payload = {
    input_per_1m: f.input_per_1m,
    output_per_1m: f.output_per_1m,
    cache_read_per_1m: f.cache_read_per_1m,
    cache_write_per_1m: f.cache_write_per_1m,
    multiplier: f.multiplier,
    peak_rules: f.peak_rules
  }
  const ok = await writeClipboard(JSON.stringify(payload))
  if (ok) message.success('已复制定价参数（JSON）')
  else message.warning('复制失败，请手动复制')
}

const importOpen = ref(false)
const importText = ref('')

function openImport() {
  importText.value = ''
  importOpen.value = true
}

function applyImport() {
  let raw: any
  try {
    raw = JSON.parse(importText.value)
  } catch {
    message.error('不是合法 JSON')
    return
  }
  if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) {
    message.error('内容应是一个 JSON 对象（复制按钮导出的那种）')
    return
  }

  // 以**当前表单**为基底合并，而不是从空白表单起步：JSON 里没出现的字段必须
  // 保留原值。从 emptyPrice() 起步的旧写法，只要导入内容不含 input_per_1m，
  // 打开时回填的输入价就被静默清成空 —— 点保存提交空串，后端按 0 落库，
  // 表现为「输入价格莫名其妙变成 0」（2026-09-20 实测事故）。
  // 显式传 null 仍是「清空该字段」：null 是明示意图，undefined 才是「没提」。
  const out: PriceConfig = {
    input_per_1m: form.value.input_per_1m,
    output_per_1m: form.value.output_per_1m,
    cache_read_per_1m: form.value.cache_read_per_1m,
    cache_write_per_1m: form.value.cache_write_per_1m,
    multiplier: form.value.multiplier,
    peak_rules: (form.value.peak_rules || []).map((r) => ({ ...r, days: [...(r.days || [])] }))
  }

  // 白名单拣字段：粘贴手编辑过的 JSON 多出来的键直接忽略，
  // 拣不出任何已知键时如实报错（贴错东西最常见的样子）
  let gotAny = false
  for (const [label, key] of PRICE_FIELDS) {
    const v = raw[key]
    if (v === undefined) continue
    if (!validPrice(v)) {
      message.error(`${label}要填一个不小于 0 的数字`)
      return
    }
    ;(out[key] as string) = v === null ? '' : String(v)
    gotAny = true
  }
  if (raw.multiplier !== undefined) {
    const m = Number(raw.multiplier)
    // 与保存/后端同口径：0 视为「没配」归一成 1（「故意填 0」不成立，
    // 免费模型用单价 0 表达）
    if (!(m >= 0) || m > MAX_MULTIPLIER) {
      message.error(`固定倍率需要在 0 到 ${MAX_MULTIPLIER} 之间`)
      return
    }
    out.multiplier = m === 0 ? 1 : m
    gotAny = true
  }
  if (raw.peak_rules !== undefined) {
    if (!Array.isArray(raw.peak_rules)) {
      message.error('时段倍率（peak_rules）应是一个数组')
      return
    }
    const rules: RateRule[] = []
    for (const [i, r] of (raw.peak_rules as any[]).entries()) {
      if (r === null || typeof r !== 'object') {
        message.error(`第 ${i + 1} 条时段规则不是对象`)
        return
      }
      const start = String(r.start ?? '')
      const end = String(r.end ?? '')
      // 判据与 submit 同一份（validatePeakRules），报错带上序号
      const ruleErr = validatePeakRules(
        [{ days: [], start, end, multiplier: Number(r.multiplier) }],
        i
      )
      if (ruleErr) {
        message.error(ruleErr)
        return
      }
      // days 是 0-6（0=周日，与表单 day-chip 同一套索引）；脏值剔掉而不是报错，
      // 手编辑 JSON 时多打个引号很常见
      const days: number[] = Array.isArray(r.days)
        ? [...new Set((r.days as unknown[]).map(Number).filter((d) => Number.isInteger(d) && d >= 0 && d <= 6))]
        : []
      rules.push({ days, start, end, multiplier: Number(r.multiplier), label: r.label ? String(r.label) : '' })
    }
    out.peak_rules = rules
    gotAny = true
  }
  if (!gotAny) {
    message.error('没有识别到任何定价字段（需要 input_per_1m / multiplier / peak_rules 等）')
    return
  }

  // 导入内容没包含、且表单里已有值的字段要点名「保留了」：
  // 否则用户会以为整张表都来自粘贴的内容，核对时把这些格子漏过去
  const kept: string[] = []
  for (const [label, key] of PRICE_FIELDS) {
    const cur = String(form.value[key] ?? '').trim()
    if (raw[key] === undefined && cur !== '') kept.push(label + ' ' + cur)
  }
  if (raw.multiplier === undefined && form.value.multiplier && form.value.multiplier !== 1) {
    kept.push('固定倍率 ×' + form.value.multiplier)
  }
  if (raw.peak_rules === undefined && (form.value.peak_rules || []).length > 0) {
    kept.push('时段倍率 ' + (form.value.peak_rules || []).length + ' 条')
  }

  form.value = out
  importOpen.value = false
  if (kept.length) {
    message.success('已导入；' + kept.join('、') + ' 不在导入内容里，已保留原值，请核对后保存')
  } else {
    message.success('已导入，请核对后保存')
  }
}

function submit() {
  const ruleErr = validatePeakRules(rules())
  if (ruleErr) {
    message.error(ruleErr)
    return
  }
  for (const [label, key] of PRICE_FIELDS) {
    const v = form.value[key] ?? ''
    if (v !== '' && !(Number(v) >= 0)) {
      message.error(label + '要填一个不小于 0 的数字')
      return
    }
  }
  // 防呆：原有单价「有值 → 空」必须二次确认。空值落库就是 0 元计费，
  // 而这个变化只体现在一个小输入框里，几乎没有可见性 ——
  // 明确确认过的不算事故，没确认就清零的才是要拦的
  const before = openedWith.value
  if (before) {
    const wiped = PRICE_FIELDS.filter(([, key]) => {
      const was = String(before[key] ?? '').trim()
      const now = String(form.value[key] ?? '').trim()
      return was !== '' && now === ''
    }).map(([label]) => label)
    if (wiped.length) {
      Modal.confirm({
        centered: true,
        title: '确认清空' + wiped.join('、') + '？',
        content: '清空后按 0 计费，这条模型的对应费用会被记成 0 元。',
        okText: '确认保存',
        cancelText: '返回修改',
        onOk: doSave
      })
      return
    }
  }
  doSave()
}

function doSave() {
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
          <a-input v-model:value="form.cache_write_per_1m" :placeholder="form.input_per_1m || '0'" />
        </a-form-item>
      </div>
      <div class="field-hint currency-hint">
        单价按所属渠道的币种录入：{{ symbolOf(currency) || '原币' }}{{ currency ? '（' + currency + '）' : '' }}。
        改渠道币种不会自动折算已有单价，需要自己重填。缓存写价留空按 0 计。
      </div>
      <a-form-item label="固定倍率（1 = 原价，可以填 0.5 表示打折）">
        <a-input-number
          :value="form.multiplier ?? 1"
          :min="0"
          :max="maxMultiplier"
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
      <!-- 左侧工具按钮（复制/导入），右侧常规动作。图钉成组放在取消/保存
           的对面，不会与主流程误触 —— 复制无破坏性，导入只回填表单 -->
      <div class="pricing-footer">
        <div class="pricing-tools">
          <a-tooltip title="把当前表单的定价参数（单价、倍率、时段规则）复制为 JSON，可粘到其它模型/渠道的导入里">
            <a-button class="table-icon-btn" type="text" size="small" aria-label="复制定价参数" @click="copyPrice">
              <CopyOutlined />
            </a-button>
          </a-tooltip>
          <a-tooltip title="粘贴定价参数 JSON 回填表单（导入后需自己点保存）">
            <a-button class="table-icon-btn" type="text" size="small" aria-label="导入定价参数" @click="openImport">
              <ImportOutlined />
            </a-button>
          </a-tooltip>
        </div>
        <a-button @click="emit('update:open', false)">取消</a-button>
        <a-button type="primary" @click="submit">保存</a-button>
      </div>
    </template>
  </a-modal>

  <!-- 导入弹窗：形态参照白名单编辑器的「批量粘贴」（项目里 a-textarea 的先例） -->
  <a-modal
    v-model:open="importOpen"
    title="导入定价参数"
    :width="'min(560px, 94vw)'"
    centered
    ok-text="导入"
    cancel-text="取消"
    @ok="applyImport"
  >
    <a-textarea
      v-model:value="importText"
      :rows="8"
      placeholder="粘贴「复制」按钮导出的定价 JSON，例如：{&quot;input_per_1m&quot;:&quot;0.15&quot;,&quot;output_per_1m&quot;:&quot;0.6&quot;}"
    />
    <div class="field-hint import-hint">
      只回填表单，不会自动保存 —— 导入后请核对再点「保存」。未知字段会被忽略；
      单价/倍率/时段规则的校验与保存时同一套。
    </div>
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
/* footer 三段式：工具组贴左，取消/保存贴右（modal footer 默认右对齐，
   外面这层 flex 把两个区域分开） */
.pricing-footer { display: flex; align-items: center; gap: 8px; }
.pricing-tools { display: flex; gap: 2px; margin-right: auto; }
.import-hint { margin: 8px 0 0; color: var(--color-text-secondary); font-size: 12px; line-height: 1.7; }
</style>
