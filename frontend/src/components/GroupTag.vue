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
    <!-- 内层 inline 包一层：inline 盒不会被任何容器压缩（放不下就溢出），
         它的 rect 宽度恒等于文字真实宽度。请求日志的列宽测量靠这一层
         「看穿」外层的 max-width: 100% —— 外层是被列宽压扁的（压扁后
         rect = 可用宽，量它等于量当前列宽，长名字永远撑不开列）。
         对渲染零影响：inline 盒继承外层的字体与 nowrap，省略号仍由
         外层的 overflow + text-overflow 画在它自己的边缘。 -->
    <span class="gt-text">{{ style.label }}</span>
  </span>
</template>

<style scoped>
.group-tag {
  /* 自动配色的明度：与模型胶囊同源（浅色族 L=0.47、深色族 0.80，
     各自都能满足正文 4.5:1）。值来自主题令牌而不是写在这里 ——
     2026-09-30 多主题之前这段是「深色用一条 :root[data-theme='dark'] 覆盖」，
     多主题下那种写法要列 8 个深色 id，每加一套主题都要再来改一次。 */
  --gt-l: var(--tag-tint-l);
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
     于是明度约束只对自动配色生效，自定义色全部绕过 ——
     而自定义色恰恰是最不可控的一类：取色器给什么就是什么。
     实测（13% 自身做底，文字用原色）：
       #ffff00 1.05:1   #00ff00 1.27:1   #a0d911 1.57:1   #faad14 1.74:1
       #13c2c2 1.98:1   #52c41a 2.03:1   #fa8c16 2.13:1   #8c8c8c 2.95:1
       #eb2f96 3.25:1   #f5222d 3.34:1   #1677ff 3.47:1
     十三个预设里只有一个（#722ed1）达标，黄色与纯绿几乎和底色同亮 ——
     那不是「颜色浅」，是根本读不出来。

     压暗用 oklab 混黑而不是改 HSL 的 L：oklab 的 L 是感知均匀的，
     混黑后各色相的观感深度一致，色相也不会像 HSL 那样偏掉。
     混合比例与目标色由主题令牌给出（浅色族混黑 50%、深色族混白 45%）：
     0.50 时十三个预设最差值 5.27:1，0.45 时 4.37:1 仍有个别不达标，
     0.55 则过暗、分组之间失去区分度；深色族方向相反，混白 45% 最差 4.83:1。
     2026-09-30 之前这里是两条按 data-theme='dark' 分支的规则，
     与 GroupsView 里的预览各写一份比例 —— 现在已经合成同一个令牌，
     两处的 50% / 45% 不可能再各改一次。 */
  --gt-base: color-mix(in oklab, var(--gt-color) var(--tag-custom-mix-amount), var(--tag-custom-mix-color));
}
</style>
