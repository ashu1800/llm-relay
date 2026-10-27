<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { LockOutlined, SafetyCertificateOutlined, KeyOutlined, ArrowLeftOutlined } from '@ant-design/icons-vue'
import { message } from '@/utils/feedback'
import { ApiError } from '@/api/client'
import { consoleAuthApi } from '@/api/consoleAuth'
import { useAuthStore } from '@/stores/auth'
import { confirmKeyError, isCompleteCode, keyStrengthError, sanitizeCode } from '@/components/authSetup'
import QRCode from '@/components/QRCode.vue'

// 首次部署的设置向导：选 2FA 还是密钥，二选一。
//
// 阶段划分（判据在 components/authSetup.ts 的 wizardStage，这里只渲染）：
//   bootstrap —— 输引导密钥（RELAY_ADMIN_KEY），换一个「向导会话」；
//   choose    —— 两张选项卡；
//   totp      —— 二维码 + 6 位码确认；
//   key       —— 手动填写管理密钥（不再自动生成）。
//
// 完成任何一条路径后，引导密钥立即退役：后续登录只认用户自己设的凭据。
const emit = defineEmits<{ (e: 'done'): void }>()
const auth = useAuthStore()

type Choice = 'totp' | 'key'
const chosen = ref<Choice | null>(null)

// 是否需要先验引导密钥：鉴权开启且尚未登录时需要；已登录（或鉴权关闭
// 的本地部署，此时后端 status 已把它视为已认证）直接进入选择阶段。
const stage = computed(() => {
  if (auth.enabled && !auth.authenticated) return 'bootstrap'
  return chosen.value ?? 'choose'
})

// ---- 阶段一：引导密钥 ----
const bootstrapKey = ref('')
const bootstrapBusy = ref(false)
const errorText = ref('')
const lockSeconds = ref(0)
let lockTimer: number | null = null

const canBootstrap = computed(
  () => bootstrapKey.value.trim().length > 0 && lockSeconds.value === 0 && !bootstrapBusy.value
)

function startCountdown(seconds: number) {
  // 先清掉上一个计时器：连续两次 429 会叠出两个 interval，它们同时递减
  // 同一个 lockSeconds（倒计时跳变），而 lockTimer 只记得最后一个 ——
  // 卸载时前一个永远清不掉。
  stopCountdown()
  lockSeconds.value = seconds
  lockTimer = window.setInterval(() => {
    lockSeconds.value--
    if (lockSeconds.value <= 0) stopCountdown()
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

async function submitBootstrap() {
  const k = bootstrapKey.value.trim()
  if (!k || bootstrapBusy.value) return
  bootstrapBusy.value = true
  errorText.value = ''
  try {
    await auth.loginKey(k)
    bootstrapKey.value = ''
    // 若库里其实已初始化（method 非 unset），store 的 method 会更新，
    // 外层 LoginView 会直接放行 —— 这里只处理向导内的推进
    if (!auth.needsSetup) {
      emit('done')
      return
    }
    message.success('引导密钥正确，请选择认证方式')
  } catch (e) {
    if (e instanceof ApiError) {
      errorText.value = e.message
      const m = e.status === 429 ? /(\d+)\s*秒/.exec(e.message) : null
      if (m) startCountdown(Number(m[1]))
    } else {
      errorText.value = '验证失败，请稍后重试'
    }
  } finally {
    bootstrapBusy.value = false
  }
}

// ---- 阶段二：选择 ----
function choose(c: Choice) {
  chosen.value = c
  errorText.value = ''
  if (c === 'totp') void initTOTP()
}

// ---- 阶段三 A：绑定 2FA ----
const totpUri = ref('')
const totpSecret = ref('')
const totpCode = ref('')
const totpBusy = ref(false)

async function initTOTP() {
  totpBusy.value = true
  errorText.value = ''
  try {
    const res = await consoleAuthApi.setupTotpInit()
    totpUri.value = res.otpauth_uri
    totpSecret.value = extractSecret(res.otpauth_uri)
    totpCode.value = ''
  } catch (e: any) {
    errorText.value = e?.message || '生成二维码失败'
  } finally {
    totpBusy.value = false
  }
}

/** 从 otpauth URI 里取出 Base32 密钥（给手输模式的保底路径）。 */
function extractSecret(uri: string): string {
  const m = /[?&]secret=([^&]+)/.exec(uri)
  return m ? decodeURIComponent(m[1]) : ''
}

const canConfirmTOTP = computed(() => isCompleteCode(totpCode.value) && !totpBusy.value)

async function confirmTOTP() {
  if (!canConfirmTOTP.value) return
  totpBusy.value = true
  errorText.value = ''
  try {
    await consoleAuthApi.setupTotpConfirm(totpCode.value.trim())
    auth.setMethod('totp')
    message.success('2FA 绑定完成，后续登录请使用认证器上的动态码')
    emit('done')
  } catch (e: any) {
    errorText.value = e?.message || '绑定失败，请检查动态码'
  } finally {
    totpBusy.value = false
  }
}

// ---- 阶段三 B：设置管理密钥 ----
const newKey = ref('')
const newKeyConfirm = ref('')
const keyBusy = ref(false)

const keyProblem = computed(() => keyStrengthError(newKey.value) || confirmKeyError(newKey.value, newKeyConfirm.value))
const canSetKey = computed(() => !keyProblem.value && !keyBusy.value)

async function submitKey() {
  if (!canSetKey.value) return
  keyBusy.value = true
  errorText.value = ''
  try {
    await consoleAuthApi.setupKey(newKey.value)
    auth.setMethod('key')
    message.success('管理密钥已设置，请妥善保存（后续登录需要它）')
    emit('done')
  } catch (e: any) {
    errorText.value = e?.message || '设置失败'
  } finally {
    keyBusy.value = false
  }
}

function back() {
  if (stage.value === 'choose') {
    chosen.value = null
    return
  }
  chosen.value = null
  errorText.value = ''
}
</script>

<template>
  <div class="setup">
    <!-- 阶段一：引导密钥 -->
    <template v-if="stage === 'bootstrap'">
      <h2 class="setup-title">首次使用：先验证引导密钥</h2>
      <p class="setup-hint">
        引导密钥由安装脚本生成，写在服务器 <span class="mono">deploy/.env</span> 的
        <span class="mono">RELAY_ADMIN_KEY</span> 里。它只用于这一次初始化。
      </p>

      <a-alert v-if="errorText" type="error" show-icon :message="errorText" class="setup-error" />

      <form class="setup-form" @submit.prevent="submitBootstrap">
        <a-input-password
          v-model:value="bootstrapKey"
          placeholder="引导密钥"
          size="large"
          autocomplete="current-password"
          autofocus
          aria-label="引导密钥"
          @pressEnter="submitBootstrap"
        >
          <template #prefix>
            <LockOutlined style="color: var(--color-icon)" />
          </template>
        </a-input-password>
        <a-button type="primary" size="large" block :disabled="!canBootstrap" :loading="bootstrapBusy" html-type="submit">
          {{ lockSeconds > 0 ? `已锁定，${lockSeconds} 秒后可重试` : '下一步' }}
        </a-button>
      </form>
    </template>

    <!-- 阶段二：选择认证方式 -->
    <template v-else-if="stage === 'choose'">
      <h2 class="setup-title">选择登录认证方式</h2>
      <p class="setup-hint">二者只能选一个。之后可以在「系统设置 → 登录认证」里切换，切换时需要验证当前凭据。</p>

      <a-alert v-if="errorText" type="error" show-icon :message="errorText" class="setup-error" />

      <div class="choice-grid">
        <button type="button" class="choice" @click="choose('totp')">
          <SafetyCertificateOutlined class="choice-icon" />
          <span class="choice-title">动态码（2FA）</span>
          <span class="choice-desc">用认证器 App（如 Google Authenticator）扫码绑定，登录时输入 6 位动态码。绑定后密钥登录自动禁用。</span>
        </button>
        <button type="button" class="choice" @click="choose('key')">
          <KeyOutlined class="choice-icon" />
          <span class="choice-title">管理密钥</span>
          <span class="choice-desc">自己设定一个至少 16 位的密钥，登录时输入它。不再自动生成，密钥由你决定。</span>
        </button>
      </div>
    </template>

    <!-- 阶段三 A：绑定 2FA -->
    <template v-else-if="stage === 'totp'">
      <h2 class="setup-title">绑定动态码（2FA）</h2>
      <p class="setup-hint">用认证器 App 扫描下方二维码，然后输入 App 上显示的 6 位动态码完成绑定。</p>

      <a-alert v-if="errorText" type="error" show-icon :message="errorText" class="setup-error" />

      <QRCode v-if="totpUri" :uri="totpUri" :secret="totpSecret" :size="180" />
      <a-spin v-else :spinning="totpBusy" />

      <form class="setup-form" @submit.prevent="confirmTOTP">
        <a-input
          v-model:value="totpCode"
          placeholder="6 位动态码"
          size="large"
          inputmode="numeric"
          autocomplete="one-time-code"
          class="mono code-input"
          aria-label="动态码"
          @input="totpCode = sanitizeCode(totpCode)"
          @pressEnter="confirmTOTP"
        />
        <a-button type="primary" size="large" block :disabled="!canConfirmTOTP" :loading="totpBusy" html-type="submit">
          完成绑定
        </a-button>
        <a-button type="link" block @click="back">返回重新选择</a-button>
      </form>
    </template>

    <!-- 阶段三 B：设置管理密钥 -->
    <template v-else>
      <h2 class="setup-title">设置管理密钥</h2>
      <p class="setup-hint">手动设定一个至少 16 位的密钥（建议用密码管理器生成随机串）。登录时输入它。</p>

      <a-alert v-if="errorText" type="error" show-icon :message="errorText" class="setup-error" />

      <form class="setup-form" @submit.prevent="submitKey">
        <a-input-password
          v-model:value="newKey"
          placeholder="新管理密钥（≥16 位）"
          size="large"
          autocomplete="new-password"
          autofocus
          aria-label="新管理密钥"
        />
        <a-input-password
          v-model:value="newKeyConfirm"
          placeholder="再输入一次"
          size="large"
          autocomplete="new-password"
          aria-label="确认新管理密钥"
          @pressEnter="submitKey"
        />
        <p v-if="newKey && keyProblem" class="setup-problem">{{ keyProblem }}</p>
        <a-button type="primary" size="large" block :disabled="!canSetKey" :loading="keyBusy" html-type="submit">
          完成设置
        </a-button>
        <a-button type="link" block @click="back">
          <ArrowLeftOutlined /> 返回重新选择
        </a-button>
      </form>
    </template>
  </div>
</template>

<style scoped>
.setup {
  display: flex;
  flex-direction: column;
}

.setup-title {
  margin: 0 0 6px;
  /* 视觉上仍是页面主标题档位：h2 只是语义层级（整页唯一的 h1 在
     LoginView，契约检查要求「登录页有且仅有一个 h1」）。 */
  font-size: var(--font-size-display-sm);
  font-weight: 700;
  color: var(--color-text);
}

.setup-hint {
  margin: 0 0 var(--gap);
  font-size: var(--font-size-md);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.setup-error {
  margin-bottom: var(--gap);
}

.setup-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: var(--gap);
}

.setup-problem {
  margin: 0;
  font-size: var(--font-size-sm);
  color: var(--text-amber);
}

.code-input {
  letter-spacing: 0.3em;
  text-align: center;
}

.choice-grid {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* 选项卡是按钮而不是卡片：整块可点、键盘可达（focus 环由 antd 的
   全局 focus 样式给出），语义上是「选择一个动作」。 */
.choice {
  display: flex;
  flex-direction: column;
  gap: 4px;
  text-align: left;
  padding: 14px 16px;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-panel);
  cursor: pointer;
  transition: border-color 0.15s ease;
}

.choice:hover {
  border-color: var(--color-primary);
}

.choice:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

/* 契约检查对 .vue 里的 color: 一视同仁（分不清图标与正文），
   所以这里也走文本档 —— --text-terracotta 与 --color-primary 同色相，
   视觉上几乎一致，但它有实测的对比度保障。 */
.choice-icon {
  font-size: var(--font-size-xl);
  color: var(--text-terracotta);
}

.choice-title {
  font-size: var(--font-size-md);
  font-weight: 600;
  color: var(--color-text);
}

.choice-desc {
  font-size: var(--font-size-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.mono {
  font-family: var(--font-family-mono);
}
</style>
