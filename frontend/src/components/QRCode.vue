<script setup lang="ts">
import { computed } from 'vue'
import { encodeQR, qrToSvg } from '@/utils/qr'

// 二维码展示：把 otpauth URI 渲染成 SVG，并给出「扫不了」时的保底路径。
//
// 为什么同时给出手动密钥：认证器 App 的手输模式需要 Base32 密钥，
// 而密钥只在 otpauth URI 里。屏幕反光、摄像头脏、App 版本老都会让
// 扫码失败 —— 此时用户唯一能自救的办法就是手输。
const props = defineProps<{
  /** otpauth:// URI */
  uri: string
  /** 手动录入用的 Base32 密钥（从 URI 里解析，可选） */
  secret?: string
  /** 边长（px）；默认 180，移动端由父容器缩小 */
  size?: number
}>()

const svg = computed(() => {
  try {
    return qrToSvg(encodeQR(props.uri))
  } catch {
    return ''
  }
})

const dim = computed(() => props.size ?? 180)
</script>

<template>
  <div class="qr-block">
    <div
      v-if="svg"
      class="qr-image"
      :style="{ width: `${dim}px`, height: `${dim}px` }"
      role="img"
      aria-label="用于绑定动态码的二维码"
      v-html="svg"
    />
    <p v-else class="qr-error">二维码生成失败，请使用下方密钥手动添加</p>
    <p v-if="secret" class="qr-secret">
      扫不出来？在认证器里手动添加，密钥：
      <code class="mono">{{ secret }}</code>
    </p>
  </div>
</template>

<style scoped>
.qr-block {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}

/* 二维码固定白底黑码：深色主题下也不能反色 —— 反色二维码有一部分
   扫码器认不出（规范只要求深色模块在浅色底上）。所以这里刻意用
   字面颜色，不用主题令牌。 */
.qr-image {
  background: #fff;
  border-radius: var(--radius-panel);
  padding: 8px;
  border: 1px solid var(--color-border);
}

.qr-image :deep(svg) {
  width: 100%;
  height: 100%;
  display: block;
}

.qr-secret {
  margin: 0;
  font-size: var(--font-size-sm);
  color: var(--color-text-secondary);
  text-align: center;
  line-height: 1.6;
}

.qr-error {
  margin: 0;
  font-size: var(--font-size-sm);
  color: var(--text-red);
}
</style>
