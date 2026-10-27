<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { LockOutlined, SafetyCertificateOutlined } from '@ant-design/icons-vue'
import { message } from '@/utils/feedback'
import { ApiError } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { isCompleteCode, loginPane, sanitizeCode } from '@/components/authSetup'
import LoginSetup from '@/components/LoginSetup.vue'

// 登录页（无账号模型，认证方式二选一）。
//
// 三种形态由 /auth/status 决定（判据在 components/authSetup.ts）：
//   - wizard：尚未完成初始化 —— 先验引导密钥，再选 2FA 或密钥；
//   - key   ：输管理密钥；
//   - totp  ：输 6 位动态码。
// 限流与错误文案由服务端给，这里负责倒计时与讲清楚「下一步做什么」。
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const pane = computed(() =>
  loginPane({
    enabled: auth.enabled,
    authenticated: auth.authenticated,
    method: auth.method,
    needs_setup: auth.needsSetup
  })
)
const key = ref('')
const code = ref('')
const submitting = ref(false)
const errorText = ref('')
// 限流倒计时（秒）。服务端 429 的文案里带「请 N 秒后再试」，
// 解析出来做个真倒计时，比一句干巴巴的报错更有用
const lockSeconds = ref(0)
let lockTimer: number | null = null

const canSubmitKey = computed(() => key.value.trim().length > 0 && lockSeconds.value === 0 && !submitting.value)
const canSubmitCode = computed(() => isCompleteCode(code.value) && lockSeconds.value === 0 && !submitting.value)

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

function handleError(e: unknown, fallback: string) {
  if (e instanceof ApiError) {
    errorText.value = e.message
    const m = e.status === 429 ? /(\d+)\s*秒/.exec(e.message) : null
    if (m) startCountdown(Number(m[1]))
  } else {
    errorText.value = fallback
  }
}

async function submitKey() {
  const k = key.value.trim()
  if (!k || submitting.value) return
  submitting.value = true
  errorText.value = ''
  try {
    await auth.loginKey(k)
    key.value = ''
    message.success('登录成功')
    await goNext()
  } catch (e) {
    handleError(e, '登录失败，请稍后重试')
  } finally {
    submitting.value = false
  }
}

async function submitCode() {
  const c = code.value.trim()
  if (!isCompleteCode(c) || submitting.value) return
  submitting.value = true
  errorText.value = ''
  try {
    await auth.loginTotp(c)
    code.value = ''
    message.success('登录成功')
    await goNext()
  } catch (e) {
    handleError(e, '登录失败，请稍后重试')
  } finally {
    submitting.value = false
  }
}

// goNext 决定登录/向导完成后的去向：仍未完成初始化就留在本页走向导，
// 否则跳到目标页。
//
// 只有这一个出口：向导成功时 LoginSetup 先调 setMethod（needsSetup 变
// false），再抛 done 事件 —— 如果 done 的处理也自己 push 一次，
// 就会与 watch 那条路径撞成 duplicate navigation。
async function goNext() {
  if (auth.needsSetup) return
  if (router.currentRoute.value.path !== '/login') return
  await router.push(redirectTarget())
}

// 状态变成「已登录且不需要初始化」时进站。两条路径都收口到这里：
//   · 向导完成（LoginSetup 的 setMethod 让它变 true）；
//   · 另一标签页完成了初始化。
// 用 watch 而不是各自 push，保证只有一次导航。加 flush: 'post' 是因为
// 向导那里紧接着还会触发一次组件卸载，等渲染落定再跳不会打断它。
watch(
  () => auth.authenticated && !auth.needsSetup,
  (ready) => {
    if (ready) void goNext()
  },
  { flush: 'post' }
)
</script>

<template>
  <div class="login-page">
    <div class="login-card" :class="{ 'is-wide': pane === 'wizard' }">
      <div class="login-brand">
        <span class="brand-mark">LR</span>
        <span class="brand-text">LLM Relay</span>
      </div>

      <!-- 整页唯一的 h1：契约检查要求登录页恰好一个（静态计数）。
           向导形态下由 LoginSetup 自己用 h2 表达各阶段标题，
           所以这里用 v-if 隐藏而不是删掉。 -->
      <h1 v-if="pane !== 'wizard'" class="login-title">登录管理台</h1>

      <!-- 首次初始化向导（含引导密钥登录） -->
      <LoginSetup v-if="pane === 'wizard'" @done="goNext" />

      <!-- 2FA 动态码登录 -->
      <template v-else-if="pane === 'totp'">
        <p class="login-hint">本站使用动态码（2FA）认证，请输入认证器 App 上的 6 位动态码。</p>
        <a-alert v-if="errorText" type="error" show-icon :message="errorText" class="login-error" />

        <form class="login-form" @submit.prevent="submitCode">
          <a-input
            v-model:value="code"
            placeholder="6 位动态码"
            size="large"
            inputmode="numeric"
            autocomplete="one-time-code"
            autofocus
            class="code-input mono"
            aria-label="动态码"
            @input="code = sanitizeCode(code)"
            @pressEnter="submitCode"
          >
            <template #prefix>
              <SafetyCertificateOutlined style="color: var(--color-icon)" />
            </template>
          </a-input>
          <a-button type="primary" size="large" block :disabled="!canSubmitCode" :loading="submitting" html-type="submit">
            {{ lockSeconds > 0 ? `已锁定，${lockSeconds} 秒后可重试` : '登录' }}
          </a-button>
        </form>

        <p class="login-foot">动态码每 30 秒更新一次。手机丢失时请按部署文档用 SQL 重置认证状态。</p>
      </template>

      <!-- 密钥登录 -->
      <template v-else>
        <p class="login-hint">本站采用密钥登录（无账号），请输入你在初始化时设置的管理密钥。</p>

        <a-alert v-if="errorText" type="error" show-icon :message="errorText" class="login-error" />

        <form class="login-form" @submit.prevent="submitKey">
          <a-input-password
            v-model:value="key"
            placeholder="管理密钥"
            size="large"
            autocomplete="current-password"
            autofocus
            aria-label="管理密钥"
            @pressEnter="submitKey"
          >
            <template #prefix>
              <LockOutlined style="color: var(--color-icon)" />
            </template>
          </a-input-password>
          <a-button type="primary" size="large" block :disabled="!canSubmitKey" :loading="submitting" html-type="submit">
            {{ lockSeconds > 0 ? `已锁定，${lockSeconds} 秒后可重试` : '登录' }}
          </a-button>
        </form>

        <p class="login-foot">忘记密钥时，请按部署文档用 SQL 清除认证状态后重新初始化。</p>
      </template>
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
  /* 与全站面板同一套几何：--radius-panel（8px）+ 1px 边框 + 投影。
     原来是 12px 圆角、无边框 —— 登录页是首屏第一眼，它和进去之后的卡片
     不是同一个圆角、同一套描边，观感上像两个站。2026-10-10 对齐。 */
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  box-shadow: var(--color-fg-shadow);
  padding: 32px 28px 24px;
}

/* 向导里有二维码与两张选项卡，380px 太窄 */
.login-card.is-wide {
  width: 460px;
}

/* 品牌区与侧栏同款（见 MainLayout 的 .brand-mark / .brand-text）：
   底色/文字色走 --brand-mark-* 令牌，圆角与字号字重也逐项对齐 ——
   同一个「LR」标记在登录页与侧栏里必须是同一个形状。 */
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
  border-radius: var(--radius-pill);
  background: var(--brand-mark-bg);
  color: var(--brand-mark-fg);
  font-size: var(--font-size-md);
  font-weight: 700;
}

.brand-text {
  font-family: var(--font-family-display);
  font-size: var(--font-size-xl);
  font-weight: 600;
  color: var(--text-primary-ink);
}

.login-title {
  margin: 0 0 6px;
  font-size: var(--font-size-display-sm);
  font-weight: 700;
  color: var(--color-text);
}

.login-hint {
  margin: 0 0 var(--gap);
  font-size: var(--font-size-md);
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
  font-size: var(--font-size-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.code-input {
  letter-spacing: 0.3em;
  text-align: center;
}

.mono {
  font-family: var(--font-family-mono);
}
</style>
