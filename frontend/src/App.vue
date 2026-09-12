<script setup lang="ts">
import { computed } from 'vue'
import { theme as antdTheme } from 'ant-design-vue'
// 组件内置文案（分页、空表格、弹窗按钮、日期面板等）走 Ant Design Vue 的 locale，
// 不配的话这些地方一律是英文：分页显示「10 / page」、确认弹窗是 OK / Cancel，
// 而页面其余部分全是中文，混在一起很突兀。
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import dayjs from 'dayjs'
import 'dayjs/locale/zh-cn'
import { useThemeStore } from '@/stores/theme'

// dayjs 的 locale 决定日期选择器、相对时间等文案，与上面的 locale 是两套，
// 必须一起设置，否则日期面板仍是英文
dayjs.locale('zh-cn')

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
  <a-config-provider :locale="zhCN" :theme="themeConfig">
    <router-view />
  </a-config-provider>
</template>
