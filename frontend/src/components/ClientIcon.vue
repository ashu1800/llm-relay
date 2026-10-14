<script setup lang="ts">
// 客户端图标：官方品牌轮廓 / 统一默认图标两种形态。
//
// **为什么用 CSS mask 而不是 <img src>**（这里试过 img，结论写下来免得回去）：
// <img> 引的 SVG 是一份独立文档，拿不到页面级 CSS —— 品牌 logo 里大量
// fill="#000"（Cursor、Ollama、JetBrains 这些官方色就是纯黑），在深色主题
// 下会直接隐形。给每个品牌准备「亮版/暗版」两份资产、再按 22 套主题挑一份，
// 意味着每次加图标都要维护两份、且主题一多就会漏（漏了不报错，只是某些
// 主题下看不见）。
//
// mask 把「形状」与「颜色」拆开：SVG 只当蒙版（它的 fill 是什么颜色无关
// 紧要，只有 alpha 参与），颜色由 background-color 决定 —— 直接读全局的
// --color-text-secondary，随主题自动变，对比度还落在 check-theme-contrast
// 已有的「次要文字 on 卡片 ≥ 4.5:1」红线上。一套资产、22 套主题全适配。
//
// 与渠道列 ChannelIcon 的分工：那个有三态（图片/文字/首字母派生色），因为
// 渠道图标是**用户可配**的；客户端图标是内置固定资产，认不出就统一
// default.svg（用户明确要过「不支持的设定一个默认图标」——派生色的首字母
// 在品牌图标旁边会像一枚坏掉的头像，统一的默认图标至少是「我不知道，
// 但这是同类东西」的一致表达）。
import { computed } from 'vue'
import { clientIconFile } from '@/components/clientMeta'

const props = withDefaults(defineProps<{ id?: string | null; size?: number }>(), { size: 18 })

const file = computed(() => clientIconFile(props.id))
// mask 的 url 走内联自定义属性：/client-icons/ 是 public 目录下的静态资源
// （与 fonts、主题光标同一条路），vite 构建时原样拷到 dist 根，url 里不能
// 带 hash。不用 `?raw` / `@/assets` import —— 那条路会产出带 hash 的文件名，
// 而这里的 src 是运行时拼的字符串，拿不到 hash。
const maskStyle = computed(() => ({ '--cli-icon': `url(/client-icons/${file.value})` }))
</script>

<template>
  <span class="cli-icon" :style="{ ...maskStyle, width: size + 'px', height: size + 'px' }" aria-hidden="true" />
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
</style>
