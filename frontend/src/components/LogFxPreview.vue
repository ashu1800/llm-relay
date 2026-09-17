<script setup lang="ts">
// 设置页那一档动效的小样：60×30 的舞台里把三档效果各演一遍（循环播放），
// 让用户不用来回切换就能看出"哪一档是什么样子"。
//
// 为什么是自绘的迷你舞台而不是截图或真表格：真表格搬进来要一整套数据与列宽，
// 而这里只需要表达"亮带怎么走 / 行怎么滑进来 / 光晕怎么呼吸"。迷你舞台用的是
// 与真效果同一套画法（同一组彩虹色标、同一个 --ease-expo、同一个主色），
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

    <!-- 自上滑入：第一行每隔一轮从上方滑进来一次。纯 CSS，靠舞台上的
         data-fx 选中它（不在这里写 v-if，好让三档在模板里长一个样子） -->

    <!-- 光晕脉动：第一行泛起一层主色光 -->
    <span v-if="fx === 'glow'" class="fxp-halo" />
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

/* ---- 自上滑入 / 光晕脉动 ----
   两条规则都指向同一个元素（`.fxp-row-1`），靠舞台上的 data-fx 分开 ——
   写两条同优先级的 animation 会互相覆盖，谁是后写的谁生效，
   而"后写的"取决于样式表里的顺序，这种 bug 只在切档时才看得出来。 */
.fxp[data-fx='slide'] .fxp-row-1 { animation: fxp-slide 2.2s var(--ease-expo) infinite; }
@keyframes fxp-slide {
  0%, 8% { opacity: 0; transform: translateY(-7px); }
  32%, 100% { opacity: 1; transform: translateY(0); }
}

/* 光晕那两下呼吸：整行泛起一层主色底 + 底下一道光带，与真效果的
   fx-glow-cell + .fx-glow 是同一套画法，只是缩到 5px 的行高上。
   呼吸两下（第一次强、第二次弱）也照着真效果的节奏来。 */
.fxp[data-fx='glow'] .fxp-row-1 { animation: fxp-glow-row 2s ease-in-out infinite; }
@keyframes fxp-glow-row {
  0% { background: var(--color-border); }
  16% { background: color-mix(in srgb, var(--color-primary) 65%, var(--color-bg)); }
  60% { background: color-mix(in srgb, var(--color-primary) 32%, var(--color-bg)); }
  100% { background: var(--color-border); }
}

.fxp-halo {
  position: absolute;
  left: 6px;
  right: 6px;
  top: 21px;
  height: 3px;
  border-radius: 2px;
  background: var(--color-primary);
  opacity: 0;
  animation: fxp-glow-band 2s ease-in-out infinite;
}
@keyframes fxp-glow-band {
  0% { opacity: 0; }
  16% { opacity: 0.95; }
  34% { opacity: 0.3; }
  50% { opacity: 0.8; }
  74% { opacity: 0.2; }
  100% { opacity: 0; }
}
</style>
