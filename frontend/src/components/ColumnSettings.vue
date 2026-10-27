<script setup lang="ts">
// 表格列显隐的入口：工具栏上一枚图标按钮 + 一个勾选面板。
//
// 做成一个共用组件而不是每张表各写一遍：五张表的交互、文案、无障碍属性
// 应该完全一致，而「勾选面板」这种东西最容易在复制粘贴里长出五套略有差别的
// 版本（一处禁用态忘了写、一处没有 aria-label）。
//
// 无障碍：触发器是 <a-button aria-label>（读屏念「选择显示的列」而不是「按钮」），
// 面板里是原生 checkbox —— 空格切换、Tab 走查、读屏报「已勾选/未勾选」都是
// 浏览器给的，不用自己补。锁定列用 disabled 表达「不可关」，
// 而不是「点了没反应」。
import { computed, ref } from 'vue'
import { SettingOutlined } from '@ant-design/icons-vue'
// 弹层的键盘可达性（打开移焦 / Esc 关闭 / 关闭还焦）：与看板主题弹层、版本面板共用
import { usePopoverA11y, type Focusable } from '@/composables/usePopoverA11y'

export interface ColumnOption {
  key: string
  label: string
  /** 不允许隐藏（如日志表的「请求时间」「操作」） */
  locked?: boolean
}

const props = defineProps<{
  options: ColumnOption[]
  visible: Record<string, boolean>
  /**
   * 行密度（可选）：只有日志表用得上 —— 它有「一屏能看多少行」这个取舍。
   * 其余四张表的数据量小、也没有两排内容的单元格，多一个开关只是噪音。
   */
  density?: 'comfortable' | 'compact'
}>()

const emit = defineEmits<{
  toggle: [string]
  reset: []
  'update:density': ['comfortable' | 'compact']
}>()

// 弹层本身是受控的（a-popover 自己管开合，我们只用 v-model:open 读它），
// 但 usePopoverA11y 需要一个可写的 open ref —— 所以这里显式持有状态。
// 之前是完全非受控的：结果是打开后焦点进不去、Esc 关不掉、关闭后焦点掉到
// body（下一次 Tab 从页面最顶上重来）。
const open = ref(false)
// 触发按钮是 a-button（组件实例，它 expose 了 focus），所以类型用 Focusable 而不是 HTMLElement
const triggerEl = ref<Focusable | null>(null)
const panelEl = ref<HTMLElement | null>(null)
// preferred 指向第一个**可用**的勾选框，而不是让「第一个可聚焦控件」决定：
// 面板里第一个可聚焦的是头部的「恢复默认」，于是「Tab 到设置按钮 → 回车打开 →
// 回车（或按住回车的按键重复）」会直接重置列显隐，而用户以为自己只是打开了面板
// （代码审查实测：打开后 activeElement 正是 .cs-reset）。
//
// :not([disabled]) 不能省：锁定列（日志表的「请求时间」「操作」）的勾选框是
// disabled，而它恰好排在第一个 —— 不排除的话 preferred 会指向一个聚焦不了的
// 元素（usePopoverA11y 里对此有兜底，但那属于「猜错了再退」，不如一开始就选对）。
usePopoverA11y({
  open,
  trigger: triggerEl,
  panel: panelEl,
  preferred: '.cs-item input:not([disabled])'
})

// 「只剩一列时不许再关」这条规则放在面板里，而不是让每个调用方各算一遍：
// 它由 options + visible 完全决定，面板自己就知道。
const visibleCount = computed(() => props.options.filter((o) => props.visible[o.key]).length)

function disabled(opt: ColumnOption): boolean {
  if (opt.locked) return true
  return props.visible[opt.key] === true && visibleCount.value <= 1
}
</script>

<template>
  <a-popover v-model:open="open" trigger="click" placement="bottomRight">
    <template #content>
      <div ref="panelEl" class="col-settings">
        <div class="cs-head">
          <span class="cs-title">显示的列</span>
          <a-button type="link" size="small" class="cs-reset" @click="emit('reset')">恢复默认</a-button>
        </div>
        <div class="cs-list">
          <a-checkbox
            v-for="opt in options"
            :key="opt.key"
            class="cs-item"
            :checked="visible[opt.key]"
            :disabled="disabled(opt)"
            @change="emit('toggle', opt.key)"
          >
            {{ opt.label }}<span v-if="opt.locked" class="cs-lock" title="该列不可隐藏">·</span>
          </a-checkbox>
        </div>
        <!-- 行密度（2026-10-11）：只有需要时才渲染（见 props.density 的说明）。
             放在勾选列表**下面**而不是上面：上面那个位置留给「恢复默认」，
             而它是打开面板之后最可能被按到的东西 —— 密度开关挡在那儿会挨误触。
             用两枚按钮而不是下拉：两个选项的下拉要点两下才看得到当前值。 -->
        <div v-if="density" class="cs-density">
          <span class="cs-title">行密度</span>
          <div class="cs-density-group" role="group" aria-label="日志行密度">
            <button
              type="button"
              class="cs-density-btn"
              :class="{ 'is-on': density === 'comfortable' }"
              :aria-pressed="density === 'comfortable'"
              title="宽松：每行 44px，两排词元与耗时竖条都看得清"
              @click="emit('update:density', 'comfortable')"
            >
              宽松
            </button>
            <button
              type="button"
              class="cs-density-btn"
              :class="{ 'is-on': density === 'compact' }"
              :aria-pressed="density === 'compact'"
              title="紧凑：每行 39px（实测），同一块表体多看到约两行"
              @click="emit('update:density', 'compact')"
            >
              紧凑
            </button>
          </div>
        </div>
      </div>
    </template>
    <!-- 悬停提示走原生 title 而不是 a-tooltip：a-tooltip 会渲染一个
         z-index 1070 的浮层，而它的位置（触发器上方）恰好压住弹层的右上角
         「恢复默认」—— 实测点下去命中的是 .ant-tooltip-inner，恢复默认点不动。
         本页的刷新按钮也是 title + aria-label 这一套，与邻座保持一致。 -->
    <a-button
      ref="triggerEl"
      class="table-icon-btn"
      type="text"
      size="small"
      title="选择显示的列"
      aria-label="选择显示的列"
      aria-haspopup="dialog"
      :aria-expanded="open"
    >
      <template #icon><SettingOutlined /></template>
    </a-button>
  </a-popover>
</template>

<style scoped>
.col-settings { min-width: 168px; }
.cs-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 6px;
}
.cs-title { font-size: var(--font-size-sm); color: var(--color-text-secondary); }
.cs-reset { padding: 0; height: auto; font-size: var(--font-size-sm); }
.cs-list { display: flex; flex-direction: column; gap: 2px; max-height: 320px; overflow-y: auto; }
/* 勾选框本身是 12px 字号（antd 默认 14 会把这面板撑得很高），
   行高与内边距一起收，12 列也能一屏放下 */
.cs-item {
  font-size: var(--font-size-sm);
  line-height: 22px;
  margin: 0;
}
.cs-item :deep(span:last-child) { font-size: var(--font-size-sm); }
/* 锁定列末尾那个点：不是装饰，是「为什么这个勾选点不动」的唯一线索 ——
   disabled 的勾选框自己不会解释原因，而 title 在触屏上没有出口 */
.cs-lock { margin-left: 3px; color: var(--color-text-tertiary); }

/* ---- 行密度（2026-10-11）---- */
.cs-density {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--color-border);
}
.cs-density-group { display: inline-flex; gap: 2px; }
/* 两枚按钮做成「分段控件」：选中的那枚有底色、未选中的透明 ——
   比两个独立按钮更容易读成「二选一」。字号与勾选框一致（12px），
   命中高度 22px（配合上下内边距接近 WCAG 2.5.8 的 24px 下限）。 */
.cs-density-btn {
  padding: 2px 8px;
  min-height: 22px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text-secondary);
  font: inherit;
  font-size: var(--font-size-sm);
  line-height: 18px;
  cursor: var(--cursor-hand);
}
.cs-density-btn:first-child { border-radius: var(--radius-control) 0 0 var(--radius-control); }
.cs-density-btn:last-child { border-radius: 0 var(--radius-control) var(--radius-control) 0; }
.cs-density-btn + .cs-density-btn { border-left: none; }
.cs-density-btn.is-on {
  background: var(--color-primary-a20);
  color: var(--text-primary-ink);
  border-color: var(--color-primary);
}
</style>
