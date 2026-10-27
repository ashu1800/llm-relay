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
    <div class="qr-frame" :style="{ width: `${dim}px`, height: `${dim}px` }">
      <!-- 四角取景卡榫刻度（方案 6） -->
      <span class="qr-reticle tl" aria-hidden="true" />
      <span class="qr-reticle tr" aria-hidden="true" />
      <span class="qr-reticle bl" aria-hidden="true" />
      <span class="qr-reticle br" aria-hidden="true" />
      <span class="qr-laser" aria-hidden="true" />
      <div
        v-if="svg"
        class="qr-image"
        role="img"
        aria-label="用于绑定动态码的二维码"
        v-html="svg"
      />
      <p v-else class="qr-error">二维码生成失败，请使用下方密钥手动添加</p>
    </div>
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
.qr-frame {
  position: relative;
  background: #fff;
  border-radius: var(--radius-panel);
  padding: 8px;
  border: 1px solid var(--color-border);
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  overflow: hidden;
}

.qr-image {
  width: 100%;
  height: 100%;
}

.qr-reticle {
  position: absolute;
  width: 10px;
  height: 10px;
  border-color: var(--color-primary);
  border-style: solid;
  pointer-events: none;
  z-index: 2;
  opacity: 0.85;
}

.qr-reticle.tl { top: 4px; left: 4px; border-width: 2px 0 0 2px; }
.qr-reticle.tr { top: 4px; right: 4px; border-width: 2px 2px 0 0; }
.qr-reticle.bl { bottom: 4px; left: 4px; border-width: 0 0 2px 2px; }
.qr-reticle.br { bottom: 4px; right: 4px; border-width: 0 2px 2px 0; }

.qr-laser {
  position: absolute;
  left: 0;
  right: 0;
  height: 2px;
  background: linear-gradient(90deg, transparent 0%, var(--color-primary) 50%, transparent 100%);
  filter: drop-shadow(0 0 4px var(--color-primary));
  opacity: 0.55;
  pointer-events: none;
  z-index: 3;
  animation: qr-laser-scan 3.5s ease-in-out infinite;
}

@keyframes qr-laser-scan {
  0%, 100% { top: 4px; opacity: 0; }
  15%, 85% { opacity: 0.7; }
  50% { top: calc(100% - 6px); }
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
