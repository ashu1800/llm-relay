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
//
// colorPrimary 与 --color-primary(#c87864) **有意不同**。
//
// antd 的 type="primary" 按钮是实心填充 + 白字（colorTextLightSolid），
// 字号 14px，属于正文级文字，要按 WCAG AA 的 4.5:1 算。
// #c87864 上压白字只有 3.32:1 —— 全站 8 个主按钮（新建渠道 / 新建密钥 /
// 保存 / 重试…）的文字都不达标。这里换成同色相压暗一档的 #b15840（4.84:1），
// 观感与品牌色一致而文字读得清。
//
// 深色主题则是「浅底 + 深字」：深底上放 #b15840 时，按钮相对卡片(#303030)
// 只有 2.73:1 的轮廓对比，会糊进背景；#e8a48c 对卡片 6.36:1、对页面底
// 7.85:1，配深字 6.98:1，两个方向都够。
//
// 因此深色主题必须同时改 colorTextLightSolid —— 不改的话，浅底上的白字
// 只有 2.08:1，比原来的问题更严重。深色下 error/success/warning 三个语义色
// 也都是浅色（#f08a7a / #45c79a / #e0a83c），配深字分别是 5.95 / 6.82 / 6.78，
// 所以这一处覆盖对它们同样是修正而不是副作用。
const themeConfig = computed(() => ({
  algorithm: themeStore.isDark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
  token: {
    colorPrimary: themeStore.isDark ? '#e8a48c' : '#b15840',
    colorInfo: themeStore.isDark ? '#e8a48c' : '#b15840',
    colorSuccess: themeStore.isDark ? '#45c79a' : '#10b37d',
    colorWarning: themeStore.isDark ? '#e0a83c' : '#f59e0b',
    colorError: themeStore.isDark ? '#f08a7a' : '#ea4343',
    // 实心按钮上的文字：浅色主题是白字压深底，深色主题反过来
    colorTextLightSolid: themeStore.isDark ? '#3a241d' : '#ffffff',
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
