// 图表配色统一从 CSS 变量取，不在 option 里写死颜色。
//
// 为什么必须这样：echarts 不认 CSS 变量，颜色只能由 JS 传进 option。
// 一旦写死成浅色主题的值，切到暗色就成了「深灰字压深色底」——
// 图例最容易漏，因为不设 textStyle.color 时 echarts 用的是自带默认色 #333，
// 在暗色背景上等于隐形；坐标轴标签同理。
//
// 颜色仍然只在 theme.css 定义一处：本文件只负责把变量读出来交给 echarts。
// 界面主题由 data-theme 属性切换（见 stores/theme.ts），所以这里读到的
// 就是当前主题生效后的值。
import { computed } from 'vue'
import { useThemeStore } from '@/stores/theme'

export function useChartTheme() {
  const themeStore = useThemeStore()

  return computed(() => {
    // 必须读一次 isDark 来建立响应式依赖：
    // getComputedStyle 不是响应式的，不建立依赖的话 computed 会把第一次
    // 的结果一直缓存下去，切主题后图表不会重算，颜色就停在旧主题上。
    const dark = themeStore.isDark
    const css = (name: string, fallback: string) =>
      getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback

    // 语义色读的是 --color-*（不是 --text-*）：这两组的分工是
    // 「大色块 / 图形」用 --color-*，「正文文字」用 --text-*。
    // 图表里的柱、饼、线都属于前者，用 --color-* 才对得上设计意图；
    // 暗色主题下这组已被整体替换成提亮版（见 theme.css 的 dark 块），
    // 所以同一个色相在两套主题下都是可见的。
    const palette = [
      css('--color-primary', dark ? '#e8a48c' : '#c87864'),
      css('--color-purple', dark ? '#ab8ef2' : '#8b5cf5'),
      css('--color-blue', dark ? '#4dd0e1' : '#06b6d4'),
      css('--color-green', dark ? '#45c79a' : '#10b37d'),
      css('--color-orange', dark ? '#e0a83c' : '#f59e0b'),
      css('--color-red', dark ? '#f08a7a' : '#ea4343'),
      css('--color-gray', dark ? '#9aa1ac' : '#6b7280')
    ]

    return {
      /** 图例文字、需要看清的主要标签 */
      text: css('--color-text', dark ? '#c8c8c8' : '#303030'),
      /** 坐标轴标签等次要文字 */
      secondary: css(
        '--color-text-secondary',
        dark ? 'rgba(200, 200, 200, 0.65)' : 'rgba(48, 48, 48, 0.65)'
      ),
      /** 坐标轴线 */
      border: css('--color-border', dark ? '#424242' : '#d9d9d9'),
      /** 网格线：比边框再淡一档，两套主题下都不抢视线 */
      split: dark ? 'rgba(255, 255, 255, 0.10)' : 'rgba(0, 0, 0, 0.06)',

      /** 多序列图表的分类色板（饼图、柱状图按类别取色） */
      palette,
      /**
       * 折线圆点的**填充**色。
       *
       * 不能写死 #fff：卡片底色在暗色主题下是 #303030，
       * 白点会渲染成 13.2:1 的刺眼实心圆 —— 与「空心圆点」的设计意图正好相反。
       * 用卡片底色填充、线色描边，才能在两套主题下都保持空心观感。
       */
      pointFill: css('--color-fg', dark ? '#303030' : '#ffffff'),

      /** 词元构成三段（与 LogsView 的 .tk-in/.tk-out/.tk-cache 同源） */
      tokenInput: css('--text-terracotta', dark ? '#e59a80' : '#b15840'),
      tokenCache: css('--text-green', dark ? '#45c79a' : '#0b7d59'),
      tokenOutput: css('--text-purple', dark ? '#ab8ef2' : '#7c4ddb'),

      dark
    }
  })
}
