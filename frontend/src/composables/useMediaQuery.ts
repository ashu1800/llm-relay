// 跟随一条媒体查询的布尔状态。
//
// 为什么不是纯 CSS：这些断点决定的是**组件状态**（侧栏是图标栏还是抽屉、
// 菜单要不要渲染文字、抽屉开没开），CSS 改不动它；两者混用还会打架。
//
// 抽成 composable 而不是各写一遍：媒体查询的样板（matchMedia + 首屏取值 +
// 监听 + 卸载清理）一共四步，最容易漏的是最后一步 —— 漏了就是组件卸载后
// 监听器还挂在 window 上，一直往一个死掉的 ref 里写值。
//
// 首屏值**同步**取（不在 onMounted 里取）：窄屏进来时不该先闪一下宽屏形态。
import { onUnmounted, ref, type Ref } from 'vue'

export function useMediaQuery(query: string): Ref<boolean> {
  const matches = ref(false)
  if (typeof window !== 'undefined' && window.matchMedia) {
    const mq = window.matchMedia(query)
    matches.value = mq.matches
    const onChange = (e: MediaQueryListEvent) => {
      matches.value = e.matches
    }
    mq.addEventListener('change', onChange)
    onUnmounted(() => mq.removeEventListener('change', onChange))
  }
  return matches
}
