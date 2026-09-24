<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LockOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { ApiError } from '@/api/client'
import { useAuthStore } from '@/stores/auth'

// 登录页（无账号模型）。
//
// 没有用户名、没有注册、没有找回 —— 只有一个「管理密钥」输入框。
// 密钥校验与限流都在服务端（15 分钟内错 5 次锁定），这里负责把
// 各种失败讲清楚：密钥错误、被限流（带倒计时）、网络不通。
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const key = ref('')
const submitting = ref(false)
const errorText = ref('')
// 限流倒计时（秒）。服务端 429 的文案里带「请 N 秒后再试」，
// 解析出来做个真倒计时，比一句干巴巴的报错更有用
const lockSeconds = ref(0)
let lockTimer: number | null = null

const canSubmit = computed(() => key.value.trim().length > 0 && lockSeconds.value === 0 && !submitting.value)

// 登录成功后回哪：只接受站内路径（必须以 / 开头且不是 //），
// 否则 ?redirect=//evil.com 会把人带出站 —— open redirect。
function redirectTarget(): string {
  const raw = route.query.redirect
  const target = typeof raw === 'string' ? raw : ''
  if (target.startsWith('/') && !target.startsWith('//')) return target
  return '/console/dashboard'
}

function startCountdown(seconds: number) {
  lockSeconds.value = seconds
  lockTimer = window.setInterval(() => {
    lockSeconds.value--
    if (lockSeconds.value <= 0) {
      stopCountdown()
      errorText.value = ''
    }
  }, 1000)
}

function stopCountdown() {
  if (lockTimer !== null) {
    window.clearInterval(lockTimer)
    lockTimer = null
  }
  lockSeconds.value = 0
}

onUnmounted(stopCountdown)

async function submit() {
  const k = key.value.trim()
  if (!k || submitting.value) return
  submitting.value = true
  errorText.value = ''
  try {
    await auth.login(k)
    key.value = ''
    message.success('登录成功')
    await router.push(redirectTarget())
  } catch (e) {
    if (e instanceof ApiError) {
      errorText.value = e.message
      // 429 的文案形如「失败次数过多，请 897 秒后再试」：解析出秒数倒计时
      const m = e.status === 429 ? /(\d+)\s*秒/.exec(e.message) : null
      if (m) startCountdown(Number(m[1]))
    } else {
      errorText.value = '登录失败，请稍后重试'
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <span class="brand-mark">LR</span>
        <span class="brand-text">LLM Relay</span>
      </div>
      <h1 class="login-title">登录管理台</h1>
      <p class="login-hint">本站采用密钥登录（无账号），请输入部署时设置的管理密钥（RELAY_ADMIN_KEY）。</p>

      <a-alert v-if="errorText" type="error" show-icon :message="errorText" class="login-error" />

      <form class="login-form" @submit.prevent="submit">
        <a-input-password
          v-model:value="key"
          placeholder="管理密钥"
          size="large"
          autocomplete="current-password"
          autofocus
          @pressEnter="submit"
        >
          <template #prefix>
            <LockOutlined style="color: var(--color-icon)" />
          </template>
        </a-input-password>
        <a-button type="primary" size="large" block :disabled="!canSubmit" :loading="submitting" html-type="submit">
          {{ lockSeconds > 0 ? `已锁定，${lockSeconds} 秒后可重试` : '登录' }}
        </a-button>
      </form>

      <p class="login-foot">密钥在服务器 .env 的 RELAY_ADMIN_KEY 中设置，轮换后所有已登录会话立即失效。</p>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: var(--color-bg);
}

.login-card {
  width: 380px;
  max-width: 100%;
  background: var(--color-fg);
  border-radius: 12px;
  box-shadow: var(--color-fg-shadow);
  padding: 32px 28px 24px;
}

/* 品牌区与侧栏同款（见 MainLayout 的 .brand-mark）：
   底色/文字色走 --brand-mark-* 令牌，深浅主题各自成对 */
.login-brand {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 20px;
}

.brand-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  border-radius: 8px;
  background: var(--brand-mark-bg);
  color: var(--brand-mark-fg);
  font-size: 14px;
  font-weight: 700;
}

.brand-text {
  font-size: 16px;
  font-weight: 700;
  color: var(--color-text);
}

.login-title {
  margin: 0 0 6px;
  font-size: 20px;
  font-weight: 700;
  color: var(--color-text);
}

.login-hint {
  margin: 0 0 var(--gap);
  font-size: 13px;
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.login-error {
  margin-bottom: var(--gap);
}

.login-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.login-foot {
  margin: 16px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text-secondary);
}
</style>
