<script setup lang="ts">
// 昨日战报：每天第一次打开看板时弹出的那张小卡片。
//
// 为什么自包含（拉数据、判断要不要弹、记录已读都在组件内）：
// 看板页面只需要放一个 <DailyReport />，战报是彩蛋不是功能 ——
// 它失败时必须完全安静（拉不到就不弹），不能让父页面为它操心。
//
// 「每天一次」记在 localStorage：日期相同就不弹。用户主动清存储会
// 再看一次，无伤大雅；跨天自然重置。
import { computed, onMounted, ref } from 'vue'
import { api } from '@/api/client'
import AnimatedNumber from '@/components/AnimatedNumber.vue'
import { currencyKeys, moneyText } from '@/utils/money'
import { burstConfetti } from '@/utils/celebrate'

type Report = {
  day: string
  /** 全表累计请求数：里程碑彩蛋的标尺（见 checkMilestone） */
  lifetime_requests?: number
  summary: {
    requests: number
    success: number
    errors: number
    success_rate: number
    total_tokens: number
    costs: Record<string, string>
  }
  top_model: { model: string; requests: number }
  /** 每币种一条：不同币种的金额不能比大小，各自选自己的最贵 */
  priciest: { model: string; channel: string; cost: string; currency: string }[]
}

const SHOWN_KEY = 'llm-relay-daily-report-shown'
const MILESTONE_KEY = 'llm-relay-milestones'
const today = () => new Date().toDateString()

const open = ref(false)
const report = ref<Report | null>(null)

/**
 * 里程碑彩蛋：累计请求数首次跨过 1 千 / 1 万 / 10 万 / 100 万时撒一把彩带。
 * 已庆祝的档位记在 localStorage，一辈子只庆祝一次。
 * 判定挂在战报弹出时（一天最多一次），所以跨阈值可能延迟到下一次
 * 打开看板才庆祝 —— 里程碑本来就是天级的事，延迟一天不丢人。
 */
function checkMilestone(lifetime: number) {
  const LEVELS = [1000, 10000, 100000, 1000000]
  let done: number[] = []
  try {
    done = JSON.parse(localStorage.getItem(MILESTONE_KEY) || '[]')
  } catch {
    done = []
  }
  const hit = LEVELS.filter((n) => lifetime >= n && !done.includes(n))
  if (hit.length === 0) return
  const reached = hit[hit.length - 1]
  try {
    localStorage.setItem(MILESTONE_KEY, JSON.stringify([...done, ...hit]))
  } catch {
    // 记不下就记不下：顶多下次再庆祝一回，彩带管够
  }
  burstConfetti()
  setTimeout(() => {
    import('ant-design-vue').then(({ message }) => {
      message.success(`累计请求突破 ${reached.toLocaleString()} 次！🎉`)
    })
  }, 600)
}

onMounted(async () => {
  try {
    if (localStorage.getItem(SHOWN_KEY) === today()) return
  } catch {
    // 存储不可用就每次都弹：多看一眼战报不是坏事
  }
  try {
    report.value = await api.get<Report>('/stats/daily-report')
    open.value = true
    if ((report.value.lifetime_requests ?? 0) > 0) {
      checkMilestone(report.value.lifetime_requests!)
    }
  } catch {
    return // 拉不到就不弹，战报保持安静
  }
  try {
    localStorage.setItem(SHOWN_KEY, today())
  } catch {
    // 同上，弹已经弹了
  }
})

const dayLabel = computed(() => {
  if (!report.value) return ''
  const d = new Date(report.value.day + 'T00:00:00')
  if (isNaN(d.getTime())) return report.value.day
  return d.toLocaleDateString('zh-CN', { month: 'long', day: 'numeric', weekday: 'long' })
})

const currencies = computed(() =>
  report.value ? currencyKeys(report.value.summary.costs) : []
)

const rate = computed(() =>
  ((report.value?.summary.success_rate ?? 0) * 100).toFixed(1) + '%'
)

function fmtTokens(n: number) {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B'
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k'
  return String(n ?? 0)
}

// 单笔金额走 utils/money 的统一规则（原来这里是 toFixed(4)：
// 与日志的 6 位、分组的 2 位对不上）。符号也交给同一个函数，
// 不认得的币种它会给「代码 + 空格」而不是猜一个 $。
const priciestLines = computed(() => {
  const list = report.value?.priciest || []
  // 单币种站点保持原样一行；两种币各自一行，互不比较、不合成"全场最贵"
  return list
    .filter((p) => p.model)
    .map((p) => {
      const cur = p.currency || 'USD'
      return `${moneyText(p.cost, cur)} · ${p.model}${p.channel ? ' @ ' + p.channel : ''}`
    })
})
</script>

<template>
  <a-modal
    v-model:open="open"
    :width="'min(420px, 92vw)'"
    centered
    :footer="null"
    class="report-modal"
  >
    <div v-if="report" class="report">
      <div class="report-eyebrow">昨日战报</div>
      <div class="report-day">{{ dayLabel }}</div>

      <template v-if="report.summary.requests > 0">
        <div class="report-grid">
          <div class="report-cell">
            <div class="report-num"><AnimatedNumber :value="report.summary.requests" /></div>
            <div class="report-cap">次请求</div>
          </div>
          <div class="report-cell">
            <div class="report-num">{{ rate }}</div>
            <div class="report-cap">成功率</div>
          </div>
          <div class="report-cell">
            <div class="report-num">{{ fmtTokens(report.summary.total_tokens) }}</div>
            <div class="report-cap">词元</div>
          </div>
        </div>

        <div class="report-line">
          花费
          <span class="report-strong">
            {{ currencies.map((c) => moneyText(report!.summary.costs[c], c)).join('　') }}
          </span>
        </div>
        <div v-if="report.top_model.model" class="report-line">
          最忙模型
          <span class="report-strong">
            {{ report.top_model.model }}（{{ report.top_model.requests }} 次）
          </span>
        </div>
        <div v-if="priciestLines.length" class="report-line">
          {{ priciestLines.length > 1 ? '最贵一单（按币种）' : '最贵一单' }}
          <span class="report-strong">{{ priciestLines.join('　') }}</span>
        </div>
      </template>

      <div v-else class="report-quiet">
        昨天静悄悄 —— 一次请求都没有。<br />今天让它跑起来吧。
      </div>

      <button class="report-ok" @click="open = false">知道了</button>
    </div>
  </a-modal>
</template>

<style scoped>
.report {
  text-align: center;
  padding: 6px 4px 2px;
}
.report-eyebrow {
  font-size: 13px;
  letter-spacing: 0.35em;
  text-indent: 0.35em; /* 让字距在视觉上居中 */
  color: var(--text-primary-ink);
}
.report-day {
  font-size: 22px;
  font-weight: 700;
  margin: 6px 0 18px;
}
.report-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  margin-bottom: 16px;
}
.report-num {
  font-size: 26px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  color: var(--text-primary-ink);
}
.report-cap {
  font-size: 12px;
  color: var(--color-text-secondary);
  margin-top: 2px;
}
.report-line {
  text-align: left;
  font-size: 14px;
  color: var(--color-text-secondary);
  padding: 7px 2px;
  border-top: 1px solid var(--color-border);
  display: flex;
  justify-content: space-between;
  gap: 12px;
}
.report-strong {
  color: var(--color-text);
  font-weight: 600;
  text-align: right;
  word-break: break-all;
}
.report-quiet {
  padding: 18px 0 6px;
  color: var(--color-text-secondary);
  line-height: 1.9;
}
.report-ok {
  margin-top: 18px;
  width: 100%;
  height: 38px;
  border: none;
  border-radius: var(--radius-control);
  background: var(--solid-primary-bg);
  color: var(--solid-primary-fg);
  font-size: 14px;
  font-weight: 600;
  cursor: pointer;
}
.report-ok:hover { filter: brightness(1.05); }
</style>
