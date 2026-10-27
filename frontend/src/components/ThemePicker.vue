<script setup lang="ts">
import { computed } from 'vue'
import { useThemeStore } from '@/stores/theme'
import { THEMES, themeById, type ThemeDef, type ThemeMode } from '@/utils/themes'

const props = defineProps<{
  /** 弹层里的紧凑形态：只展示分段胶囊 */
  dense?: boolean
}>()

const store = useThemeStore()

interface ThemeModeOption {
  mode: ThemeMode
  label: string
  title: string
}

const MODE_OPTIONS: ThemeModeOption[] = [
  { mode: 'auto', label: '跟随系统', title: '自动跟随操作系统的深浅色设置' },
  { mode: 'light', label: '暖白', title: '明亮温暖的浅色界面' },
  { mode: 'dark', label: '深灰', title: '舒适柔和的深色界面' }
]

const currentModeText = computed(() => {
  if (store.mode === 'auto') {
    return `跟随系统（当前呈现为${themeById(store.themeId).name}）`
  }
  return themeById(store.themeId).name
})

function onPickMode(mode: ThemeMode, e: MouseEvent) {
  if (e.detail === 0) {
    store.setMode(mode)
    return
  }
  store.setMode(mode, { x: e.clientX, y: e.clientY })
}

function onPickTheme(t: ThemeDef, e: MouseEvent) {
  onPickMode(t.id as ThemeMode, e)
}
</script>

<template>
  <div class="theme-picker" :class="{ 'is-dense': props.dense }">
    <!-- 分段胶囊选择器（对齐截图样式） -->
    <div class="theme-segmented" role="radiogroup" aria-label="主题模式选择">
      <button
        v-for="opt in MODE_OPTIONS"
        :key="opt.mode"
        type="button"
        class="seg-item"
        :class="{ 'is-active': store.mode === opt.mode }"
        :title="opt.title"
        :aria-checked="store.mode === opt.mode"
        role="radio"
        @click="onPickMode(opt.mode, $event)"
      >
        <!-- 跟随系统（电脑图标） -->
        <svg v-if="opt.mode === 'auto'" viewBox="0 0 16 16" class="seg-icon" fill="none" stroke="currentColor" stroke-width="1.3">
          <rect x="2" y="2.5" width="12" height="8.5" rx="1.5" />
          <path d="M6 13.5h4M8 11v2.5" />
        </svg>
        <!-- 暖白（太阳图标） -->
        <svg v-else-if="opt.mode === 'light'" viewBox="0 0 16 16" class="seg-icon" fill="none" stroke="currentColor" stroke-width="1.3">
          <circle cx="8" cy="8" r="3" />
          <path d="M8 1.5v1.5M8 13v1.5M1.5 8h1.5M13 8h1.5M3.4 3.4l1.1 1.1M11.5 11.5l1.1 1.1M3.4 12.6l1.1-1.1M11.5 4.5l1.1-1.1" />
        </svg>
        <!-- 深灰（月亮图标） -->
        <svg v-else viewBox="0 0 16 16" class="seg-icon" fill="none" stroke="currentColor" stroke-width="1.3">
          <path d="M13.5 9.5A5.5 5.5 0 1 1 6.5 2.5a4.5 4.5 0 0 0 7 7z" />
        </svg>
        <span class="seg-label">{{ opt.label }}</span>
      </button>
    </div>

    <!-- 非紧凑形态（设置页）：展示卡片与偏好说明 -->
    <div v-if="!props.dense" class="theme-cards-wrap">
      <div class="theme-grid">
        <label
          v-for="t in THEMES"
          :key="t.id"
          class="theme-item"
          :class="{ 'is-active': store.themeId === t.id && store.mode === t.id }"
          :title="`${t.name} · ${t.upstream}`"
          @click="onPickTheme(t, $event)"
        >
          <span class="theme-swatch" :data-swatch="t.id" aria-hidden="true">
            <i class="sw-dot"></i>
            <i class="sw-card"></i>
          </span>
          <span class="theme-name">
            {{ t.name }}
            <span v-if="store.themeId === t.id && store.mode === t.id" class="theme-check" aria-hidden="true">✓</span>
          </span>
          <span class="theme-upstream">{{ t.upstream }}</span>
          <span class="theme-desc">{{ t.desc }}</span>
        </label>
      </div>
      <div class="theme-foot">
        当前模式：<span class="theme-current">{{ currentModeText }}</span>
        <span class="theme-foot-sep">·</span>
        偏好只存在这台浏览器（不进配置备份）
      </div>
    </div>
  </div>
</template>

<style scoped>
.theme-picker {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* ---- 分段胶囊选择器（对齐用户截图） ---- */
.theme-segmented {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 3px;
  border-radius: var(--radius-control);
  background: color-mix(in oklab, var(--color-bg) 70%, var(--color-fg));
  border: 1px solid var(--color-border);
}

.seg-item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  border-radius: calc(var(--radius-control) - 2px);
  border: 1px solid transparent;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--font-size-sm);
  cursor: var(--cursor-hand);
  transition: all 0.2s cubic-bezier(0.34, 1.56, 0.64, 1);
  white-space: nowrap;
}

.seg-item:hover {
  color: var(--color-text);
}

.seg-item.is-active {
  background: var(--color-fg);
  border-color: var(--color-border);
  color: var(--color-text);
  font-weight: 600;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.12);
}

.seg-icon {
  width: 14px;
  height: 14px;
  flex: none;
}

.seg-label {
  line-height: 1.2;
}

/* ---- 设置页大卡片栅格 ---- */
.theme-cards-wrap {
  margin-top: 4px;
}

.theme-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

@media (max-width: 640px) {
  .theme-grid {
    grid-template-columns: 1fr;
  }
}

.theme-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  background: var(--color-bg);
  cursor: var(--cursor-hand);
  transition: border-color 0.15s ease, background-color 0.15s ease;
}

.theme-item:hover {
  border-color: color-mix(in srgb, var(--color-primary) 45%, var(--color-border));
}

.theme-item.is-active {
  border-color: var(--color-primary);
}

.theme-swatch {
  position: relative;
  display: block;
  height: 34px;
  padding: 6px 6px 6px 18px;
  border-radius: var(--radius-control);
  background: var(--sw-page);
  border: 1px solid var(--sw-border);
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
  font-size: var(--font-size-md);
  font-weight: 600;
  color: var(--color-text);
}

.theme-item.is-active .theme-name {
  color: var(--text-primary-ink);
}

.theme-check {
  margin-left: auto;
  color: var(--text-primary-ink);
}

.theme-upstream {
  font-size: var(--font-size-xs);
  line-height: 1.4;
  color: var(--color-text-secondary);
}

.theme-desc {
  font-size: var(--font-size-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.theme-foot {
  margin-top: 10px;
  padding-top: 8px;
  border-top: 1px solid var(--color-border);
  font-size: var(--font-size-sm);
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
