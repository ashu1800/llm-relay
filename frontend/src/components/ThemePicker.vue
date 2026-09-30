<script setup lang="ts">
// 主题选择器（2026-09-30 随 12 套主题一起立）。
//
// 两个地方用同一份实现：
//   · 看板工具栏右上角那枚按钮的弹层（dense：迷你色样 + 两列，省地方）
//   · 系统设置页的「界面主题」面板（全尺寸：带一句话说明）
// 分成两份实现的话，「设置页选中的和弹层里选中的不是同一套」这种问题迟早出现。
//
// 交互按原生 radio 组织，不自己写 keydown：单选组天生支持 Tab 进组、
// 方向键换项、Space 选中，读屏也会念「已选中」。radio 本身 1px + opacity 0
// 藏起来但保留可聚焦，焦点环画在卡片上（全站唯一不能直接把环画在元素上的地方，
// 与设置页原来那套动效卡片的做法一致）。
import { computed } from 'vue'
import { useThemeStore } from '@/stores/theme'
import { DEFAULT_DARK, DEFAULT_LIGHT, THEMES, themeById, type ThemeDef } from '@/utils/themes'

const props = defineProps<{
  /** 弹层里的紧凑形态：色样更矮、隐藏说明文字 */
  dense?: boolean
}>()

const store = useThemeStore()

const groups = computed(() => [
  { tone: 'light' as const, label: '浅色', items: THEMES.filter((t) => t.tone === 'light') },
  { tone: 'dark' as const, label: '深色', items: THEMES.filter((t) => t.tone === 'dark') }
])

function isDefault(t: ThemeDef) {
  return t.id === DEFAULT_LIGHT || t.id === DEFAULT_DARK
}

/**
 * 选择一套主题。
 *
 * **只挂 @click，不能同时挂 @change** —— 这不是风格问题，而是一个实测过的坑：
 * withBurst 把 `themeId.value = id` 推迟到 View Transition 的回调里执行
 * （浏览器要先抓旧快照，回调是异步的），而 change 会在同一个 task 里紧跟 click 触发。
 * 两个都挂的话，change 那一刻 store 里的 id 还是旧值，于是它走「普通切换」把 DOM
 * 先改了 —— Chrome 抓到的「旧快照」已经是新主题，扩散动画等于空转（实测属性变更
 * 比过渡回调早 15.6ms）。单挂 click 就没有第二个入口去抢跑。
 *
 * 键盘激活（方向键 / 空格）也会派发 click，但它是合成事件：`detail === 0`、
 * 坐标恒为 (0,0)。这类没有「点击位置」，从 (0,0) 扩散等于从左上角飞出来，
 * 所以只走普通切换。
 */
function onPick(t: ThemeDef, e: MouseEvent) {
  if (e.detail === 0) {
    store.setTheme(t.id)
    return
  }
  store.setTheme(t.id, { x: e.clientX, y: e.clientY })
}
</script>

<template>
  <div class="theme-picker" :class="{ 'is-dense': props.dense }" role="radiogroup" aria-label="界面主题">
    <div v-for="g in groups" :key="g.tone" class="theme-group">
      <div class="theme-group-label" aria-hidden="true">{{ g.label }}</div>
      <div class="theme-grid">
        <label
          v-for="t in g.items"
          :key="t.id"
          class="theme-item"
          :class="{ 'is-active': store.themeId === t.id }"
          :title="`${t.name} · ${t.upstream}`"
        >
          <input
            class="theme-radio"
            type="radio"
            name="ui-theme"
            :value="t.id"
            :checked="store.themeId === t.id"
            @click="onPick(t, $event)"
          />
          <!-- 迷你预览：外圈页面底 + 内嵌卡片底（带描边）+ 右上角主色点。
               四个色值来自 theme.css 的 [data-swatch] 表，不随当前主题变化 ——
               它画的是「这套主题长什么样」，check-contracts.mjs 盯着它与主题块一致。 -->
          <span class="theme-swatch" :data-swatch="t.id" aria-hidden="true">
            <i class="sw-dot"></i>
            <i class="sw-card"></i>
          </span>
          <span class="theme-name">
            {{ t.name }}
            <span v-if="isDefault(t)" class="theme-badge">默认</span>
            <span v-if="store.themeId === t.id" class="theme-check" aria-hidden="true">✓</span>
          </span>
          <span class="theme-upstream">{{ t.upstream }}</span>
          <span v-if="!props.dense" class="theme-desc">{{ t.desc }}</span>
        </label>
      </div>
    </div>
    <div class="theme-foot">
      当前：<span class="theme-current">{{ themeById(store.themeId).name }}</span>
      <span class="theme-foot-sep">·</span>
      勾选即刻生效，偏好只存在这台浏览器（不进配置备份）
    </div>
  </div>
</template>

<style scoped>
.theme-picker {
  width: 100%;
}
.theme-group + .theme-group {
  margin-top: 10px;
}
/* 分组标题：浅色 / 深色。它只是视觉分组，读屏靠每张卡的 radio 名字，
   所以标记 aria-hidden，免得读屏把「浅色」当成一个可选项念出来。 */
.theme-group-label {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  color: var(--color-text-secondary);
  margin-bottom: 6px;
}
.theme-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.theme-picker:not(.is-dense) .theme-grid {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
@media (max-width: 900px) {
  .theme-picker:not(.is-dense) .theme-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 600px) {
  .theme-grid,
  .theme-picker:not(.is-dense) .theme-grid {
    grid-template-columns: 1fr;
  }
}

.theme-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  background: var(--color-bg);
  cursor: var(--cursor-hand);
  transition: border-color 0.15s ease, background-color 0.15s ease;
}
.theme-item:hover {
  border-color: color-mix(in srgb, var(--color-primary) 45%, var(--color-border));
}
/* 选中态只用描边 + 名字颜色 + 勾，**不加底色**。
   加过底（主色 8% 混卡片色），但那样这张卡上的说明文字（--color-text-secondary）
   在 dark / nord / solarized-dark 三套里会掉到 4.08~4.35:1，低于项目自己的
   4.5:1 口径；混 4% 也仍有 4.34。而「不换底色」时这些字落在 --color-bg 上，
   最差一套也有 4.89:1 —— 那正是对比度脚本已经在盯的组合。
   选中的辨识度由三处给足：主色描边、名字变主色墨色、右侧一枚勾。 */
.theme-item.is-active {
  border-color: var(--color-primary);
}
/* radio 藏起来但不能 display:none：那样键盘就聚焦不到它了。
   用 1px + opacity 0 保留可聚焦、可被读屏读到，焦点环改由卡片承担。 */
.theme-radio {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
  pointer-events: none;
}
.theme-item:focus-within {
  outline: var(--focus-ring);
  outline-offset: var(--focus-ring-offset);
  box-shadow: var(--focus-ring-halo-shadow);
}

/* 迷你预览。色值全部来自 [data-swatch='<id>']（theme.css）——
   组件里不写死任何一套主题的颜色，否则加了主题就得回来补预览。 */
.theme-swatch {
  position: relative;
  display: block;
  height: 34px;
  padding: 6px 6px 6px 18px;
  border-radius: var(--radius-control);
  background: var(--sw-page);
  border: 1px solid var(--sw-border);
}
.theme-picker.is-dense .theme-swatch {
  height: 28px;
  padding: 5px 5px 5px 15px;
}
.sw-card {
  display: block;
  width: 100%;
  height: 100%;
  border-radius: 3px;
  background: var(--sw-card);
  border: 1px solid var(--sw-border);
}
.sw-dot {
  position: absolute;
  left: 6px;
  top: 50%;
  width: 8px;
  height: 8px;
  margin-top: -4px;
  border-radius: 50%;
  background: var(--sw-accent);
}

.theme-name {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text);
}
.theme-item.is-active .theme-name {
  color: var(--text-primary-ink);
}
/* 「默认」角标：描边小胶囊，**不填底色**。
   填过 --color-border，但那样角标文字（10px 的次要文字）压在同名的边框色上，
   12 套里有 10 套低于 4.5:1（最差 solarized-dark 3.31）—— 边框色与次要文字
   本来就挨得近，两者互压必然不够。不填底色时它落在 --color-bg 上（4.89:1 起），
   而那对组合已经在对比度脚本的断言表里。 */
.theme-badge {
  font-size: 10px;
  font-weight: 500;
  line-height: 1;
  padding: 2px 5px;
  border: 1px solid var(--color-border);
  border-radius: 4px;
  color: var(--color-text-secondary);
}
.theme-check {
  margin-left: auto;
  color: var(--text-primary-ink);
}
.theme-upstream {
  font-size: 11px;
  line-height: 1.4;
  color: var(--color-text-secondary);
}
.theme-desc {
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-secondary);
}
.theme-foot {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid var(--color-border);
  font-size: 12px;
  color: var(--color-text-secondary);
}
.theme-current {
  color: var(--text-primary-ink);
  font-weight: 600;
}
.theme-foot-sep {
  margin: 0 4px;
}
</style>
