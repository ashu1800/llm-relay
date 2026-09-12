<script setup lang="ts">
// 按需引入 ECharts，避免把完整包打进产物（完整包约 1MB）
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import * as echarts from 'echarts/core'
import { BarChart, HeatmapChart, LineChart, PieChart } from 'echarts/charts'
import {
  GridComponent,
  LegendComponent,
  TooltipComponent,
  VisualMapComponent
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([
  LineChart,
  BarChart,
  PieChart,
  HeatmapChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  VisualMapComponent,
  CanvasRenderer
])

const props = withDefaults(defineProps<{ option: Record<string, unknown>; height?: string }>(), {
  height: '260px'
})

const el = ref<HTMLDivElement | null>(null)
const chart = shallowRef<ReturnType<typeof echarts.init> | null>(null)
let observer: ResizeObserver | null = null

function render() {
  if (!el.value) return
  if (!chart.value) chart.value = echarts.init(el.value)
  chart.value.setOption(props.option, true)
}

// 尺寸变化只需重排，不必重跑 setOption
function resize() {
  chart.value?.resize()
}

onMounted(() => {
  render()
  window.addEventListener('resize', resize)
  // 侧边栏折叠不会触发 window.resize，必须监听容器自身尺寸变化
  if (typeof ResizeObserver !== 'undefined' && el.value) {
    observer = new ResizeObserver(resize)
    observer.observe(el.value)
  }
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resize)
  if (observer) observer.disconnect()
  if (chart.value) chart.value.dispose()
  chart.value = null
})

watch(() => props.option, render, { deep: true })
</script>

<template>
  <div ref="el" class="echart" :style="{ height }" />
</template>

<style scoped>
.echart {
  width: 100%;
}
</style>
