<script setup lang="ts">
// 客户端图标：三种形态，按传入的 spec 渲染（见 clientDisplay 的 ClientIconSpec）：
//   · builtin —— 内置品牌轮廓，CSS mask 渲染（本组件的原生形态）；
//   · img     —— 用户映射的位图（data URI / 图片地址）；
//   · text    —— 用户映射的一两个字符（emoji / 缩写）。
//
// **为什么内置图标用 CSS mask 而不是 <img src>**（这里试过 img，结论写下来免得回去）：
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
// 用户映射的 img / text 没有这个问题（位图与字符自带颜色），直接渲染。
//
// 与渠道列 ChannelIcon 的分工：那个的三态是「图片/文字/首字母派生色」，
// 因为渠道图标是**用户可配**的；客户端的默认兜底是统一 default.svg
// （用户明确要过「不支持的设定一个默认图标」），派生色不进这条链路。
import { computed, ref, watch } from 'vue'
import { clientIconFile } from '@/components/clientMeta'
import type { ClientIconSpec } from '@/components/clientDisplay'

const props = withDefaults(
  defineProps<{ id?: string | null; spec?: ClientIconSpec | null; size?: number }>(),
  { size: 18 }
)

// spec（用户映射的三态）优先；没传时按 id 走内置链路 ——
// spec 由 clientDisplay.resolveClientIcon 现算，调用方不必自己判断形态
const resolved = computed<ClientIconSpec>(
  () => props.spec ?? { kind: 'builtin', file: clientIconFile(props.id) }
)
// mask 的 url 走内联自定义属性：/client-icons/ 是 public 目录下的静态资源
// （与 fonts、主题光标同一条路），vite 构建时原样拷到 dist 根，url 里不能
// 带 hash。不用 `?raw` / `@/assets` import —— 那条路会产出带 hash 的文件名，
// 而这里的 src 是运行时拼的字符串，拿不到 hash。
const maskFile = computed(() => (resolved.value.kind === 'builtin' ? resolved.value.file : ''))

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
const fallbackFile = computed(() => clientIconFile(props.id))
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
  <span
    v-else
    class="cli-icon"
    :style="{
      // 图片挂了就回落到内置图标（见 script 里 imgFailed 的说明）
      '--cli-icon': `url(/client-icons/${showImg ? maskFile : fallbackFile})`,
      width: size + 'px',
      height: size + 'px'
    }"
    aria-hidden="true"
  />
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
