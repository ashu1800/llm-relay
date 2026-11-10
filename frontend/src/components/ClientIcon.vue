<script setup lang="ts">
// 客户端图标：三种形态，按传入的 spec 渲染（见 clientDisplay 的 ClientIconSpec）：
//   · builtin —— 内置品牌轮廓，CSS mask 渲染（本组件的原生形态）；
//   · img     —— 用户映射的位图（data URI / 图片地址）；
//   · text    —— 用户映射的一两个字符（emoji / 缩写）。
//
// **为什么单色图标用 CSS mask 而不是 <img src>**（这里试过 img，结论写下来免得回去）：
// <img> 引的 SVG 是一份独立文档，拿不到页面级 CSS —— 品牌 logo 里大量
// fill="#000"（Cursor、Ollama、JetBrains 这些官方色就是纯黑），在深色主题
// 下会直接隐形。给每个品牌准备「亮版/暗版」两份资产、再按 12 套主题挑一份，
// 意味着每次加图标都要维护两份、且主题一多就会漏（漏了不报错，只是某些
// 主题下看不见）。
//
// mask 把「形状」与「颜色」拆开：SVG 只当蒙版（它的 fill 是什么颜色无关
// 紧要，只有 alpha 参与），颜色由 background-color 决定 —— 直接读全局的
// 图标色令牌，随主题自动变，对比度落在 check-theme-contrast 已有的
// 「非文本 on 卡片 ≥ 3:1」红线上。一套资产、12 套主题全适配。
//
// 2026-11-05 起多了一条支路：**有官方配色的图标**（COLOR_ICONS）在浅色
// 主题下直接 <img> 画彩色原图，深色主题仍回落到 mask 单色轮廓。
// 判据与理由写在下面 colorFile / .cli-color 的注释里。
// 用户映射的 img / text 没有这个问题（位图与字符自带颜色），直接渲染。
//
// 与渠道列 ChannelIcon 的分工：那个的三态是「图片/文字/首字母派生色」，
// 因为渠道图标是**用户可配**的；客户端的默认兜底是统一 default.svg
// （用户明确要过「不支持的设定一个默认图标」），派生色不进这条链路。
import { computed, ref, watch } from 'vue'
import { COLOR_ICONS, COLOR_ONLY_ICONS, clientIconFile } from '@/components/clientMeta'
import { maskIconFile, type ClientIconSpec } from '@/components/clientDisplay'

const props = withDefaults(
  defineProps<{ id?: string | null; spec?: ClientIconSpec | null; size?: number }>(),
  { size: 18 }
)

// spec（用户映射的三态）优先；没传时按 id 走内置链路 ——
// spec 由 clientDisplay.resolveClientIcon 现算，调用方不必自己判断形态
const resolved = computed<ClientIconSpec>(
  () => props.spec ?? { kind: 'builtin', file: clientIconFile(props.id) }
)

// 图片加载失败时的兜底：<img> 没有 onerror 处理就是一格裂图（浏览器还会
// 显示一段 alt 或占位边框）。这种值不是异常 —— 后端在抓取失败时会**按原样**
// 保存 http(s) 地址（见 admin_client_alias.go 的 updateClientAlias），
// 所以「图标列里有一条外链迟早会挂」是设计上存在的状态。
// 失败后回落内置链路（该客户端的内置图标 → default.svg），与没配图标一致。
const imgFailed = ref(false)
// src 变了要重置：换图标之后还显示上一张的兜底就成了错误的沉默
watch(
  () => (resolved.value.kind === 'img' ? resolved.value.src : ''),
  () => {
    imgFailed.value = false
  }
)
const showImg = computed(() => resolved.value.kind === 'img' && !imgFailed.value)
const imgSrc = computed(() => (resolved.value.kind === 'img' ? resolved.value.src : ''))
// mask 的 url 走内联自定义属性：/client-icons/ 是 public 目录下的静态资源
// （与 fonts、主题光标同一条路），vite 构建时原样拷到 dist 根，url 里不能
// 带 hash。不用 `?raw` / `@/assets` import —— 那条路会产出带 hash 的文件名，
// 而这里的 src 是运行时拼的字符串，拿不到 hash。
//
// 「画哪份文件」的判据在 clientDisplay.maskIconFile（纯函数 + 单测）：
// 内置图标用 spec 里的文件，位图挂了才回落内置链路。
const maskFile = computed(() => maskIconFile(resolved.value, props.id, imgFailed.value))

// 有官方配色的内置图标（COLOR_ICONS）：浅色主题画彩色原图，深色主题回落
// 单色轮廓。为什么不一概画彩色：彩色 logo 里常有深色部分（axios 的深紫、
// curl 的近黑蓝），压在深色卡片上会糊成一团；而 OpenAI / Cursor / Copilot
// 这些官方本来就是黑白标，用主题色反而是对的形态。
const colorFile = computed(() =>
  resolved.value.kind === 'builtin' && COLOR_ICONS.has(resolved.value.file) ? resolved.value.file : ''
)
// 深色主题也画原图的图标（COLOR_ONLY_ICONS）：zcode / omp 的自带底色让
// mask 退化成一块实心方块（没有可用的回落形态）；pi 的三色（珊瑚/蓝/黄）
// 压在深色卡片上对比度都在 4:1 以上，没有「深色部分糊掉」的问题，回落反而
// 丢掉了要的彩色形态。
const colorOnly = computed(() => colorFile.value !== '' && COLOR_ONLY_ICONS.has(colorFile.value))
const sizeStyle = computed(() => ({ width: props.size + 'px', height: props.size + 'px' }))
const maskStyle = computed(() => ({
  '--cli-icon': `url(/client-icons/${maskFile.value})`,
  width: props.size + 'px',
  height: props.size + 'px'
}))
</script>

<template>
  <!-- 三态共用一套尺寸语义：宽度即图标宽，行内垂直居中交给父容器 -->
  <img
    v-if="showImg"
    class="cli-img"
    :src="imgSrc"
    :style="{ width: size + 'px', height: size + 'px' }"
    alt=""
    aria-hidden="true"
    referrerpolicy="no-referrer"
    @error="imgFailed = true"
  />
  <span
    v-else-if="resolved.kind === 'text'"
    class="cli-glyph"
    :style="{
      // 字号跟随图标边长（18px 时约 16px）。这里只能内联：size 是运行时
      // prop，令牌是静态刻度 —— 与 ChannelIcon 的同类写法一致，
      // 契约检查不覆盖内联 style 的字面量（CLAUDE.md 约定 3 讲的是
      // 组件 CSS 里不许写字面字号）。
      fontSize: size * 0.9 + 'px',
      lineHeight: size + 'px'
    }"
    aria-hidden="true"
    >{{ resolved.ch }}</span
  >
  <!-- 有官方配色的内置图标：浅色主题画彩色原图，深色主题回落单色轮廓。
       两个节点都渲染、由 CSS 按 <html data-tone> 二选一 —— 比在 JS 里读
       主题状态少一份耦合，浮层（teleport 到 body）也能正确切换。 -->
  <template v-else-if="colorFile">
    <img
      class="cli-img cli-color"
      :class="{ 'cli-always': colorOnly }"
      :src="`/client-icons/${colorFile}`"
      :style="sizeStyle"
      alt=""
      aria-hidden="true"
      referrerpolicy="no-referrer"
    />
    <span v-if="!colorOnly" class="cli-icon cli-mono" :style="maskStyle" aria-hidden="true" />
  </template>
  <!-- 单色内置图标（官方本来就是黑白标，或没有彩色版）：mask + 主题色。
       内置图标用它自己那份 SVG；位图挂了才回落到内置链路（判据见
       clientDisplay.maskIconFile —— 2026-11-05 那个「全都长一个样」的 bug）。 -->
  <span v-else class="cli-icon" :style="maskStyle" aria-hidden="true" />
</template>

<style scoped>
.cli-icon {
  display: inline-block;
  flex: 0 0 auto;
  vertical-align: middle;
  /* 形状来自 SVG 的 alpha 通道，颜色来自主题 —— 见组件头注释 */
  background-color: var(--color-text-secondary);
  -webkit-mask: var(--cli-icon) center / contain no-repeat;
  mask: var(--cli-icon) center / contain no-repeat;
}

.cli-img {
  display: inline-block;
  flex: 0 0 auto;
  vertical-align: middle;
  object-fit: contain;
}

/* 品牌彩色版的二选一（2026-11-05 站主要求「图标要和官方一样，包括颜色」）：
   浅色主题画彩色原图，深色主题换成单色轮廓 —— 彩色 logo 里的深色部分
   （axios 深紫、curl 近黑蓝）压在深色卡片上会糊掉，而黑白标的官方形态
   本来就是「随背景反色」。
   两条规则都写在 .cli-icon / .cli-img 之后：同为单类选择器时后者胜出，
   深色那两条靠 :root 前缀提权。 */
.cli-mono {
  display: none;
}
:root[data-tone='dark'] .cli-color {
  display: none;
}
:root[data-tone='dark'] .cli-mono {
  display: inline-block;
}
/* 深色主题也画原图的图标（COLOR_ONLY_ICONS：ZCode 的自带底色、Pi 的三色
   官方标志）没有回落档：两个类 + :root 前缀，压过上面那条隐藏规则。 */
:root[data-tone='dark'] .cli-color.cli-always {
  display: inline-block;
}

.cli-glyph {
  display: inline-block;
  flex: 0 0 auto;
  vertical-align: middle;
  text-align: center;
  /* 文字色走 --text-* 那一组：它是正文（一个字符），不是图标色块 ——
     --color-text-secondary 是按非文本阈值（3:1）挑的，压到卡片上只有 4.9:1
     上下，而这一格是用户自己挑的缩写，必须按正文的 4.5:1 线读得清。
     （check-contracts 的文字色守卫只拦 7 个语义色令牌，漏了这一个；
     2026-10-19 审查按 CLAUDE.md 前端约定 1 补上。） */
  color: var(--text-gray);
  /* 字符不像 mask 有形状轮廓，字重补一点存在感（与旁边 mask 图标视觉等重） */
  font-weight: 600;
}
</style>
