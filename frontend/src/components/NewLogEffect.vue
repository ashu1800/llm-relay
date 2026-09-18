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
// 火红是站主点名的主题色：与扫光的彩虹不同，这里的色标刻意写死
// 而不取 --color-primary（陶土色偏暗，对撞的「火焰感」需要饱和的暖红）。
const EMBER_COLORS = ['#ff3d1f', '#ff6a3d', '#ffa940', '#ffb199']
// 每个 target 拆成两批零件：
//   对撞本体 5 个：中央亮点、左/右两道光带、左/右两端最后的闪光；
//   溅射火星 10 粒：亮点迸出的瞬间从中心向随机方向飞散（方向与距离
//   内联写死在生成那一刻，同一次入场的十粒也不整齐）。
const pulseParts = computed(() => {
  if (props.mode !== 'pulse') return []
  return props.targets.flatMap((t) => {
    const mid = t.left + t.width / 2
    const core = [
      { key: 'p' + t.key, cls: 'fx-pulse-dot', style: { top: t.top + 'px', left: mid + 'px' } },
      { key: 'pl' + t.key, cls: 'fx-pulse-beam is-left', style: { top: t.top + 'px', left: t.left + 'px', width: t.width / 2 + 'px' } },
      { key: 'pr' + t.key, cls: 'fx-pulse-beam is-right', style: { top: t.top + 'px', left: mid + 'px', width: t.width / 2 + 'px' } },
      { key: 'sl' + t.key, cls: 'fx-pulse-spark is-left', style: { top: t.top + 'px', left: t.left + 3 + 'px' } },
      { key: 'sr' + t.key, cls: 'fx-pulse-spark is-right', style: { top: t.top + 'px', left: t.left + t.width - 9 + 'px' } },
    ]
    // 溅射火星：从中心向两侧上方飞散（dy 向上、dx 一左一右），
    // 颜色在火红的深浅里轮换 —— 它们是「迸出的火星」，不是背景噪音
    const embers = Array.from({ length: 10 }, (_, i) => {
      const dx = (i % 2 === 0 ? -1 : 1) * (14 + Math.random() * 42)
      const dy = -(6 + Math.random() * 20)
      const size = 3 + Math.random() * 3
      return {
        key: 'e' + t.key + '-' + i,
        cls: 'fx-pulse-ember',
        style: {
          top: t.top + 'px',
          left: mid + 'px',
          width: size.toFixed(1) + 'px',
          height: size.toFixed(1) + 'px',
          background: EMBER_COLORS[i % EMBER_COLORS.length],
          animationDelay: (Math.random() * 0.1).toFixed(2) + 's',
          '--ember-dx': dx.toFixed(0) + 'px',
          '--ember-dy': dy.toFixed(0) + 'px',
        },
      }
    })
    return core.concat(embers)
  })
})

// ---- 星尘上浮的元素清单 ----
// 14 粒（初版 7 粒被站主判为「基本看不到」）：起点 x 在行内取伪随机
// 位置，升速与延迟各自错开。颜色同时写进 --star-glow —— 粒子的
// 光晕必须与本体同色，用 currentColor 会取到继承的文字色，
// 星星就成了「彩色的点配一圈灰光」，这正是第一版发暗的原因。
const STAR_COLORS = ['#ff5a3c', '#ffc53d', '#36cfc9', '#ab8ef2']
const stardust = computed(() => {
  if (props.mode !== 'stardust') return []
  return props.targets.flatMap((t) =>
    Array.from({ length: 14 }, (_, i) => {
      const left = t.left + t.width * (0.04 + 0.92 * Math.random())
      const dur = 1.5 + Math.random() * 0.5
      const delay = Math.random() * 0.35
      const rise = 30 + Math.random() * 14
      const color = STAR_COLORS[i % STAR_COLORS.length]
      const size = 4.5 + Math.random() * 1.5
      return {
        key: 's' + t.key + '-' + i,
        style: {
          top: t.top + 4 + 'px',
          left: left.toFixed(1) + 'px',
          width: size.toFixed(1) + 'px',
          height: size.toFixed(1) + 'px',
          background: `radial-gradient(circle, #fff 0% 28%, ${color} 70%, transparent 100%)`,
          boxShadow: `0 0 7px ${color}`,
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
   四个阶段写在一个 1.3s 的循环里（站主拍板「再久一点点」后的时长）：
   0-0.2s 中央亮点迸出（scale 0 -> 1.6）+ 十粒火星向两侧上空溅射；
   0.15-0.75s 两道光带从中央向两端展开（scaleX 0 -> 1）；
   0.7-1.3s 光带渐隐、两端各闪一下火红光斑后全部熄灭。
   配色是写死的火红 —— 陶土主色偏暗，撑不起「对撞的火焰感」；
   光带只有 3px 高、贴着行底分割线（与扫光同一个着力点），
   所以它读起来是「这一行在发信号」，而不是「这一行被盖住了」。 */
.fx-pulse {
  position: absolute;
  height: 3px;
  z-index: 3;
  pointer-events: none;
}
.fx-pulse-dot {
  width: 8px;
  height: 8px;
  margin-top: -2.5px;
  margin-left: -4px;
  border-radius: 50%;
  background: radial-gradient(circle, #fff 0% 30%, #ff5a3c 62%, transparent 100%);
  transform: scale(0);
  animation: fx-pulse-dot 1.3s ease-out forwards;
}
@keyframes fx-pulse-dot {
  0% { transform: scale(0); opacity: 0; }
  12% { transform: scale(1.6); opacity: 1; }
  26% { transform: scale(0.9); opacity: 0.95; }
  62%, 100% { transform: scale(0); opacity: 0; }
}
.fx-pulse-beam {
  background-image: linear-gradient(90deg, transparent 0%, #ff5a3c 78%, #ffb199 94%, #fff 100%);
  transform: scaleX(0);
  animation: fx-pulse-beam 1.3s cubic-bezier(0.22, 1, 0.36, 1) forwards;
}
.fx-pulse-beam.is-left {
  transform-origin: right center;
  background-image: linear-gradient(90deg, #fff 0%, #ffb199 6%, #ff5a3c 22%, transparent 100%);
}
.fx-pulse-beam.is-right {
  transform-origin: left center;
}
@keyframes fx-pulse-beam {
  0%, 8% { transform: scaleX(0); opacity: 0; }
  10% { opacity: 1; }
  56% { transform: scaleX(1); opacity: 0.95; }
  84% { opacity: 0.35; }
  100% { transform: scaleX(1); opacity: 0; }
}
/* 端点火红光斑：光到达时才亮，一大圈再灭 —— 这是「撞到了」的收束 */
.fx-pulse-spark {
  width: 9px;
  height: 9px;
  margin-top: -3px;
  border-radius: 50%;
  background: radial-gradient(circle, #fff 0% 32%, #ff5a3c 66%, transparent 100%);
  transform: scale(0);
  opacity: 0;
  animation: fx-pulse-spark 1.3s ease-out forwards;
}
@keyframes fx-pulse-spark {
  0%, 46% { transform: scale(0); opacity: 0; }
  58% { transform: scale(2.4); opacity: 1; }
  76% { transform: scale(1); opacity: 0.6; }
  92%, 100% { transform: scale(0); opacity: 0; }
}
/* 溅射火星：迸出瞬间从中心向两侧上空飞散的小粒子，
   飞行向量由内联 --ember-dx / --ember-dy 给（生成时随机）。 */
.fx-pulse-ember {
  height: 4px;
  border-radius: 50%;
  opacity: 0;
  animation: fx-pulse-ember 1.3s ease-out forwards;
}
@keyframes fx-pulse-ember {
  0% { opacity: 0; transform: translate(0, 0) scale(1); }
  10% { opacity: 1; }
  70% { opacity: 0.85; }
  100% {
    opacity: 0;
    transform: translate(var(--ember-dx, 20px), calc(var(--ember-dy, -12px) - 6px)) scale(0.3);
  }
}

/* ---------- 星尘上浮（档位：stardust） ----------
   14 粒小星从行内升起、上浮、缩没（初版 7 粒 / 1.1s 被站主判为
   「基本看不到」：粒子加倍、时长拉到 1.5-2.0s、本体改白心亮核、
   光晕与本体同色 —— 第一版光晕误用 currentColor，星星周围是一圈
   灰光，发暗的锅在那里）。上升距离由每粒自己的 --star-rise 给。 */
.fx-star {
  position: absolute;
  z-index: 3;
  pointer-events: none;
  border-radius: 50%;
  opacity: 0;
  animation: fx-star-rise 1.7s ease-out forwards;
}
@keyframes fx-star-rise {
  0% { opacity: 0; transform: translateY(5px) scale(0.5); }
  18% { opacity: 1; }
  72% { opacity: 0.8; }
  100% {
    opacity: 0;
    transform: translateY(calc(-1 * var(--star-rise, 36px))) scale(0.4);
  }
}
</style>
