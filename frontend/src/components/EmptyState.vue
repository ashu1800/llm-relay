<script setup lang="ts">
// 全站统一的空态：一块主题感知的插画 + 一句话 + 可选的行动按钮。
//
// ---- 为什么不再直接用 antd 的 <a-empty>（2026-10-10 UI 打磨）----
//
// a-empty 的默认插画是一张**写死浅色**的 SVG（内部 fill 是 #f5f5f5 / #e6e6e6），
// 它不读任何令牌，也不吃 antd 的暗色算法。本站有 8 套深色主题，实测
// （.shots/theme-mocha.png、.shots/theme-nord.png）那块插画在深色卡片上
// 是一块发亮的浅灰图形 —— 12 套主题里唯一「不跟着主题走」的元素。
//
// 这里改成自绘线稿：描边走 color-mix 从 --color-text 派生、点缀走 --color-primary，
// 两族主题自动换档，不需要任何 [data-theme] 分支（theme.css 顶部那条约定）。
//
// ---- 尺寸 ----
//
// 比 antd 的默认插画矮一档（art 72px vs 100px）。空态高度只影响「内容在
// 已撑满的面板里居中到哪儿」，不影响面板底边（.panel :deep(.ds-panel)
// 是 flex: 1 1 auto，见 RequestLogPanel）；而表格 emptyText 那一处是
// 反方向的安全边界 —— 比 antd 矮不会把表体顶高，比它高才会。
defineProps<{
  /** 空态文案。写陈述句（「还没有密钥」），行动引导交给 #action 插槽里的按钮 */
  text: string
  /** sm 用在表格 emptyText 这类本来就窄的容器里 */
  size?: 'sm' | 'md'
}>()
</script>

<template>
  <div class="empty-state" :class="'is-' + (size ?? 'md')">
    <!-- 图形是纯装饰：语义由下面那句话承担，所以 aria-hidden。
         「一支箭头落进托盘」= 还没有东西进来，与日志/请求列表的语义对上。
         描边属性挂在根上而不是两组各写一遍：SVG 的 stroke / stroke-width /
         linecap / linejoin 都是**可继承**的，两个 <g> 直接继承即可
         （stroke 的初始值是 none，所以根上那句 stroke="currentColor" 不能省）。 -->
    <svg
      class="es-art"
      viewBox="0 0 96 72"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <!-- 电气工程美学待命参考线与网格（方案 10 & 方案 9） -->
      <g class="es-radar">
        <circle cx="48" cy="42" r="28" stroke-dasharray="2 4" stroke-width="1" />
        <circle cx="48" cy="42" r="16" stroke-dasharray="1 3" stroke-width="0.8" opacity="0.5" />
      </g>
      <g class="es-grid">
        <line x1="8" y1="56" x2="20" y2="56" stroke-dasharray="2 3" />
        <line x1="76" y1="56" x2="88" y2="56" stroke-dasharray="2 3" />
        <path d="M48 4v6 M45 7h6" />
      </g>
      <g class="es-line">
        <path d="M20 42h56v14a4 4 0 0 1-4 4H24a4 4 0 0 1-4-4z" />
        <path d="M20 42h14l4 6h20l4-6h14" />
        <path d="M48 14v18" />
      </g>
      <g class="es-accent">
        <path d="M42 26l6 6 6-6" />
      </g>
    </svg>

    <p class="es-text">{{ text }}</p>

    <!-- 工业就绪待命徽标（方案 9） -->
    <div class="es-standby-pill" aria-hidden="true">
      <span class="es-standby-dot" />
      <span>STANDBY · 待命接入</span>
    </div>

    <div v-if="$slots.action" class="es-action">
      <slot name="action" />
    </div>
  </div>
</template>

<style scoped>
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: var(--gap);
  text-align: center;
}

/* 线稿颜色：从正文色派生，浅色族是一道淡灰、深色族是一道淡亮 ——
   同一个表达式，两族都对。30% 是「看得见但不抢戏」那一档
   （antd 默认插画的对比度更低，这里刻意比它实一点，因为它现在承载
   的是唯一的视觉锚点）。 */
.es-art {
  color: color-mix(in oklab, var(--color-text) 30%, transparent);
  display: block;
  animation: es-breathe 4.5s ease-in-out infinite;
}

.es-grid {
  opacity: 0.6;
  stroke: color-mix(in oklab, var(--color-text) 22%, transparent);
}

.es-radar {
  opacity: 0.35;
  stroke: color-mix(in oklab, var(--color-primary) 40%, transparent);
}

.es-standby-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 1px 8px;
  border-radius: var(--radius-pill);
  font-family: var(--font-family-mono);
  font-size: var(--font-size-2xs);
  letter-spacing: 0.05em;
  color: var(--color-text-secondary);
  background: color-mix(in oklab, var(--color-border) 35%, transparent);
  border: 1px solid var(--color-border);
  margin-top: 2px;
}

.es-standby-dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--text-green);
  box-shadow: 0 0 5px var(--text-green);
}

@keyframes es-breathe {
  0%, 100% {
    filter: drop-shadow(0 0 0 transparent);
  }
  50% {
    filter: drop-shadow(0 2px 10px color-mix(in oklab, var(--color-primary) 18%, transparent));
  }
}

/* 点缀（箭头）用主色：全站只有主色是「有指向性」的颜色。
   这里改的是 stroke 而不是 color —— 图形本身不是文字，走 color 会被
   check-contracts.mjs 的「文字色令牌契约」判成「把填充色当正文色」，
   而那条守卫不该为了这一处开豁免（豁免一多，守卫就形同虚设）。
   .es-line 不需要规则：它继承根上的 stroke="currentColor"。 */
.es-accent {
  stroke: var(--color-primary);
}

.es-text {
  margin: 0;
  font-size: var(--font-size-md);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.is-md .es-art {
  width: 96px;
  height: 72px;
}

.is-sm .es-art {
  width: 64px;
  height: 48px;
}

.is-sm .es-text {
  font-size: var(--font-size-sm);
}
</style>
