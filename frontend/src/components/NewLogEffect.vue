<script setup lang="ts">
// 请求日志的「新日志入场动效」图层 —— 三档效果都画在这里，表格内部一个字节都不动。
//
// 为什么全部画在表格**外面**这一层（.log-table 里、a-table 之外）：
// 往 tr 里塞任何非单元格子元素（哪怕只是一个 ::after 伪元素）都会让 Chrome 在
// `table-layout: fixed` 下不再把多余的宽度分给各列 —— 整张表从「铺满容器」塌回
// 声明宽度，而表头是另一张表、照旧铺满，于是右侧空出一条。这是踩过的坑，
// 实测记录与隔离实验见 RequestLogPanel 里 fxTargets 那段注释与 docs/ui-spec 第 11 条；
// frontend/scripts/check-contracts.mjs 也钉着不许再挂回去。
// 所以：is-new 这个类**不带任何样式**，只是「这一行正在做入场动画」的标记，
// 真正的效果由这一层按量好的位置画出来。
//
// 三档各自的机制：
//   sweep 彩虹扫光（默认）—— 沿新行下沿是一条绝对定位亮带，动 background-position-x。
//         原来在 RequestLogPanel 里，2026-09-17 搬到这里，行为一个字节没改。
//   slide 自上滑入 —— 纯 CSS：新行的每个单元格 translateY(-8px) + 淡入。
//   glow  光晕脉动 —— 这一层里的一道光带贴在新行上，呼吸两下后褪去。
//
// 为什么 slide 敢动单元格的 transform，而扫光当初连伪元素都不敢往 tr 里放：
// 那一次的问题是「tr 里多了一个非单元格子元素」改变了列宽分配，而 transform 是
// 绘制期属性、不参与布局，单元格加它不会影响 table-layout 的列宽计算。固定列
// （position: sticky）本身已经构成包含块，也不会因为多一个 transform 改变吸附基准。
// 这条结论由 .shots/verify-log-effects.mjs 的 layout 模式逐项复测（列宽 / 行高 /
// 表体高 / 页面横溢出全部与切换前一致）。
import { computed } from 'vue'
import type { LogFxId } from '@/utils/effects'

/** 一条新行的位置：由面板量好传下来（以 .log-table 的盒子为原点的相对坐标） */
export type FxTarget = { key: number; top: number; left: number; width: number }

const props = defineProps<{
  /** 当前档位 */
  mode: LogFxId
  /** 正在做入场动画的那些行（扫光与光晕共用这一份位置数据） */
  targets: FxTarget[]
}>()

/** 光晕那一道光带：贴在新行上（顶边压进分割线 2px，与扫光同一手法） */
const GLOW_H = 3

const glowBands = computed(() =>
  props.mode !== 'glow'
    ? []
    : props.targets.map((t) => ({
        // key 加前缀：光晕带与扫光亮带指向同一行，两个 v-for 是兄弟节点，
        // 不加前缀 Vue 会认为它们在同一组里有两个同 key 的兄弟
        key: 'g' + t.key,
        top: t.top - GLOW_H,
        left: t.left,
        width: t.width,
      })),
)
</script>

<template>
  <!-- 这一层本身不吃事件：它铺在表格上面，任何一处 pointer-events 漏出去都会
       让表头或某一行点不动。
       data-targets 是给回归脚本读的状态（.shots/verify-log-effects.mjs）：
       「现在有几行正在做入场动画」没法从静态样式里看出来。 -->
  <div class="fx-layer" :data-targets="targets.length">
    <!-- 彩虹扫光：位置由面板量出来（top/left/width 都取自那一行的 getBoundingClientRect），
         所以它天生等于「整行宽 + 贴行底边」。
         用 v-if 而不是 v-show：v-show 留下的是 display: none 的元素，
         它会被「这一档有没有画亮带」这类检查数进去（实测踩过）。 -->
    <span
      v-for="b in mode === 'sweep' ? targets : []"
      :key="b.key"
      class="log-sweep"
      :style="{ top: b.top + 'px', left: b.left + 'px', width: b.width + 'px' }"
    />

    <!-- 光晕脉动：整行宽度的一道光带，呼吸两下 -->
    <span
      v-for="g in glowBands"
      :key="g.key"
      class="fx-glow"
      :style="{ top: g.top + 'px', left: g.left + 'px', width: g.width + 'px' }"
    />
  </div>
</template>

<style scoped>
.fx-layer {
  position: absolute;
  inset: 0;
  pointer-events: none;
  z-index: 3;
}

/* ---------- 彩虹扫光（档位：sweep，默认） ----------
   这一段与它原来在 RequestLogPanel 里的样子逐字一致（只多了一条 v-show）。
   时机、位移、配色、时长一个字都没改，理由见注释与 docs/ui-spec 第 11 条。 */
.log-sweep {
  position: absolute;
  /* 3px：分割线本身是单元格的 1px 下边框（border-collapse 为 separate 时它算在
     行高内），亮带盖住它再往上压 2px，看起来才是一道光扫过去而不是一条细线 */
  height: 3px;
  /* 压过固定列的 sticky 单元格（它们是 position: sticky + 不透明底、z-index: 2） */
  z-index: 3;
  pointer-events: none;
  background-image: linear-gradient(
    90deg,
    rgba(255, 77, 79, 0) 0%,
    rgba(255, 77, 79, 0.95) 12%,
    #ffa940 28%,
    #ffec3d 42%,
    #52c41a 56%,
    #36cfc9 68%,
    #2f54eb 82%,
    rgba(114, 46, 209, 0.95) 92%,
    rgba(114, 46, 209, 0) 100%
  );
  background-repeat: no-repeat;
  /* 带子占整行的 30%：位移的百分比是相对 (行宽 - 带宽) 算的，
     所以 -50% / 150% 对应左缘落在 -35% / 105% 处 —— 两端都在行外，
     起手看不见、收尾也在行外消失 */
  background-size: 30% 100%;
  background-position-x: -50%;
  /* 匀速，并且**不要**换成 --ease-expo：那个曲线 0.3 秒就走完了全程，
     剩下的时间停在右端不动，观感是「闪一下」而不是「扫过 2 秒」。
     这里的时长本身就是需求（约 2s），不是「快点响应」那类过渡。 */
  animation: row-sweep 2s linear forwards;
}

@keyframes row-sweep {
  from {
    background-position-x: -50%;
  }
  to {
    background-position-x: 150%;
  }
}

/* ---------- 光晕脉动（档位：glow） ----------
   一道贴在新行上的光带，用不透明度做两次呼吸。
   为什么是"一道带子"而不是给整行铺一层底：单元格有各自的背景，行本身没有
   一个可以被着色的盒子（同扫光的理由）。带宽 = 行宽、高 3px 是刻意的 ——
   扫光的高度也是 3px，两档的"着力点"因此落在同一条分割线上。 */
.fx-glow {
  position: absolute;
  height: 3px;
  z-index: 3;
  pointer-events: none;
  background-image: linear-gradient(
    90deg,
    transparent 0%,
    color-mix(in srgb, var(--color-primary) 70%, transparent) 18%,
    color-mix(in srgb, var(--color-primary) 95%, transparent) 50%,
    color-mix(in srgb, var(--color-primary) 70%, transparent) 82%,
    transparent 100%
  );
  /* 呼吸两下：两次起伏之间不停顿，第二次比第一次弱一点，收尾是渐隐不是硬切 */
  animation: fx-glow 1.6s ease-in-out 1;
}

@keyframes fx-glow {
  0% { opacity: 0; }
  14% { opacity: 0.95; }
  32% { opacity: 0.28; }
  50% { opacity: 0.8; }
  72% { opacity: 0.2; }
  100% { opacity: 0; }
}

/* ---------- 自上滑入 / 光晕脉动的行内部分（纯 CSS，靠面板挂在 .log-table 上的 data-fx） ----------
   这里 MUST 用 :global(...) 把整条选择器包起来，不能写成
   `:global(.log-table[data-fx='slide']) :deep(tr.is-new > td)`。

   为什么：scoped 编译对 ":global(A) :deep(B)" 这种组合会**丢掉选择器的后半截**
   （实测编译产物就是 `.log-table[data-fx="slide"]`），于是规则落到 tr 上而不是
   它里面的单元格上 —— 而 transform / background-image 挂在 tr 上基本看不出效果，
   整条动画静默失效、不报任何错。包成 :global(...) 之后是按原样输出的，
   而且这里本来就没有"本组件的元素"要锚定：.log-table 是父组件的根节点，
   tr/td 由 antd 渲染，两边都不吃 scope 属性。

   为什么这两条是安全的（当初扫光连伪元素都不敢往 tr 里放）：那一次的问题是
   「tr 里多了一个非单元格子元素」，改变了 table-layout: fixed 的列宽分配；
   而 transform 与 background-image 都是绘制期属性，不参与布局，也不会改变
   固定列（position: sticky）的吸附基准。这条由 .shots/verify-log-effects.mjs
   的 layout 模式逐项复测（列宽 / 行高 / 表体高 / 页面横溢出三档一致）。 */
:global(.log-table[data-fx='slide'] tr.is-new > td) {
  animation: fx-slide-in 0.45s var(--ease-expo) both;
}

@keyframes fx-slide-in {
  from {
    opacity: 0;
    transform: translateY(-8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

/* 光晕那两下呼吸：整行的单元格各加一层很淡的主色底。
   放在单元格上而不是行上，理由同扫光 —— 行没有可着色的盒子。
   它只是"底噪"，所以 alpha 很低（0.13 上下），要的是"整行泛起一层光"，
   不是把行染成主色。 */
:global(.log-table[data-fx='glow'] tr.is-new > td) {
  animation: fx-glow-cell 1.6s ease-in-out 1;
}

@keyframes fx-glow-cell {
  0% { background-image: none; }
  16% {
    background-image: linear-gradient(
      color-mix(in srgb, var(--color-primary) 13%, transparent),
      color-mix(in srgb, var(--color-primary) 13%, transparent)
    );
  }
  60% {
    background-image: linear-gradient(
      color-mix(in srgb, var(--color-primary) 6%, transparent),
      color-mix(in srgb, var(--color-primary) 6%, transparent)
    );
  }
  100% { background-image: none; }
}
</style>
