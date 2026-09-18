<script setup lang="ts">
// 活跃热力图：最近 7 天 × 24 小时的使用日记（GitHub 贡献图风格）。
//
// 它与被移除的「图表区」不是一回事：趋势图回答分析性的问题，
// 热力图回答的是纯兴趣的问题 ——「我什么时段用得最凶」。所以它
// 默认折叠、一格一色、没有任何坐标轴，扫一眼有个印象就够。
// 数据来自既有的 /stats/heatmap 端点（days=7，口径与看板一致）。
import { computed, onMounted, ref } from 'vue'
import { api } from '@/api/client'
import { currencyKeys, moneyText } from '@/utils/money'

type Cell = { day: string; hour: number; requests: number; costs: Record<string, string>; tokens: number }

const WEEK = ['日', '一', '二', '三', '四', '五', '六']
const HOURS = Array.from({ length: 24 }, (_, i) => i)

const open = ref(false)
const loaded = ref(false)
const cells = ref<Cell[]>([])
/** (day,hour) -> 请求次数，渲染查表用 */
const byKey = computed(() => {
  const m = new Map<string, Cell>()
  for (const c of cells.value) m.set(c.day + '#' + c.hour, c)
  return m
})
const maxRequests = computed(() => cells.value.reduce((a, c) => Math.max(a, c.requests), 0))
/** 最近的 7 个日期（今天在最右），与后端 days=7 的覆盖面对齐 */
const days = computed(() => {
  const out: string[] = []
  for (let i = 6; i >= 0; i--) {
    const d = new Date()
    d.setDate(d.getDate() - i)
    out.push(`${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`)
  }
  return out
})

async function toggle() {
  open.value = !open.value
  if (open.value && !loaded.value) {
    try {
      const res = await api.get<{ items: Cell[] }>('/stats/heatmap?days=7')
      cells.value = res.items || []
    } catch {
      cells.value = [] // 热力图是彩蛋，拉不到就画一张空的，不报错
    }
    loaded.value = true
  }
}

function cell(day: string, hour: number) {
  return byKey.value.get(day + '#' + hour)
}

/** 强度 0-4：按请求次数相对峰值分档，0 档几乎不可见 —— 空格子也是信息 */
function level(c: Cell | undefined) {
  if (!c || c.requests <= 0) return 0
  const r = c.requests / Math.max(1, maxRequests.value)
  if (r > 0.66) return 4
  if (r > 0.4) return 3
  if (r > 0.18) return 2
  return 1
}

function cellTitle(day: string, hour: number) {
  const c = cell(day, hour)
  if (!c || c.requests <= 0) return `${day} ${String(hour).padStart(2, '0')}:00 — 无流量`
  const costs = currencyKeys(c.costs).map((k) => moneyText(c.costs[k], k)).join(' / ')
  return `${day} ${String(hour).padStart(2, '0')}:00 — ${c.requests} 次请求${costs ? ' · ' + costs : ''}`
}

onMounted(() => {
  // 首次展开才拉数据（见 toggle）：折叠着的内容不值得白打一个请求
})
</script>

<template>
  <section class="heat-panel" :class="{ open }">
    <button class="heat-head" :aria-expanded="open" @click="toggle">
      <span class="heat-title">最近 7 天活跃热力</span>
      <span class="heat-arrow" aria-hidden="true">{{ open ? '收起 ▾' : '展开 ▸' }}</span>
    </button>
    <div v-if="open" class="heat-body">
      <div class="heat-grid">
        <!-- 小时轴：每 3 小时一个标签，中间留空保持对齐 -->
        <div class="heat-hours">
          <span v-for="h in HOURS" :key="h" class="heat-hour">
            {{ h % 3 === 0 ? String(h).padStart(2, '0') : '' }}
          </span>
        </div>
        <div v-for="(day, di) in days" :key="day" class="heat-col">
          <div class="heat-dow">{{ WEEK[(new Date(day + 'T00:00:00')).getDay()] }}</div>
          <div
            v-for="h in HOURS"
            :key="h"
            class="heat-cell"
            :class="'lv' + level(cell(day, h))"
            :title="cellTitle(day, h)"
          />
          <span v-if="di === days.length - 1" class="heat-today">今</span>
        </div>
      </div>
      <div class="heat-legend">
        安静
        <span class="heat-cell lv0" /><span class="heat-cell lv1" /><span class="heat-cell lv2" /><span class="heat-cell lv3" /><span class="heat-cell lv4" />
        疯狂
      </div>
    </div>
  </section>
</template>

<style scoped>
.heat-panel {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  background: var(--color-fg);
  margin-bottom: var(--gap);
  overflow: hidden;
}
.heat-head {
  width: 100%;
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 14px;
  background: none;
  border: none;
  cursor: pointer;
  font: inherit;
  color: var(--color-text);
}
.heat-title { font-size: 13px; font-weight: 600; }
.heat-arrow { font-size: 12px; color: var(--color-text-secondary); }

.heat-body { padding: 4px 14px 12px; }
.heat-grid {
  display: flex;
  gap: 4px;
}
.heat-hours {
  display: grid;
  grid-template-rows: repeat(25, 13px);
  gap: 2px;
  font-size: 10px;
  color: var(--color-text-secondary);
  text-align: right;
  flex: none;
}
.heat-hour { line-height: 13px; padding-right: 2px; }
.heat-col {
  display: grid;
  grid-template-rows: 13px repeat(24, 13px);
  gap: 2px;
  flex: 1;
  position: relative;
  min-width: 0;
}
.heat-dow {
  font-size: 10px;
  color: var(--color-text-secondary);
  text-align: center;
  line-height: 13px;
}
.heat-cell {
  border-radius: 3px;
  background: color-mix(in oklab, var(--color-green) 7%, transparent);
}
.heat-cell.lv1 { background: color-mix(in oklab, var(--color-green) 26%, transparent); }
.heat-cell.lv2 { background: color-mix(in oklab, var(--color-green) 48%, transparent); }
.heat-cell.lv3 { background: color-mix(in oklab, var(--color-green) 72%, transparent); }
.heat-cell.lv4 { background: var(--color-green); }

.heat-today {
  position: absolute;
  top: 0;
  right: -2px;
  font-size: 10px;
  color: var(--text-primary-ink);
}
.heat-legend {
  display: flex;
  align-items: center;
  gap: 4px;
  justify-content: flex-end;
  margin-top: 8px;
  font-size: 11px;
  color: var(--color-text-secondary);
}
.heat-legend .heat-cell { width: 11px; height: 11px; }
</style>
