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
import { buildAntdTokens, isDarkTone, readCssVar } from '@/utils/antdTheme'
import { themeById } from '@/utils/themes'

// dayjs 的 locale 决定日期选择器、相对时间等文案，与上面的 locale 是两套，
// 必须一起设置，否则日期面板仍是英文
dayjs.locale('zh-cn')

const themeStore = useThemeStore()

// Ant Design Vue 令牌与本站 CSS 变量同源（2026-09-30 改为运行时读 CSS）。
//
// 这里的 13 个色值全部来自 styles/theme.css 的令牌，映射关系在
// utils/antdTheme.ts 里一处维护。以前它们是手抄的字面量 —— 那时只有两套主题
// 还抄得动，12 套主题 × 13 个值就是 156 份会各自漂移的副本，
// 而漂移的表现是「按钮颜色和卡片不是同一套主题」，不报错、不失败。
//
// 两个值得记住的取色讲究（细节见 antdTheme.ts 与 theme.css）：
//
// 1. colorPrimary 取的是 --solid-primary-bg，**不是** --color-primary。
//    antd 的 type="primary" 按钮是实心填充 + 白字（colorTextLightSolid），
//    字号 14px，属于正文级文字，要按 WCAG AA 的 4.5:1 算。
//    主色 #c87864 上压白字只有 3.32:1 —— 全站 8 个主按钮（新建渠道 / 新建密钥 /
//    保存 / 重试…）的文字都不达标。--solid-primary-bg 是专为「主色铺底 + 上面写字」
//    挑的那一档（浅色族深底白字、深色族浅底深字，两个值必须成对使用）。
// 2. 文字灰阶（二级/占位/三级）必须显式映射：antd 默认的占位文字
//    rgba(0,0,0,.25) 压白底只有 1.84:1 —— 输入框里「这个框该填什么」的
//    唯一线索几乎看不见。
const themeConfig = computed(() => {
  // 依赖的是**主题 id**，不是 tone：同族换主题（Nord → Dracula）时 tone 不变，
  // 若依赖 tone，这个 computed 不会重算，antd 的按钮/表格会停在上一个主题的配色上。
  const def = themeById(themeStore.themeId)
  return {
    algorithm: isDarkTone(def.tone) ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
    token: {
      ...buildAntdTokens(readCssVar),
      borderRadius: 6,
      fontFamily: 'var(--font-family-base)'
    }
  }
})
</script>

<template>
  <a-config-provider :locale="zhCN" :theme="themeConfig">
    <router-view />
  </a-config-provider>
</template>
