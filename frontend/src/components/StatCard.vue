<script setup lang="ts">
import { onUnmounted, ref, useSlots, watch } from 'vue'
// 概览统计卡：左侧彩色图标 + 标题 + 大号数值（对齐参考站 summary-card）
//
// 各项数值来自 docs/layout-dashboard.json 的实测抓取，
// 不是照着截图目测的：
//   summary-icon   48x48，border-radius 8px（圆角方块，不是圆形），
//                  字号 24px，背景为该色调 15% 透明度
//   summary-label  16px / 400 / rgba(48,48,48,0.65)
//   summary-value  24px / 700，颜色 = 该卡的色调色
const props = withDefaults(defineProps<{
  label: string
  value: string | number
  /** 语义色：purple | orange | blue | green | red | gray */
  tone?: 'purple' | 'orange' | 'blue' | 'green' | 'red' | 'gray'
  hint?: string
  /**
   * 数值变化闪光的触发戳：值一变卡片泛起一次本色调的微光。
   * 传时间戳由父组件决定何时闪（通常是实时推送里数字真的变了）。
   * 连续触发时光会保持常亮、停了才熄 —— 「持续进账时光亮着」正是想要的语义。
   */
  flash?: number
  /**
   * 持续的热度状态（预算体系用）：warm = 逼近预算（琥珀晕），
   * hot = 超支（红晕 + 缓慢脉动）。不传 = 常态。
   */
  heat?: 'warm' | 'hot'
}>(), { tone: 'purple' })

// 右侧附加区只在被使用时渲染（useSlots 判断）：
// 空插槽也渲染容器的话，margin-left:auto 会凭空多出一个
// 不可见的弹性项，其余卡片的布局跟着变。
const slots = useSlots()

// 闪光不是「变化瞬间」而是「亮 0.7s 再熄」：流量大时每次推送都重新计时，
// 光就一直在 —— 钱在持续进来，灯不该灭
const flashing = ref(false)
let flashTimer: number | null = null
watch(() => props.flash, (v) => {
  if (!v) return
  flashing.value = true
  if (flashTimer !== null) window.clearTimeout(flashTimer)
  flashTimer = window.setTimeout(() => {
    flashing.value = false
  }, 700)
})
onUnmounted(() => {
  if (flashTimer !== null) window.clearTimeout(flashTimer)
})
</script>

<template>
  <!-- 色调类挂在卡片根节点上，图标与数值都从它取色。
       原来只挂在图标上，于是数值只能固定用正文色 ——
       参考站里数值和图标是同一个颜色，这正是「数值全是黑的」的原因。 -->
  <article
    class="summary-card"
    :class="['tone-' + tone, { 'is-flash': flashing, 'heat-warm': heat === 'warm', 'heat-hot': heat === 'hot' }]"
  >
    <div class="summary-icon">
      <slot name="icon" />
    </div>
    <div class="summary-body">
      <div class="summary-label">{{ label }}</div>
      <!-- 需要滚动动画的数值走这个插槽（看板接的是实时推送）；
           不传插槽时行为完全不变 -->
      <div class="summary-value">
        <slot name="value">{{ value }}</slot>
      </div>
      <div v-if="hint" class="summary-hint">{{ hint }}</div>
    </div>
    <!-- 右侧附加区：放「切换显示格式」这类只作用于本卡的小控件。
         margin-left:auto 把它推到卡片最右、垂直居中；
         只有插槽被使用时才渲染（见 script 里的说明） -->
    <div v-if="slots.suffix" class="summary-suffix">
      <slot name="suffix" />
    </div>
  </article>
</template>

<style scoped>
.summary-card {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: var(--pad-panel);
  background: var(--color-fg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  box-shadow: var(--color-fg-shadow);
}

/* 每种色调只在这里定义一次，图标与数值共用。
   用自定义属性而不是给每个色调各写一条选择器：
   否则加一种色调要改两处，迟早会漏。

   --tone-color 给图标用（大面积色块 + 图形，不适用文字的 4.5:1 要求），
   --tone-ink 给数值文字用 —— 它们必须是可读的正文色。
   两者分开是因为语义色本身在白底上普遍偏浅（#10b37d 只有 2.70:1），
   而深色主题下又要往相反方向调，靠同一个变量做不到两边都达标。

   --tone-bg 从语义色派生（而不是照抄浅色原值的 rgba 字面量）：
   深色主题的 --color-* 已换成提亮版，照抄会让色调底不跟随，
   看板格式切换按钮的 hover（也取 --tone-bg）在深色下反馈偏弱。 */
.tone-purple { --tone-color: var(--color-purple); --tone-bg: color-mix(in oklab, var(--color-purple) 15%, transparent); --tone-ink: var(--text-purple); }
.tone-orange { --tone-color: var(--color-orange); --tone-bg: color-mix(in oklab, var(--color-orange) 15%, transparent); --tone-ink: var(--text-amber); }
.tone-blue   { --tone-color: var(--color-blue);   --tone-bg: color-mix(in oklab, var(--color-blue) 15%, transparent);   --tone-ink: var(--text-blue); }
.tone-green  { --tone-color: var(--color-green);  --tone-bg: color-mix(in oklab, var(--color-green) 15%, transparent); --tone-ink: var(--text-green); }
.tone-red    { --tone-color: var(--color-red);    --tone-bg: color-mix(in oklab, var(--color-red) 15%, transparent);    --tone-ink: var(--text-red); }
.tone-gray   { --tone-color: var(--color-gray);   --tone-bg: color-mix(in oklab, var(--color-gray) 15%, transparent);   --tone-ink: var(--text-gray); }

.summary-icon {
  width: 48px;
  height: 48px;
  flex: 0 0 48px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  font-size: 24px;
  color: var(--tone-color);
  background: var(--tone-bg);
}

.summary-body { min-width: 0; }

.summary-label {
  font-size: 16px;
  font-weight: 400;
  line-height: 1.15;
  /* 用变量而不是写死 rgba(48,48,48,0.65)：
     那个字面值正是本变量在亮色主题下的取值（见 docs/ui-spec.json），
     写死的话暗色主题下会变成深色字压深色底 */
  color: var(--color-text-secondary);
}

.summary-value {
  font-size: 24px;
  font-weight: 700;
  line-height: 1.2;
  /* 数值是正文内容，用 --tone-ink 而不是 --tone-color：
     后者在浅色底上普遍只有 2.4–4.2:1，读起来发虚 */
  color: var(--tone-ink);
  font-variant-numeric: tabular-nums;
  word-break: break-all;
}

.summary-hint {
  font-size: 14px;
  line-height: 1.3;
  color: var(--color-text-secondary);
}

/* 右侧附加区。色调变量（--tone-*）定义在卡片根上，
   这里的控件（如格式切换按钮）直接继承本卡的色调体系 */
.summary-suffix {
  margin-left: auto;
  align-self: center;
  flex: 0 0 auto;
  display: flex;
  align-items: center;
}

/* ---- 数值变化闪光 ----
   光晕用本卡色调（--tone-color）：请求数闪紫、花费闪橙，
   不用眼睛读数字，余光扫过就知道是哪张卡在动 */
.summary-card.is-flash {
  animation: card-flash 0.7s ease-out;
}
@keyframes card-flash {
  0% {
    box-shadow:
      var(--color-fg-shadow),
      0 0 0 0 color-mix(in oklab, var(--tone-color) 42%, transparent);
  }
  100% {
    box-shadow:
      var(--color-fg-shadow),
      0 0 16px 3px transparent;
  }
}

/* ---- 预算热度 ----
   warm（逼近 80%）与 hot（超支）都是「常驻状态色」而不是一次动画：
   卡片边框着色 + 向外柔光，hot 再叠一层缓慢脉动 ——
   体温计越烧越红，收回预算线以下自然消失（跨天/后端不再推 hot） */
.summary-card.heat-warm {
  border-color: color-mix(in oklab, var(--color-orange) 55%, var(--color-border));
  box-shadow: var(--color-fg-shadow), 0 0 14px color-mix(in oklab, var(--color-orange) 20%, transparent);
}
.summary-card.heat-hot {
  border-color: color-mix(in oklab, var(--color-red) 60%, var(--color-border));
  box-shadow: var(--color-fg-shadow), 0 0 16px color-mix(in oklab, var(--color-red) 24%, transparent);
  animation: heat-pulse 2.4s ease-in-out infinite;
}
@keyframes heat-pulse {
  0%,
  100% {
    box-shadow: var(--color-fg-shadow), 0 0 10px color-mix(in oklab, var(--color-red) 14%, transparent);
  }
  50% {
    box-shadow: var(--color-fg-shadow), 0 0 20px color-mix(in oklab, var(--color-red) 30%, transparent);
  }
}
</style>
