import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  DEFAULT_LOG_FX,
  LOG_FX_OPTIONS,
  readLogFx,
  writeLogFx,
  type LogFxId,
} from '@/utils/effects'

// 新日志入场动效的档位。
//
// 为什么这次用了 pinia（筛选偏好那边刻意没用，见 utils/persistedChoice.ts 的说明）：
// 那边只有「读一次、写一次」，而这里有两个页面同时关心同一个值 ——
// 设置页负责改、看板那一块负责按它换样式，而且要求**立刻生效**（不刷新、不重进页面）。
// 用 localStorage 当唯一的共享媒介做不到这件事：storage 事件只在**别的**标签页
// 触发，同一个页面里改了值，看板读不到。一个 store 直接解决。
//
// 读取时机：store 首次被用到时才建实例，此时 localStorage 一定可用（浏览器环境），
// 与 theme store 的写法一致。
export const useLogFxStore = defineStore('logFx', () => {
  const fx = ref<LogFxId>(readLogFx())

  /** 档位表：设置页渲染卡片、面板取提示文案都从这里来，不另抄一份 */
  const options = LOG_FX_OPTIONS

  function setFx(next: LogFxId) {
    // 先落盘再改状态：落盘只是「记住选择」，失败了也不该挡住本次切换
    writeLogFx(next)
    fx.value = next
  }

  /** 回到默认档（设置页的「恢复默认」入口） */
  function reset() {
    setFx(DEFAULT_LOG_FX)
  }

  return { fx, options, setFx, reset }
})
