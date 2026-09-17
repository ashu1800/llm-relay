<script setup lang="ts">
// 页面工具栏：对齐参考站 dashboard-toolbar（高 42px，白底，圆角 8，flex gap 8）
defineProps<{ label?: string }>()
</script>

<template>
  <section class="panel toolbar-panel">
    <span v-if="label" class="toolbar-label">{{ label }}</span>
    <slot />
    <div class="toolbar-spacer" />
    <slot name="right" />
  </section>
</template>

<style scoped>
.toolbar-panel {
  display: flex;
  /* 允许换行：看板工具栏的内容（标签 + 4 档时间 + 三个下拉 + 条件标签 +
     刷新）声明宽合计近千像素，窄屏不换行会溢出面板把内容区顶出横向滚动 */
  flex-wrap: wrap;
  align-items: center;
  gap: var(--gap);
  row-gap: 6px;
  /* 不许被压扁（站主 2026-09-17 反馈「浏览器窗口高度不高时纵向居中没居中，
     而是贴到了底部边缘」）。
     看板整页锁高：路由 meta.fill 让内容区成为高度确定的纵向容器
     （MainLayout 的 .content-inner.is-fill），本组件是看板 .dashboard 这个
     纵向弹性容器的子项，默认 flex-shrink: 1。窗口一矮它就被按比例压缩，
     而下面的 min-height 是 42px —— 比内容实际需要的 50px 还小
     （32px 控件 + 上下各 8px 内边距 + 上下各 1px 边框），
     于是内容比盒子高、只能朝下溢出，控件贴到面板底边。
     实测（内容盒基准，下留白为负即溢出）：视口高 1000 时下留白 -3.47，
     900 时 -6.84，850 起一直 -8（触到 min-height 下限）；
     1100px 宽那档换行后最差 -46。
     弹性预算该由下面的日志面板吃掉（它有 min-height: 0），
     工具栏永远按内容占高。
     回归脚本：.shots/verify-toolbar-center.mjs（修复前 75 项失败）。 */
  flex: 0 0 auto;
  /* 42px 是空内容时的下限（对齐参考站 dashboard-toolbar 的高度基准），
     不是本组件的实际高度 —— 单行实测 50px，窄屏换行后更高。
     注意它不能当兜底用：它比内容矮，正是上面那个 bug 的另一半原因。 */
  min-height: 42px;
}

.toolbar-label {
  font-size: 13px;
  color: var(--color-text-secondary);
}

.toolbar-spacer {
  flex: 1;
}
</style>
