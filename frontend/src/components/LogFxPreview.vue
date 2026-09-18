<script setup lang="ts">
// 设置页那一档动效的小样：60×30 的舞台里把三档效果各演一遍（循环播放），
// 让用户不用来回切换就能看出"哪一档是什么样子"。
//
// 为什么是自绘的迷你舞台而不是截图或真表格：真表格搬进来要一整套数据与列宽，
// 而这里只需要表达"亮带怎么走 / 光怎么对奔 / 星尘怎么升"。迷你舞台用的是
// 与真效果同一套画法（同一组彩虹色标、同一个主色、同一套节奏），
// 所以小样与真身不会说两套话。
//
// 纯装饰：对读屏器隐藏（外面那张卡片的名字与描述才是可读的信息）。
import type { LogFxId } from '@/utils/effects'

defineProps<{ fx: LogFxId }>()
</script>

<template>
  <span class="fxp" :data-fx="fx" aria-hidden="true">
    <!-- 舞台上的两条"行" -->
    <span class="fxp-row fxp-row-0" />
    <span class="fxp-row fxp-row-1" />

    <!-- 彩虹扫光：一道亮带沿第一行下沿扫过。色标与真效果逐字一致 -->
    <span v-if="fx === 'sweep'" class="fxp-band" />

    <!-- 双星对撞：中央亮点 + 两道对奔的光（循环节奏与真效果同源） -->
    <template v-if="fx === 'pulse'">
      <span class="fxp-pulse-dot" />
      <span class="fxp-pulse-beam is-left" />
      <span class="fxp-pulse-beam is-right" />
    </template>

    <!-- 星尘上浮：三粒小星错落升起（真效果是七粒，小样放三粒意思到了） -->
    <template v-if="fx === 'stardust'">
      <span class="fxp-star" style="left: 14px; animation-delay: 0s" />
      <span class="fxp-star" style="left: 29px; animation-delay: 0.3s" />
      <span class="fxp-star" style="left: 43px; animation-delay: 0.55s" />
    </template>
  </span>
</template>

<style scoped>
.fxp {
  position: relative;
  display: block;
  width: 60px;
  height: 30px;
  border-radius: 5px;
  overflow: hidden;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
}
.fxp-row {
  position: absolute;
  left: 6px;
  right: 6px;
  height: 5px;
  border-radius: 2px;
  background: var(--color-border);
}
/* 两条基线错开一点高度，看起来才像"一列行"而不是两根横杠 */
.fxp-row-0 { top: 7px; }
.fxp-row-1 { top: 17px; }

/* ---- 彩虹扫光 ----
   与真效果的色标、带宽比例（30%）一致，只是把 2s 压到 1.8s 免得小样看着发木 */
.fxp-band {
  position: absolute;
  left: 6px;
  right: 6px;
  top: 21px;
  height: 3px;
  border-radius: 2px;
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
  background-size: 30% 100%;
  animation: fxp-sweep 1.8s linear infinite;
}
@keyframes fxp-sweep {
  0% { background-position-x: -50%; }
  100% { background-position-x: 150%; }
}

/* ---- 双星对撞 ----
   中线在行底（top: 21px），亮点迸出后两道光向两端跑，收在端点。
   2.2s 一轮，前 40% 演完、后面留白喘口气 —— 循环预览需要呼吸感。 */
.fxp-pulse-dot {
  position: absolute;
  left: 50%;
  top: 21px;
  width: 7px;
  height: 7px;
  margin: -2px 0 0 -3.5px;
  border-radius: 50%;
  background: radial-gradient(circle, #fff 0% 30%, var(--color-primary) 62%, transparent 100%);
  transform: scale(0);
  animation: fxp-pulse-dot 2.2s ease-out infinite;
}
@keyframes fxp-pulse-dot {
  0% { transform: scale(0); opacity: 0; }
  4% { transform: scale(1.6); opacity: 1; }
  9% { transform: scale(0.9); opacity: 0.95; }
  16%, 100% { transform: scale(0); opacity: 0; }
}
.fxp-pulse-beam {
  position: absolute;
  top: 21px;
  height: 3px;
  background-image: linear-gradient(90deg, transparent 0%, var(--color-primary) 92%, #fff 100%);
  transform: scaleX(0);
  animation: fxp-pulse-beam 2.2s cubic-bezier(0.22, 1, 0.36, 1) infinite;
}
.fxp-pulse-beam.is-left {
  left: 6px;
  right: 50%;
  transform-origin: right center;
  background-image: linear-gradient(90deg, #fff 0%, var(--color-primary) 8%, transparent 100%);
}
.fxp-pulse-beam.is-right {
  left: 50%;
  right: 6px;
  transform-origin: left center;
}
@keyframes fxp-pulse-beam {
  0%, 3% { transform: scaleX(0); opacity: 0; }
  5% { opacity: 1; }
  14% { transform: scaleX(1); opacity: 0.95; }
  20% { opacity: 0.4; }
  24%, 100% { transform: scaleX(1); opacity: 0; }
}

/* ---- 星尘上浮 ----
   三粒小星从第一行升起，节奏各自错开（真效果的随机性在循环预览里
   用固定的 delay 表达）。颜色轮换与真效果同源。 */
.fxp-star {
  position: absolute;
  top: 19px;
  width: 4px;
  height: 4px;
  border-radius: 50%;
  opacity: 0;
  animation: fxp-star-rise 2.2s ease-out infinite;
}
.fxp-star:nth-of-type(odd) { background: var(--color-primary); }
.fxp-star:nth-of-type(even) { background: #36cfc9; }
@keyframes fxp-star-rise {
  0% { opacity: 0; transform: translateY(3px) scale(0.6); }
  10% { opacity: 0.95; }
  26% { opacity: 0; transform: translateY(-13px) scale(0.35); }
  100% { opacity: 0; transform: translateY(-13px) scale(0.35); }
}
</style>
