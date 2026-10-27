<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { message } from '@/utils/feedback'
import { api } from '@/api/client'
import { consoleAuthApi } from '@/api/consoleAuth'
import { useAuthStore } from '@/stores/auth'
import {
  authActions,
  confirmKeyError,
  isCompleteCode,
  keyStrengthError,
  methodLabel,
  sanitizeCode,
  type AuthMethod
} from '@/components/authSetup'
import QRCode from '@/components/QRCode.vue'
import { modalWidth } from '@/utils/modalWidth'

// 「登录认证」面板：展示当前方式 + 四个凭据管理动作。
//
// 权限模型（与后端一致）：
//   - 密钥模式：改密需旧密钥；切到 2FA 需扫码 + 验新码；
//   - 2FA 模式：换绑需当前动态码；切到密钥需当前动态码 + 新密钥。
// 每一次成功变更都会轮换会话版本 —— 其它设备上的登录全部失效，
// 当前浏览器由后端重签 Cookie 无感续期。
const auth = useAuthStore()
const method = ref<AuthMethod>('unset')
const loading = ref(true)
const busy = ref(false)
const errorText = ref('')

async function loadMethod() {
  loading.value = true
  try {
    const res = await api.get<{ runtime: { console_auth_method?: AuthMethod } }>('/settings')
    const m = res.runtime?.console_auth_method
    method.value = m === 'totp' || m === 'key' ? m : 'unset'
    auth.setMethod(method.value)
  } catch {
    // 设置页其它区块也会失败，这里不额外报错，保持面板可读
  } finally {
    loading.value = false
  }
}

onMounted(loadMethod)

const actions = computed(() => authActions(method.value))

// ---- 弹窗骨架：四个动作共用一个 modal，用 kind 切换内容 ----
type Kind = 'changeKey' | 'switchToTotp' | 'switchToKey' | 'rebind' | null
const kind = ref<Kind>(null)
const open = computed({
  get: () => kind.value !== null,
  set: (v: boolean) => {
    if (!v) close()
  }
})

const title = computed(() => {
  switch (kind.value) {
    case 'changeKey':
      return '修改管理密钥'
    case 'switchToTotp':
      return '切换到动态码（2FA）登录'
    case 'switchToKey':
      return '切换到密钥登录'
    case 'rebind':
      return '更换 2FA 绑定'
    default:
      return ''
  }
})

function close() {
  kind.value = null
  errorText.value = ''
  resetForms()
}

function start(k: Exclude<Kind, null>) {
  close()
  kind.value = k
  if (k === 'switchToTotp' || k === 'rebind') {
    // 前两步：switchToTotp 直接出码；rebind 先要当前码
    if (k === 'switchToTotp') void beginSwitchToTotp()
  }
}

// ---- 表单状态 ----
const oldKey = ref('')
const newKey = ref('')
const newKey2 = ref('')
const curCode = ref('')
const newCode = ref('')

const uri = ref('')
const secret = ref('')

function resetForms() {
  oldKey.value = ''
  newKey.value = ''
  newKey2.value = ''
  curCode.value = ''
  newCode.value = ''
  uri.value = ''
  secret.value = ''
}

function extractSecret(u: string): string {
  const m = /[?&]secret=([^&]+)/.exec(u)
  return m ? decodeURIComponent(m[1]) : ''
}

const keyProblem = computed(() => keyStrengthError(newKey.value) || confirmKeyError(newKey.value, newKey2.value))

function applyMethod(m: AuthMethod, msg: string) {
  method.value = m
  auth.setMethod(m)
  message.success(msg)
  close()
}

async function doChangeKey() {
  if (keyProblem.value) return
  busy.value = true
  errorText.value = ''
  try {
    await consoleAuthApi.changeKey(oldKey.value, newKey.value)
    applyMethod('key', '密钥已修改，其它设备上的登录已失效')
  } catch (e: any) {
    errorText.value = e?.message || '修改失败'
  } finally {
    busy.value = false
  }
}

async function beginSwitchToTotp() {
  busy.value = true
  errorText.value = ''
  try {
    const res = await consoleAuthApi.switchToTotpInit()
    uri.value = res.otpauth_uri
    secret.value = extractSecret(res.otpauth_uri)
  } catch (e: any) {
    errorText.value = e?.message || '生成二维码失败'
  } finally {
    busy.value = false
  }
}

async function doSwitchToTotp() {
  if (!isCompleteCode(newCode.value)) return
  busy.value = true
  errorText.value = ''
  try {
    await consoleAuthApi.switchToTotpConfirm(newCode.value.trim())
    applyMethod('totp', '已切换到动态码登录，原密钥已失效')
  } catch (e: any) {
    errorText.value = e?.message || '切换失败'
  } finally {
    busy.value = false
  }
}

async function doSwitchToKey() {
  if (!isCompleteCode(curCode.value) || keyProblem.value) return
  busy.value = true
  errorText.value = ''
  try {
    await consoleAuthApi.switchToKey(curCode.value.trim(), newKey.value)
    applyMethod('key', '已切换到密钥登录，原 2FA 绑定已失效')
  } catch (e: any) {
    errorText.value = e?.message || '切换失败'
  } finally {
    busy.value = false
  }
}

async function beginRebind() {
  if (!isCompleteCode(curCode.value)) return
  busy.value = true
  errorText.value = ''
  try {
    const res = await consoleAuthApi.rebindTOTPInit(curCode.value.trim())
    uri.value = res.otpauth_uri
    secret.value = extractSecret(res.otpauth_uri)
  } catch (e: any) {
    errorText.value = e?.message || '验证失败'
  } finally {
    busy.value = false
  }
}

async function doRebind() {
  if (!isCompleteCode(newCode.value)) return
  busy.value = true
  errorText.value = ''
  try {
    await consoleAuthApi.rebindTOTPConfirm(newCode.value.trim())
    applyMethod('totp', '2FA 绑定已更换，原设备的动态码已失效')
  } catch (e: any) {
    errorText.value = e?.message || '换绑失败'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="panel">
    <h2 class="panel-title">登录认证</h2>

    <div class="auth-line">
      <a-tag :color="method === 'unset' ? 'red' : 'green'">{{ methodLabel(method) }}</a-tag>
      <span v-if="method === 'unset'" class="dim">
        尚未完成初始化，请<strong>退出后重新登录</strong>，登录页会引导你选择 2FA 或密钥。
      </span>
      <span v-else class="dim">登录会话有效期由 <span class="mono">RELAY_SESSION_TTL</span> 控制</span>
    </div>

    <template v-if="method !== 'unset'">
      <div class="btn-row">
        <a-button v-if="actions.canChangeKey" size="small" @click="start('changeKey')">修改密钥</a-button>
        <a-button v-if="actions.canSwitchToTotp" size="small" type="primary" ghost @click="start('switchToTotp')">
          切换到动态码（2FA）
        </a-button>
        <a-button v-if="actions.canRebindTotp" size="small" @click="start('rebind')">更换 2FA 绑定</a-button>
        <a-button v-if="actions.canSwitchToKey" size="small" type="primary" ghost @click="start('switchToKey')">
          切换到密钥登录
        </a-button>
      </div>

      <div class="note">
        两种方式<strong>二选一</strong>：同一时刻只有一种登录凭据有效。改动凭据会立即让<strong>其它设备</strong>上的登录失效，
        当前浏览器自动续期。2FA 绑定完成时请<strong>立即用新设备验证一次</strong>，确认能解出动态码后再关闭页面。
      </div>
    </template>

    <div v-if="loading" class="dim">正在读取…</div>

    <a-modal
      v-model:open="open"
      :title="title"
      :width="modalWidth(420)"
      :footer="null"
      :mask-closable="!busy"
      destroy-on-close
    >
      <a-alert v-if="errorText" type="error" show-icon :message="errorText" class="modal-error" />

      <!-- 修改密钥 -->
      <div v-if="kind === 'changeKey'" class="form">
        <p class="tip">修改后旧密钥立即失效，其它设备需用新密钥重新登录。</p>
        <a-input-password v-model:value="oldKey" placeholder="当前密钥" size="large" autocomplete="current-password" />
        <a-input-password v-model:value="newKey" placeholder="新密钥（≥16 位）" size="large" autocomplete="new-password" />
        <a-input-password v-model:value="newKey2" placeholder="再输入一次新密钥" size="large" autocomplete="new-password" />
        <p v-if="newKey && keyProblem" class="problem">{{ keyProblem }}</p>
        <a-button
          type="primary"
          block
          size="large"
          :loading="busy"
          :disabled="!oldKey || !!keyProblem"
          @click="doChangeKey"
        >
          确认修改
        </a-button>
      </div>

      <!-- 密钥 → 2FA -->
      <div v-else-if="kind === 'switchToTotp'" class="form">
        <p class="tip">
          用认证器 App 扫码，然后输入 App 上显示的 6 位动态码。完成后<strong>密钥登录立即禁用</strong>，
          之后只能用动态码登录。
        </p>
        <QRCode v-if="uri" :uri="uri" :secret="secret" :size="200" />
        <a-spin v-else :spinning="busy" />
        <a-input
          v-model:value="newCode"
          placeholder="6 位动态码"
          size="large"
          inputmode="numeric"
          autocomplete="one-time-code"
          class="mono code-input"
          @input="newCode = sanitizeCode(newCode)"
        />
        <a-button
          type="primary"
          block
          size="large"
          :loading="busy"
          :disabled="!isCompleteCode(newCode) || !uri"
          @click="doSwitchToTotp"
        >
          完成切换
        </a-button>
      </div>

      <!-- 2FA → 密钥 -->
      <div v-else-if="kind === 'switchToKey'" class="form">
        <p class="tip">先输入当前认证器上的 6 位动态码，再设置新的管理密钥。完成后 2FA 绑定立即失效。</p>
        <a-input
          v-model:value="curCode"
          placeholder="当前 6 位动态码"
          size="large"
          inputmode="numeric"
          autocomplete="one-time-code"
          class="mono code-input"
          @input="curCode = sanitizeCode(curCode)"
        />
        <a-input-password v-model:value="newKey" placeholder="新管理密钥（≥16 位）" size="large" autocomplete="new-password" />
        <a-input-password v-model:value="newKey2" placeholder="再输入一次新密钥" size="large" autocomplete="new-password" />
        <p v-if="newKey && keyProblem" class="problem">{{ keyProblem }}</p>
        <a-button
          type="primary"
          block
          size="large"
          :loading="busy"
          :disabled="!isCompleteCode(curCode) || !!keyProblem"
          @click="doSwitchToKey"
        >
          完成切换
        </a-button>
      </div>

      <!-- 2FA 换绑 -->
      <div v-else-if="kind === 'rebind'" class="form">
        <template v-if="!uri">
          <p class="tip">先输入当前认证器上的动态码验证身份，再绑定新设备。</p>
          <a-input
            v-model:value="curCode"
            placeholder="当前 6 位动态码"
            size="large"
            inputmode="numeric"
            autocomplete="one-time-code"
            class="mono code-input"
            @input="curCode = sanitizeCode(curCode)"
          />
          <a-button type="primary" block size="large" :loading="busy" :disabled="!isCompleteCode(curCode)" @click="beginRebind">
            下一步
          </a-button>
        </template>
        <template v-else>
          <p class="tip">用新设备扫码，输入新设备上显示的 6 位动态码完成换绑。</p>
          <QRCode :uri="uri" :secret="secret" :size="200" />
          <a-input
            v-model:value="newCode"
            placeholder="新设备上的 6 位动态码"
            size="large"
            inputmode="numeric"
            autocomplete="one-time-code"
            class="mono code-input"
            @input="newCode = sanitizeCode(newCode)"
          />
          <a-button type="primary" block size="large" :loading="busy" :disabled="!isCompleteCode(newCode)" @click="doRebind">
            完成换绑
          </a-button>
        </template>
      </div>
    </a-modal>
  </section>
</template>

<style scoped>
.auth-line {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}

.btn-row {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}

.note {
  font-size: var(--font-size-sm);
  line-height: 1.7;
  color: var(--color-text-secondary);
}

.form {
  display: flex;
  flex-direction: column;
  gap: 12px;
  align-items: stretch;
}

.tip {
  margin: 0;
  font-size: var(--font-size-sm);
  line-height: 1.6;
  color: var(--color-text-secondary);
}

.problem {
  margin: 0;
  font-size: var(--font-size-sm);
  color: var(--text-amber);
}

.modal-error {
  margin-bottom: 12px;
}

.code-input {
  letter-spacing: 0.3em;
  text-align: center;
}

.mono {
  font-family: var(--font-family-mono);
}
</style>
