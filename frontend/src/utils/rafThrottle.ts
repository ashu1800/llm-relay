/**
 * rAF 节流器（2026-09-27 性能收口，从 RequestLogPanel.vue 抽出）：
 * scroll / resize 事件的触发频率高于帧率（Windows 上鼠标滚轮一格能连发
 * 十几个 scroll），而经它节流的处理全是「读几何」—— 逐事件跑等于每次
 * 强制布局，滚动一快就掉帧。合并到每帧最多一次，且 rAF 回调跑在本帧
 * paint 之前：处理结果仍然与滚动同步呈现，只是不再为中间态白算。
 * cancel 供卸载时摘除挂起的帧。
 */
export function rafThrottle(fn: () => void): (() => void) & { cancel: () => void } {
  let raf = 0
  const schedule = () => {
    if (!raf)
      raf = requestAnimationFrame(() => {
        raf = 0
        fn()
      })
  }
  schedule.cancel = () => {
    if (raf) cancelAnimationFrame(raf)
    raf = 0
  }
  return schedule
}
