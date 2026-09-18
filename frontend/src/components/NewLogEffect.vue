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
// 真正的效果由这一层按量好的位置画出来。三档现在**全部**走这一层 ——
// 2026-09-18 换血时把最后两档依赖行内动画的（slide/glow）一起送走了，
// 「单元格上跑动画」从此不再是任何一档的实现方式。
//
// 三档各自的机制：
//   sweep    彩虹扫光（默认）—— 沿新行下沿是一条绝对定位亮带，动 background-position-x。
//   pulse    双星对撞 —— 行中央迸出一个亮点，两道光沿行底边同时奔向左右两端，
//            到端点各闪一下熄灭。「一个请求，分发两端」，正是这个站干的事。
//   stardust 星尘上浮 —— 七粒星尘从新行错落升起、上浮飘散。
//
// 星尘的位置带随机数（粒子落在行内哪个 x、飘多快）：每次新日志到达都会生成
// 一批新的粒子参数，所以同一行两次入场飘法不一样 —— 「每次都一样」是机械感
// 的主要来源，宁可多写两行也不要它。
import { computed } from 'vue'
import type { LogFxId } from '@/utils/effects'

/** 一条新行的位置：由面板量好传下来（以 .log-table 的盒子为原点的相对坐标） */
export type FxTarget = { key: number; top: number; left: number; width: number }

const props = defineProps<{
  /** 当前档位 */
  mode: LogFxId
  /** 正在做入场动画的那些行（三档共用这一份位置数据） */
  targets: FxTarget[]
}>()

// ---- 双星对撞的元素清单 ----
// 每个 target 拆成 5 个零件：中央亮点、左/右两道光带、左/右两端最后的闪光。
const pulseParts = computed(() => {
  if (props.mode !== 'pulse') return []
  return props.targets.flatMap((t) => {
    const mid = t.left + t.width / 2
    return [
      { key: 'p' + t.key, cls: 'fx-pulse-dot', style: { top: t.top + 'px', left: mid + 'px' } },
      { key: 'pl' + t.key, cls: 'fx-pulse-beam is-left', style: { top: t.top + 'px', left: t.left + 'px', width: t.width / 2 + 'px' } },
      { key: 'pr' + t.key, cls: 'fx-pulse-beam is-right', style: { top: t.top + 'px', left: mid + 'px', width: t.width / 2 + 'px' } },
      { key: 'sl' + t.key, cls: 'fx-pulse-spark is-left', style: { top: t.top + 'px', left: t.left + 3 + 'px' } },
      { key: 'sr' + t.key, cls: 'fx-pulse-spark is-right', style: { top: t.top + 'px', left: t.left + t.width - 9 + 'px' } },
    ]
  })
})

// ---- 星尘上浮的元素清单 ----
// 7 粒；起点 x 在行内取伪随机位置，升速与延迟也各自错开。
// 随机性只存在于元素生成的那一刻（内联样式写死）：不追求真随机，
// 只追求「两次入场不一样」。
const STAR_COLORS = ['var(--color-primary)', '#ffa940', '#36cfc9']
const stardust = computed(() => {
  if (props.mode !== 'stardust') return []
  return props.targets.flatMap((t) =>
    Array.from({ length: 7 }, (_, i) => {
      const left = t.left + t.width * (0.06 + 0.88 * Math.random())
      const dur = 0.9 + Math.random() * 0.4
      const delay = Math.random() * 0.28
      const rise = 22 + Math.random() * 12
      return {
        key: 's' + t.key + '-' + i,
        style: {
          top: t.top + 4 + 'px',
          left: left.toFixed(1) + 'px',
          background: STAR_COLORS[i % STAR_COLORS.length],
          animationDuration: dur.toFixed(2) + 's',
          animationDelay: delay.toFixed(2) + 's',
          '--star-rise': rise.toFixed(0) + 'px',
        },
      }
    }),
  )
})
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

    <!-- 双星对撞：中央亮点 + 两道对奔的光带 + 两端收尾的闪光 -->
    <span
      v-for="p in pulseParts"
      :key="p.key"
      class="fx-pulse"
      :class="p.cls"
      :style="p.style"
    />

    <!-- 星尘上浮：七粒小星错落升起 -->
    <span
      v-for="s in stardust"
      :key="s.key"
      class="fx-star"
      :style="s.style"
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

/* ---------- 双星对撞（档位：pulse） ----------
   三个阶段写在一个 0.95s 的循环里：
   0-0.15s 中央亮点迸出（scale 0 -> 1.6）；
   0.1-0.55s 两道光带从中央向两端展开（scaleX 0 -> 1）；
   0.5-0.95s 光带渐隐、两端各闪一下火花后全部熄灭。
   光带只有 3px 高、贴着行底分割线（与扫光同一个着力点），
   所以它读起来是「这一行在发信号」，而不是「这一行被盖住了」。 */
.fx-pulse {
  position: absolute;
  height: 3px;
  z-index: 3;
  pointer-events: none;
}
.fx-pulse-dot {
  width: 7px;
  height: 7px;
  margin-top: -2px;
  margin-left: -3.5px;
  border-radius: 50%;
  background: radial-gradient(circle, #fff 0% 30%, var(--color-primary) 62%, transparent 100%);
  transform: scale(0);
  animation: fx-pulse-dot 0.95s ease-out forwards;
}
@keyframes fx-pulse-dot {
  0% { transform: scale(0); opacity: 0; }
  16% { transform: scale(1.6); opacity: 1; }
  34% { transform: scale(0.9); opacity: 0.95; }
  60%, 100% { transform: scale(0); opacity: 0; }
}
.fx-pulse-beam {
  background-image: linear-gradient(90deg, transparent 0%, var(--color-primary) 92%, #fff 100%);
  transform: scaleX(0);
  animation: fx-pulse-beam 0.95s cubic-bezier(0.22, 1, 0.36, 1) forwards;
}
.fx-pulse-beam.is-left {
  transform-origin: right center;
  background-image: linear-gradient(90deg, #fff 0%, var(--color-primary) 8%, transparent 100%);
}
.fx-pulse-beam.is-right {
  transform-origin: left center;
}
@keyframes fx-pulse-beam {
  0%, 10% { transform: scaleX(0); opacity: 0; }
  12% { opacity: 1; }
  58% { transform: scaleX(1); opacity: 0.95; }
  82% { opacity: 0.4; }
  100% { transform: scaleX(1); opacity: 0; }
}
/* 端点火花：光到达时才亮，一小圈就灭 */
.fx-pulse-spark {
  width: 6px;
  height: 6px;
  margin-top: -1.5px;
  border-radius: 50%;
  background: radial-gradient(circle, #fff 0% 40%, var(--color-primary) 70%, transparent 100%);
  transform: scale(0);
  opacity: 0;
  animation: fx-pulse-spark 0.95s ease-out forwards;
}
@keyframes fx-pulse-spark {
  0%, 48% { transform: scale(0); opacity: 0; }
  60% { transform: scale(1.5); opacity: 1; }
  85%, 100% { transform: scale(0); opacity: 0; }
}

/* ---------- 星尘上浮（档位：stardust） ----------
   七粒小星从行内升起、上浮、缩没。颜色三选一轮换（主色/琥珀/青），
   与扫光的彩虹同一家族；上升距离由每粒自己的 --star-rise 给（内联注入），
   所以同一次入场的七粒也不是整齐划一的。 */
.fx-star {
  position: absolute;
  width: 4px;
  height: 4px;
  border-radius: 50%;
  z-index: 3;
  pointer-events: none;
  opacity: 0;
  box-shadow: 0 0 5px color-mix(in srgb, currentColor 60%, transparent);
  animation: fx-star-rise 1.1s ease-out forwards;
}
@keyframes fx-star-rise {
  0% { opacity: 0; transform: translateY(4px) scale(0.6); }
  22% { opacity: 0.95; }
  100% {
    opacity: 0;
    transform: translateY(calc(-1 * var(--star-rise, 26px))) scale(0.35);
  }
}
</style>
