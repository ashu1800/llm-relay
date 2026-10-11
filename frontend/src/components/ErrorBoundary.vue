<script setup lang="ts">
// 错误边界（2026-10-11）：任何一个视图在渲染期抛异常时，兜住它并给出可恢复的界面。
//
// ---- 为什么需要 ----
//
// 全仓此前没有任何 errorHandler / onErrorCaptured —— 组件渲染抛异常时 Vue 会
// 卸载整棵子树，用户看到的是一张**完全空白**的页面：没有提示、没有恢复入口，
// 只能自己猜要不要刷新（而刷新多半真的能恢复）。
// 空白页是最糟的失败模式：它和「后端挂了」「会话过期」「权限没了」长得一模一样，
// 用户据此得出的结论通常是错的。
//
// ---- 只兜渲染/生命周期错误 ----
//
// `onErrorCaptured` 收的是渲染函数、watcher、生命周期钩子里同步抛出的异常。
// 事件处理器里的异步失败（保存请求 500、轮询失败）不归它管 —— 那些由各自的
// catch 与 DataState 负责。把它们也兜进来会把「一次请求失败」放大成「整页错误」，
// 而用户正在看的表格本来还能用。
//
// ---- 为什么不只靠 app.config.errorHandler ----
//
// 那个全局钩子只够「记一笔 + 弹个提示」，它拿不到「把这块内容换成错误界面」的
// 能力 —— 页面仍然是空白的。两层一起用：这里负责**界面**，全局钩子负责
// 边界之外（App 自身的 setup、以及没被这个边界覆盖的组件）的兜底。
//
// 返回 false 阻止继续向上冒泡：否则同一个错误会被 app.config.errorHandler
// 再处理一次，用户先看到一个转瞬即逝的 message、再看到一张空白页。
import { onErrorCaptured, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ExclamationCircleOutlined } from '@ant-design/icons-vue'
import { writeClipboard } from '@/utils/clipboard'
import { message } from '@/utils/feedback'

const err = ref<Error | null>(null)
const info = ref('')
const copied = ref(false)

onErrorCaptured((e, _instance, i) => {
  err.value = e instanceof Error ? e : new Error(String(e))
  info.value = i
  // 控制台留全量（堆栈 + 出错阶段）：界面上只给人话，排查还是要靠控制台
  console.error('[ErrorBoundary] 视图渲染异常', e, i)
  return false
})

// 换页即复位：错误多半只属于刚坏掉的那一个视图，用户点侧栏去别的页面
// 应该能正常用 —— 卡在错误页上只能整页刷新就太粗暴了。
const route = useRoute()
watch(
  () => route.fullPath,
  () => {
    err.value = null
    info.value = ''
    copied.value = false
  }
)

function reload() {
  location.reload()
}

/** 复制完整错误信息：报障时它比一张截图有用得多（堆栈、阶段都在里面） */
async function copyDetail() {
  if (!err.value) return
  const text = `${err.value.name}: ${err.value.message}\n阶段: ${info.value}\n${err.value.stack || ''}`
  if (await writeClipboard(text)) {
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 2000)
  } else {
    message.warning('复制失败，请手动选择复制')
  }
}
</script>

<template>
  <div v-if="err" class="err-boundary" role="alert">
    <ExclamationCircleOutlined class="eb-icon" aria-hidden="true" />
    <h2 class="eb-title">这个页面出错了</h2>
    <p class="eb-text">
      页面在渲染时遇到了未预期的错误。刷新通常就能恢复；如果反复出现，
      请把下面的信息复制下来一起反馈。
    </p>
    <pre class="eb-detail" tabindex="0">{{ err.message }}<template v-if="info">（{{ info }}）</template></pre>
    <div class="eb-actions">
      <a-button type="primary" @click="reload">重新加载页面</a-button>
      <a-button @click="copyDetail">{{ copied ? '已复制' : '复制错误信息' }}</a-button>
      <router-link to="/console/dashboard" class="eb-link">回到数据看板</router-link>
    </div>
  </div>
  <!-- 正常路径：边界只是透传，不产生任何额外 DOM（不包一层 div：
       看板那页是 meta.fill 的弹性链，多一层盒子就会把它掐断） -->
  <slot v-else />
</template>

<style scoped>
/* 错误面板走全站面板的几何（--radius-panel + 1px 描边 + 投影），
   与「加载失败」的 DataState 面板同一观感 —— 两者都是「这里没内容，
   原因是坏的」，用户不该看到两种不同的失败长相。 */
.err-boundary {
  max-width: 640px;
  margin: 48px auto;
  padding: 24px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  text-align: center;
  background: var(--color-fg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  box-shadow: var(--color-fg-shadow);
}

/* 图标用 --text-red（正文级语义色，白底 5.44:1）；--color-red 只有 3.90:1，
   对图标（非文本，3:1 门槛）够用，但这一块整体是「需要读清的失败说明」，
   统一用可读的那一档更稳。 */
.eb-icon {
  font-size: var(--font-size-display);
  color: var(--text-red);
}

.eb-title {
  margin: 0;
  font-size: var(--font-size-display-sm);
  font-weight: 600;
  color: var(--color-text);
}

.eb-text {
  margin: 0;
  max-width: 460px;
  font-size: var(--font-size-md);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

/* 原始错误信息：等宽、可聚焦（键盘用户可以选中复制）、长行折行不撑破面板。
   它不是装饰 —— 用户报障时唯一能给出具体信息的地方。 */
.eb-detail {
  margin: 4px 0;
  padding: 8px 10px;
  max-width: 100%;
  overflow-x: auto;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  font-family: var(--font-family-mono);
  font-size: var(--font-size-sm);
  line-height: 1.5;
  color: var(--color-text);
  text-align: left;
  white-space: pre-wrap;
  word-break: break-word;
}

.eb-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: center;
  gap: var(--gap);
  margin-top: 4px;
}

.eb-link {
  font-size: var(--font-size-md);
}
</style>
