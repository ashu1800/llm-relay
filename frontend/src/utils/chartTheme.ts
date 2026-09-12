// 图表配色统一从 CSS 变量取，不在 option 里写死颜色。
//
// 为什么必须这样：echarts 不认 CSS 变量，颜色只能由 JS 传进 option。
// 一旦写死成浅色主题的值，切到暗色就成了「深灰字压深色底」——
// 图例最容易漏，因为不设 textStyle.color 时 echarts 用的是自带默认色 #333，
// 在暗色背景上等于隐形；坐标轴标签同理。
//
// 变量本身已由 stores/theme.ts 在切换主题时同步写好（apply 里 setProperty），
// 所以这里读到的就是当前主题的值。颜色仍然只在 theme.ts 定义一处，
// 图表跟着走，不需要再维护第二份色表。
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
      dark
    }
  })
}
