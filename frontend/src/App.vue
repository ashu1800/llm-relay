<script setup lang="ts">
import { computed } from 'vue'
import { theme as antdTheme } from 'ant-design-vue'
import { useThemeStore } from '@/stores/theme'

const themeStore = useThemeStore()

// Ant Design Vue 令牌与参考站 CSS 变量保持同源
const themeConfig = computed(() => ({
  algorithm: themeStore.isDark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
  token: {
    colorPrimary: '#c87864',
    colorInfo: '#c87864',
    colorSuccess: '#10b37d',
    colorWarning: '#f59e0b',
    colorError: '#ea4343',
    colorTextBase: themeStore.isDark ? '#c8c8c8' : '#303030',
    colorBgBase: themeStore.isDark ? '#202020' : '#f8f5ee',
    colorBgContainer: themeStore.isDark ? '#303030' : '#ffffff',
    colorBorder: themeStore.isDark ? '#424242' : '#d9d9d9',
    borderRadius: 6,
    fontFamily: 'var(--font-family-base)'
  }
}))
</script>

<template>
  <a-config-provider :theme="themeConfig">
    <router-view />
  </a-config-provider>
</template>
