<script setup lang="ts">
import { computed } from 'vue'
import { groupStyle, groupVars } from '@/utils/groupStyle'

// 分组胶囊：全站所有显示分组名的地方都用它，颜色来自分组自己的配置
// （没配就按分组名派生，见 utils/groupStyle.ts）。
//
// 为什么不直接用 a-tag：a-tag 的颜色只能从 antd 的预设色板里挑，
// 而分组颜色是用户用取色器选的任意值；而且这里要保证「同一个分组在
// 渠道列表、密钥白名单、请求日志里是同一个颜色」。
const props = defineProps<{
  /** 展示的名字（模型名 / 密钥名 / 分组名都行） */
  name?: string | null
  /** 分组配的颜色；空表示按分组名自动派生 */
  color?: string | null
  /**
   * 颜色的来源名。留空时按 name 派生 —— 显示分组名时这就是对的；
   * 显示模型名/密钥名时**必须**传分组名，否则三个胶囊三种颜色。
   */
  colorFrom?: string | null
}>()

const style = computed(() => groupStyle(props.name, props.color, props.colorFrom))
const vars = computed(() => groupVars(style.value))
</script>

<template>
  <span class="group-tag" :class="{ 'is-custom': !style.auto }" :style="vars" :title="style.label">
    {{ style.label }}
  </span>
</template>

<style scoped>
.group-tag {
  /* 自动配色的明度：与模型胶囊同源（L=0.47 让各色相都能满足正文 4.5:1） */
  --gt-l: 0.47;
  /* 基色：自动模式用 oklch 拼出来，自定义模式在下面被覆盖成用户选的颜色 */
  --gt-base: oklch(var(--gt-l) var(--gt-c) var(--gt-h));

  display: inline-block;
  max-width: 100%;
  padding: 1px 8px;
  border-radius: var(--radius-control);
  border: 1px solid color-mix(in oklab, var(--gt-base) 32%, transparent);
  background: color-mix(in oklab, var(--gt-base) 13%, transparent);
  color: var(--gt-base);
  font-size: 12px;
  font-weight: 500;
  line-height: 20px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}

.group-tag.is-custom {
  --gt-base: var(--gt-color);
}

/* 深色主题：同一个色相在深底上必须提亮才够对比 */
:root[data-theme='dark'] .group-tag {
  --gt-l: 0.80;
}

/* 自定义色在深底上同理：直接沿用原色的话，深蓝/深紫几乎看不见背景边界。
   用 color-mix 往白里兑，保持色相不变。 */
:root[data-theme='dark'] .group-tag.is-custom {
  --gt-base: color-mix(in oklab, var(--gt-color) 62%, white);
}
</style>
