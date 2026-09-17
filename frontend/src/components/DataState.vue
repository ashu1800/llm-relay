<script setup lang="ts">
// 统一的「加载中 / 加载失败 / 空」三态呈现。
//
// 之前各页面写法不一，问题最大的是 ChannelsView 和 ModelsView：
// 加载失败时只弹一个转瞬即逝的 message，表格随即显示 antd 的「暂无数据」——
// 用户会把失败读成「本来就没有数据」，然后去别处找原因。
//
// 这里区分两种失败，因为它们的正确处理方式不同：
//   - 首次加载就失败：没有任何数据可看，给一整块说明 + 重试按钮，
//     而不是显示一个空表格
//   - 已有数据后刷新失败：保留用户正在看的内容，只在顶部提示，
//     把表格清空反而是帮倒忙
withDefaults(
  defineProps<{
    // 非空即表示加载失败
    error?: string
    // 当前是否已经有数据可显示；用它区分上面两种失败
    hasData?: boolean
    // 首次加载、还没有任何数据时的占位
    loading?: boolean
    title?: string
    // 失败时给用户的下一步建议
    hint?: string
    // 加载成功但一条数据都没有。与「加载失败」是两回事，必须分开呈现 ——
    // 把失败显示成空列表，用户会去别处找原因。
    empty?: boolean
    // 空态文案，例如「还没有配置代理」
    emptyText?: string
  }>(),
  {
    error: '',
    hasData: false,
    loading: false,
    title: '加载失败',
    hint: '请确认后端服务是否正常，然后重试。',
    empty: false,
    emptyText: '暂无数据'
  }
)

const emit = defineEmits<{ retry: [] }>()
</script>

<template>
  <!-- 已有数据时刷新失败：保留表格，只在顶部说明。
       alert 与 slot 必须放进同一个 template 一起渲染 —— 之前 alert 是互斥链
       的链首（v-if）、slot 是链尾（v-else），alert 命中时 slot 必然不渲染，
       正在看的表格整块消失，与这条注释承诺的正好相反 -->
  <template v-if="error && hasData">
    <a-alert type="error" show-icon class="ds-alert" :message="error">
      <template #action>
        <!-- 用 a-button 而不是裸 <a>：没有 href 的 <a> 拿不到隐式 tabindex，
             Tab 键永远聚焦不到、回车也触发不了，键盘用户就卡在这一步。 -->
        <a-button type="link" size="small" @click="emit('retry')">重试</a-button>
      </template>
    </a-alert>
    <slot />
  </template>

  <!-- 首次加载失败：绝不能显示成空列表 -->
  <div v-else-if="error" class="ds-panel">
    <div class="ds-title">{{ title }}</div>
    <div class="ds-msg">{{ error }}</div>
    <div class="ds-hint">{{ hint }}</div>
    <a-button type="primary" @click="emit('retry')">重试</a-button>
  </div>

  <!-- 首次加载中：给一个明确的加载态，而不是先闪一下「暂无数据」 -->
  <div v-else-if="loading && !hasData" class="ds-panel">
    <a-spin />
    <div class="ds-hint">加载中…</div>
  </div>

  <!-- 加载成功但确实没有数据 -->
  <div v-else-if="empty && !loading" class="ds-panel">
    <a-empty :description="emptyText" />
  </div>

  <slot v-else />
</template>

<style scoped>
.ds-alert {
  margin: 0 var(--gap) var(--gap);
}
.ds-panel {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 56px var(--gap);
  text-align: center;
}
.ds-title {
  font-size: 15px;
  font-weight: 600;
  /* 标题是正文文字，用 --text-red（白底 5.44:1）而不是 --color-red。
     后者 #ea4343 在白底上只有 3.90:1、深色底上 3.38:1，都不达标 ——
     而这是「加载失败」的标题，恰恰是最需要一眼看清的一句。 */
  color: var(--text-red);
}
.ds-msg {
  max-width: 560px;
  /* 变量名是 --color-text，不存在 --color-text-primary。
     写错时 var() 解析失败会让整条声明失效，color 静默回退成继承值 ——
     看起来「差不多对」，所以这类错误很容易一直留着。 */
  color: var(--color-text);
  word-break: break-all;
}
.ds-hint {
  color: var(--color-text-secondary);
  font-size: 13px;
}
</style>
