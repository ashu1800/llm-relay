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
  /* 自定义色也要压到同一条明度线上。
     原来这里是 --gt-base: var(--gt-color) 直接沿用用户选的原色，
     于是 L=0.47 这条约束只对自动配色生效，自定义色全部绕过 ——
     而自定义色恰恰是最不可控的一类：取色器给什么就是什么。
     实测（13% 自身做底，文字用原色）：
       #ffff00 1.05:1   #00ff00 1.27:1   #a0d911 1.57:1   #faad14 1.74:1
       #13c2c2 1.98:1   #52c41a 2.03:1   #fa8c16 2.13:1   #8c8c8c 2.95:1
       #eb2f96 3.25:1   #f5222d 3.34:1   #1677ff 3.47:1
     十三个预设里只有一个（#722ed1）达标，黄色与纯绿几乎和底色同亮 ——
     那不是「颜色浅」，是根本读不出来。

     压暗用 oklab 混黑而不是改 HSL 的 L：oklab 的 L 是感知均匀的，
     混黑后各色相的观感深度一致，色相也不会像 HSL 那样偏掉。
     取 50% 是量出来的：0.50 时十三个预设最差值 5.27:1，
     0.45 时 4.37:1 仍有个别不达标，0.55 则过暗、分组之间失去区分度。 */
  --gt-base: color-mix(in oklab, var(--gt-color) 50%, black);
}

/* 深色主题：同一个色相在深底上必须提亮才够对比 */
:root[data-theme='dark'] .group-tag {
  --gt-l: 0.80;
}

/* 自定义色在深底上同理：直接沿用原色的话，深蓝/深紫几乎看不见背景边界。
   同样用 oklab 混白保持感知均匀：混白 55% 时十三个预设最差值 4.83:1。
   注意方向与浅色主题相反 —— 浅色底要压暗，深色底要提亮。 */
:root[data-theme='dark'] .group-tag.is-custom {
  --gt-base: color-mix(in oklab, var(--gt-color) 45%, white);
}
</style>
