<script setup lang="ts">
// 版本徽标与更新面板。
//
// 位置在侧栏品牌名下方，与 MainLayout 里原来那枚只读徽标同一个位置 ——
// 也就是说这次改造是**替换**而不是新增：同一个信息不该在界面上出现两次。
//
// 面板里的状态链是有严格顺序的，每一条都对应一个具体时刻：
//
//   1. 有错误        → 显示错误与重试（**必须排在「有更新」之前**）
//   2. 任务进行中    → 进度条与阶段说明
//   3. 任务刚结束    → 成功 + 重启按钮，或失败 + 原因
//   4. 有更新且能更新 → 一键更新按钮（附更新日志入口）
//   5. 有更新但不能   → 说明原因（源码构建 / 更新器不在场）
//   6. 已是最新       → 发布链接 + 回滚入口
//
// 第 1 条排在最前是有原因的：更新失败时如果被第 4 条「有新版本可用」盖住，
// 用户看到的是「还能再点一次」，而错误原因（磁盘满、校验不通过）
// 就再也没机会被看到 —— 他会一直点，一直失败。
//
// 面板本体用 a-popover：侧栏 192px 且 overflow-y: auto，任何 overflow
// 不是 visible 的祖先都会裁掉绝对定位的后代（实测把 300px 的面板裁成
// 192px）。popover 默认渲染进 body，天然脱离裁剪上下文，且自带
// 视口翻转/钳制 —— 不需要自己维护那套定位数学。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import {
  ReloadOutlined,
  DownloadOutlined,
  RollbackOutlined,
  CheckCircleOutlined,
  ExclamationCircleOutlined,
  SyncOutlined,
  GithubOutlined
} from '@ant-design/icons-vue'
import { useVersionStore } from '@/stores/version'
import { versionApi, type RollbackCandidate } from '@/api/version'
import { writeClipboard } from '@/utils/clipboard'

const store = useVersionStore()

const open = ref(false)

// ---- 更新流程的本地状态 ----
// 这些不进 store：它们是「这一次交互」的状态（按钮是不是正在转圈、
// 错误提示要不要显示），而 store 里放的是「服务端的事实」。
const applying = ref(false)
const applyError = ref('')
const restarting = ref(false)
const restartCountdown = ref(0)
const justFinished = ref<'update' | 'rollback' | ''>('')

// ---- 回滚 ----
const rollbackOpen = ref(false)
const rollbackLoading = ref(false)
const rollbackError = ref('')
const rollbackList = ref<RollbackCandidate[]>([])
const rollbackHasBackup = ref(false)
const selectedVersion = ref('')
const rollingBack = ref(false)
const copied = ref(false)

const info = computed(() => store.info)
const display = computed(() => info.value?.display || '')
const fullVersion = computed(() => info.value?.version || '')
const buildType = computed(() => store.buildType)
const hasUpdate = computed(() => store.hasUpdate)
const canApply = computed(() => store.canApply)
const checkResult = computed(() => store.check)
const task = computed(() => store.task)

// 徽标配色：有更新且能更新 → 琥珀（需要动作）；其余 → 中性
const badgeClass = computed(() => (store.shouldNotify ? 'is-update' : ''))

// 更新方式的中文名，用于面板里的说明
const modeLabel = computed(() => {
  switch (checkResult.value?.apply_mode || buildType.value) {
    case 'docker':
      return '拉取新镜像并重建容器'
    case 'binary':
      return '下载新版本并替换程序文件'
    default:
      return '手动更新'
  }
})

// upToDateConfirmed：可以确定「已是最新」。
//
// 三个条件：检测跑完了、过程没出错、且结果里没有「我没能真正确认」的
// 警告。第三个条件最容易漏 —— 后端在仓库不存在或没有发布记录时
// 仍会返回 200（外层包一个 warning），因为那不该被当成一次 HTTP 故障；
// 但对界面来说，它与「确实是最新」是两件完全不同的事。
const upToDateConfirmed = computed(() => {
  if (store.checkLoading || store.checkError) return false
  if (!checkResult.value) return false
  if (hasUpdate.value) return false
  return !checkResult.value.warning
})

onMounted(async () => {
  // 版本信息在挂载时就要有：徽标是常驻元素，等用户点开才显示
  // 会让侧栏在首屏闪一下空白
  if (!store.infoLoaded) await store.fetchInfo()
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (restartTimer) clearInterval(restartTimer)
})

// 面板打开时：检测一次更新 + 捞一次进行中的任务（可能是上次留下的，
// 这让「刷新页面后回来还能看到进度」成立）。
watch(open, async (v) => {
  if (!v) return
  // 打开面板时才检测：关闭状态下不打 GitHub，
  // 而限额是共享的（未配 token 时每小时只有 60 次）
  if (!store.checkedOnce && !store.checkLoading) {
    void store.fetchCheck(false)
  }
  const t = await store.pollProgress()
  // 只有带 id 的真实任务才需要起轮询；idle 占位对象意味着没有任务在跑
  if (t && t.id && !t.done) startPolling()
})

// ---- 轮询任务进度 ----
//
// 任务在服务端跑（可能几分钟），界面只负责把它的状态取回来。
// 1.5 秒一次：足够让进度条看起来是在动，又不会把管理接口打满。
let pollTimer: ReturnType<typeof setInterval> | null = null

function startPolling() {
  stopPolling()
  pollTimer = setInterval(async () => {
    const t = await store.pollProgress()
    // 只在「有一个带 id 的真实任务且它结束了」时才收尾 ——
    // 后端在没有任务时返回的是 idle 占位对象（无 id），
    // 把它也当成「刚结束」会让面板凭空显示一次成功提示
    if (!t || !t.id || t.done) {
      stopPolling()
      // 任务结束时刷新版本信息：重启之后 build_type 之外的字段
      // （date、commit）本就会变
      void store.fetchInfo()
      if (t && t.id && t.done && !t.failed) {
        justFinished.value = t.kind === 'rollback' ? 'rollback' : 'update'
      }
    }
  }, 1500)
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

// ---- 检测更新 ----
async function refresh(force = true) {
  applyError.value = ''
  justFinished.value = ''
  await store.fetchCheck(force)
}

// ---- 一键更新 ----
async function doUpdate() {
  if (applying.value) return
  applying.value = true
  applyError.value = ''
  justFinished.value = ''
  try {
    await store.startUpdate()
    startPolling()
  } catch (e: any) {
    applyError.value = e?.message || '启动更新失败'
  } finally {
    applying.value = false
  }
}

// ---- 重启 ----
//
// 后端在回响应之后才退出进程，所以这里通常能拿到响应；
// 拿不到也是正常的（重启成功了，只是连接断了）。
// 倒计时只是「最长还要等多久」的展示；探活立刻开始 ——
// 容器/systemd 通常 1-2 秒就把服务拉起来，回来即刷新，
// 不必白等满 8 秒。
let restartTimer: ReturnType<typeof setInterval> | null = null

async function doRestart() {
  if (restarting.value) return
  restarting.value = true
  restartCountdown.value = 8

  await store.restart()

  restartTimer = setInterval(() => {
    restartCountdown.value -= 1
    if (restartCountdown.value <= 0 && restartTimer) {
      clearInterval(restartTimer)
      restartTimer = null
    }
  }, 1000)
  void waitAndReload()
}

// 轮询健康检查直到服务回来，然后刷新页面。
// 最多等 30 秒：超过这个时间说明不是「还在启动」而是「起不来了」，
// 那时候继续转圈只会让人以为在等一个即将出现的结果。
async function waitAndReload() {
  for (let i = 0; i < 15; i++) {
    try {
      const res = await fetch('/healthz', { cache: 'no-store' })
      if (res.ok) {
        window.location.reload()
        return
      }
    } catch {
      // 服务还没起来
    }
    await new Promise((r) => setTimeout(r, 2000))
  }
  if (restartTimer) {
    clearInterval(restartTimer)
    restartTimer = null
  }
  restarting.value = false
  message.warning('服务在 30 秒内没有恢复响应，请检查容器或进程状态')
}

// ---- 回滚 ----
async function toggleRollback() {
  rollbackOpen.value = !rollbackOpen.value
  if (rollbackOpen.value && rollbackList.value.length === 0 && !rollbackLoading.value) {
    await loadRollback()
  }
}

async function loadRollback() {
  rollbackLoading.value = true
  rollbackError.value = ''
  try {
    const data = await versionApi.rollbackVersions()
    rollbackList.value = data.versions || []
    rollbackHasBackup.value = !!data.has_backup
  } catch (e: any) {
    rollbackError.value = e?.message || '获取可回滚版本失败'
  } finally {
    rollbackLoading.value = false
  }
}

async function doRollback(version?: string) {
  if (rollingBack.value) return
  rollingBack.value = true
  rollbackError.value = ''
  try {
    await store.rollback(version)
    rollbackOpen.value = false
    startPolling()
  } catch (e: any) {
    rollbackError.value = e?.message || '回滚失败'
  } finally {
    rollingBack.value = false
  }
}

// 本地回滚提示：不联网，任何情况下都能用，所以单独给一条。
// 与「下载指定版本」相比它可靠得多，界面上的措辞也要体现这一点。
//
// 两条文案都写明了**前提条件**，因为它们各自依赖一份「上一个版本」的记录：
// 二进制形态靠同目录下的 .backup 文件，容器形态靠更新器内存里记下的
// 上一个镜像 ID。刚部署完还没更新过时这份记录是空的，此时按钮会失败 ——
// 提前一句话说清，比让用户点一下再收到报错要好。
const localRollbackHint = computed(() => {
  if (buildType.value === 'binary') {
    return '把上一版本的程序文件换回来。不联网，因此在 GitHub 不可达时依然可用。'
  }
  return '由宿主侧更新器把容器切回上一个镜像（需要此前执行过一次更新）。'
})

const dockerRollbackCommand = computed(() => {
  if (!selectedVersion.value) return ''
  return [
    `# 在服务器上编辑 deploy/docker-compose.yml，把镜像固定到该版本：`,
    `#   image: ghcr.io/ashu1800/llm-relay:${selectedVersion.value}`,
    `# 然后重建容器：`,
    `cd /opt/llm-relay/deploy && docker compose up -d --no-deps app`
  ].join('\n')
})

async function copyCommand() {
  const ok = await writeClipboard(dockerRollbackCommand.value)
  copied.value = ok
  if (ok) setTimeout(() => (copied.value = false), 2000)
}

function formatTime(s: string): string {
  if (!s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleDateString()
}

// 阶段名 → 中文。服务端的 phase 是稳定的英文标识（便于日志检索），
// 界面上要给人看，所以在这里翻译。
const phaseText = computed(() => {
  const p = task.value?.phase || ''
  const map: Record<string, string> = {
    prepare: '准备中',
    checking: '正在检查版本',
    downloading: '正在下载',
    verifying: '正在校验文件',
    installing: '正在安装',
    requested: '已请求宿主侧更新器',
    pull: '正在拉取镜像',
    recreate: '正在重建容器',
    wait: '正在等待服务就绪',
    rolling_back: '正在回滚',
    done: '已完成',
    failed: '失败'
  }
  return map[p] || p
})
</script>

<template>
  <div class="version-badge">
    <!-- 徽标本体。收起侧栏时不渲染（那一列只有 40px，塞不下） -->
    <a-popover
      v-model:open="open"
      trigger="click"
      placement="bottomLeft"
      overlay-class-name="vb-popover"
    >
      <button
        class="version-badge-trigger"
        :class="badgeClass"
        :title="hasUpdate ? '有新版本可用，点击查看' : '当前版本：' + fullVersion"
        :aria-expanded="open"
      >
        <span v-if="display" class="version-text">{{ display }}</span>
        <span v-else class="version-skeleton" aria-hidden="true"></span>
        <!-- 有更新时的呼吸圆点。只在「能更新」时亮，理由见 store 的 shouldNotify -->
        <span v-if="store.shouldNotify" class="version-dot" aria-hidden="true">
          <span class="version-dot-ping"></span>
          <span class="version-dot-core"></span>
        </span>
      </button>

      <template #content>
        <div class="version-panel" role="dialog" aria-label="版本与更新">
          <!-- 头部：标题 + 刷新 -->
          <div class="vb-head">
            <span class="vb-title">版本与更新</span>
            <button
              class="vb-icon-btn"
              :disabled="store.checkLoading"
              title="重新检测（跳过服务端缓存）"
              @click="refresh(true)"
            >
              <ReloadOutlined :spin="store.checkLoading" />
            </button>
          </div>

          <div class="vb-body">
            <!-- 当前版本，永远是第一个信息 -->
            <div class="vb-current">
              <div class="vb-current-row">
                <span class="vb-version">{{ display }}</span>
                <!-- 绿色对勾只在「确实检测过、且确实是最新」时出现。
                     检测没成功却打一个对勾，是在告诉用户一个我们并不知道的
                     结论 —— 实测踩过：仓库还没有任何发布时，面板一边显示
                     「检测未完成」的警告，一边在版本号旁边打勾并写着
                     「已是最新版本」，两句话互相矛盾。 -->
                <CheckCircleOutlined v-if="upToDateConfirmed" class="vb-ok" />
              </div>
              <div class="vb-sub">
                <template v-if="hasUpdate">最新版本：{{ checkResult?.latest }}</template>
                <template v-else-if="store.checkLoading">正在检测…</template>
                <template v-else-if="store.checkError">检测失败</template>
                <!-- 检测跑通了但结果不可信（仓库不存在、没有发布记录、
                     GitHub 限额用尽…）：如实说「没能确认」，而不是
                     替用户下一个「已是最新」的结论 -->
                <template v-else-if="checkResult?.warning">未能确认是否最新</template>
                <template v-else>已是最新版本</template>
              </div>
              <div class="vb-meta">
                <span>{{ buildType === 'docker' ? '容器部署' : buildType === 'binary' ? '二进制部署' : '源码构建' }}</span>
                <span v-if="info?.commit && info.commit !== 'unknown'"> · {{ info.commit }}</span>
              </div>
            </div>

            <!-- ① 检测本身的错误 / 更新失败的原因。
                 必须排在「有更新」之前：否则重试入口会被更新按钮盖掉 -->
            <div v-if="store.checkError || applyError" class="vb-alert is-error">
              <ExclamationCircleOutlined class="vb-alert-icon" />
              <div class="vb-alert-body">
                <strong>操作未成功</strong>
                <p>{{ applyError || store.checkError }}</p>
              </div>
            </div>
            <div v-else-if="checkResult?.warning" class="vb-alert is-warn">
              <ExclamationCircleOutlined class="vb-alert-icon" />
              <div class="vb-alert-body">
                <strong>检测未完成</strong>
                <p>{{ checkResult.warning }}</p>
              </div>
            </div>

            <!-- ② 任务进行中 -->
            <div v-if="store.taskRunning" class="vb-task">
              <div class="vb-task-head">
                <SyncOutlined spin />
                <span>{{ phaseText }}</span>
                <span class="vb-task-pct">{{ task?.percent }}%</span>
              </div>
              <div class="vb-progress">
                <div class="vb-progress-fill" :style="{ width: (task?.percent || 0) + '%' }"></div>
              </div>
              <p class="vb-task-msg">{{ task?.message }}</p>
              <button class="vb-btn is-ghost" @click="store.cancelTask()">取消</button>
            </div>

            <!-- ③ 任务刚结束：成功 → 提示重启；失败 → 给原因 + 回滚入口。
                 条件是「有 id 且已结束」——没有 id 的是后端在无任务时
                 返回的 idle 占位对象，它不该被渲染成一次「已完成的任务」 -->
            <div v-else-if="task && task.id && task.done" class="vb-result">
              <div class="vb-alert" :class="task.failed ? 'is-error' : 'is-ok'">
                <CheckCircleOutlined v-if="!task.failed" class="vb-alert-icon" />
                <ExclamationCircleOutlined v-else class="vb-alert-icon" />
                <div class="vb-alert-body">
                  <strong>{{ task.failed ? '未完成' : justFinished === 'rollback' ? '回滚完成' : '更新完成' }}</strong>
                  <p>{{ task.message }}</p>
                </div>
              </div>
              <!-- 成功后要重启才生效。binary 形态是必须的；docker 形态
                   宿主侧更新器已经重建过容器，这一步是兜底 -->
              <button
                v-if="!task.failed"
                class="vb-btn is-primary"
                :disabled="restarting"
                @click="doRestart"
              >
                <SyncOutlined v-if="restarting" spin />
                <ReloadOutlined v-else />
                <span v-if="restarting">正在重启…（{{ restartCountdown }}s）</span>
                <span v-else>立即重启以生效</span>
              </button>
              <button v-else class="vb-btn is-ghost" @click="refresh(true)">重新检测</button>
            </div>

            <!-- ④ 有更新且可以更新 -->
            <template v-else-if="hasUpdate && canApply">
              <div class="vb-alert is-update">
                <DownloadOutlined class="vb-alert-icon" />
                <div class="vb-alert-body">
                  <strong>发现新版本 v{{ checkResult?.latest }}</strong>
                  <p>{{ modeLabel }}</p>
                </div>
              </div>
              <button class="vb-btn is-primary" :disabled="applying" @click="doUpdate">
                <SyncOutlined v-if="applying" spin />
                <DownloadOutlined v-else />
                {{ applying ? '正在启动…' : '立即更新' }}
              </button>
              <a
                v-if="checkResult?.release?.html_url"
                class="vb-link"
                :href="checkResult.release.html_url"
                target="_blank"
                rel="noopener noreferrer"
              >
                查看更新日志
              </a>
            </template>

            <!-- ⑤ 有更新但不能自动更新：把原因说清楚，并给一条出路 -->
            <template v-else-if="hasUpdate && !canApply">
              <div class="vb-alert is-warn">
                <ExclamationCircleOutlined class="vb-alert-icon" />
                <div class="vb-alert-body">
                  <strong>有新版本 v{{ checkResult?.latest }}，但无法一键更新</strong>
                  <p>{{ checkResult?.blocked_reason || '当前部署方式不支持自动更新' }}</p>
                </div>
              </div>
              <a
                v-if="checkResult?.release?.html_url"
                class="vb-btn is-ghost"
                :href="checkResult.release.html_url"
                target="_blank"
                rel="noopener noreferrer"
              >
                <GithubOutlined />
                前往发布页
              </a>
            </template>

            <!-- ⑥ 已是最新：给发布页链接 -->
            <template v-else>
              <a
                v-if="checkResult?.release?.html_url"
                class="vb-link"
                :href="checkResult.release.html_url"
                target="_blank"
                rel="noopener noreferrer"
              >
                <GithubOutlined />
                查看发布记录
              </a>
            </template>

            <!-- ⑦ 回滚入口。放在所有状态分支之外，因为「更新失败」时
                 它才是最该出现的东西。

                 原来它只在「已是最新」那一支里 —— 结果是更新一失败，
                 回滚入口就消失了，而那一刻用户正需要它。实测踩到过：
                 宿主侧更新器报错后面板里只剩一句「重新检测」。

                 任务运行中不显示（那时点了也不会执行，只会收到 409）。
                 默认收起：它是一个低频且危险的动作，不该和「检测更新」
                 争夺注意力。 -->
            <template v-if="!store.taskRunning">
              <div class="vb-divider"></div>

              <button class="vb-collapse" @click="toggleRollback">
                <RollbackOutlined />
                <span>回滚到旧版本</span>
                <span class="vb-chevron" :class="{ 'is-open': rollbackOpen }">›</span>
              </button>

              <div v-if="rollbackOpen" class="vb-rollback">
                <!-- 本地回滚：最可靠的一条路（不联网） -->
                <div v-if="rollbackHasBackup || buildType === 'docker'" class="vb-local-rb">
                  <p class="vb-hint">{{ localRollbackHint }}</p>
                  <button class="vb-btn is-warn" :disabled="rollingBack" @click="doRollback()">
                    <SyncOutlined v-if="rollingBack" spin />
                    <RollbackOutlined v-else />
                    {{ rollingBack ? '正在回滚…' : '回滚到上一版本' }}
                  </button>
                </div>

                <div class="vb-divider"></div>

                <p class="vb-hint">或选择下载一个具体的旧版本：</p>

                <div v-if="rollbackLoading" class="vb-loading"><SyncOutlined spin /> 正在获取…</div>

                <div v-else-if="rollbackError" class="vb-alert is-error">
                  <ExclamationCircleOutlined class="vb-alert-icon" />
                  <div class="vb-alert-body">
                    <p>{{ rollbackError }}</p>
                  </div>
                </div>

                <p v-else-if="rollbackList.length === 0" class="vb-hint is-center">
                  没有可回滚的版本（只列出比当前版本旧、且非预发布的正式版本）
                </p>

                <template v-else>
                  <button
                    v-for="item in rollbackList"
                    :key="item.version"
                    class="vb-rb-item"
                    :class="{ 'is-selected': selectedVersion === item.version }"
                    :disabled="rollingBack"
                    @click="selectedVersion = selectedVersion === item.version ? '' : item.version"
                  >
                    <span class="vb-radio" :class="{ 'is-on': selectedVersion === item.version }"></span>
                    <span class="vb-rb-ver">v{{ item.version }}</span>
                    <span class="vb-rb-date">{{ formatTime(item.published_at) }}</span>
                  </button>

                  <!-- 选中后给出确认与命令。
                       命令是给「自动更新不可用」的部署留的出路 ——
                       那种情况下界面上的按钮点了也不会生效（没有更新器），
                       用户需要的是能复制走的东西 -->
                  <div v-if="selectedVersion" class="vb-rb-confirm">
                    <template v-if="canApply">
                      <button
                        class="vb-btn is-warn"
                        :disabled="rollingBack"
                        @click="doRollback(selectedVersion)"
                      >
                        <SyncOutlined v-if="rollingBack" spin />
                        <RollbackOutlined v-else />
                        {{ rollingBack ? '正在回滚…' : `回滚到 v${selectedVersion}` }}
                      </button>
                    </template>
                    <template v-else>
                      <div class="vb-code">
                        <button class="vb-copy" @click="copyCommand">
                          {{ copied ? '已复制' : '复制' }}
                        </button>
                        <code>{{ dockerRollbackCommand }}</code>
                      </div>
                    </template>
                    <p class="vb-warn-text">
                      <ExclamationCircleOutlined />
                      回滚会替换当前程序，完成后需要重启服务。
                    </p>
                  </div>
                </template>
              </div>
            </template>
          </div>
        </div>
      </template>
    </a-popover>
  </div>
</template>

<style>
/* popover 的 overlay 渲染在 body 下（teleport），scoped 样式够不到，
   所以这段必须是全局的。内容自己带标题栏与滚动结构，
   antd 默认的内边距只会添乱。 */
.vb-popover .ant-popover-inner {
  padding: 0;
  overflow: hidden;
}
</style>

<style scoped>
/* ---------- 徽标 ---------- */
.version-badge {
  position: relative;
  align-self: flex-start;
}

.version-badge-trigger {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-pill);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: var(--font-family-mono);
  font-size: 11px;
  line-height: 1.3;
  cursor: var(--cursor-hand);
  transition: background 0.2s var(--ease-expo), color 0.2s var(--ease-expo),
    border-color 0.2s var(--ease-expo);
}

.version-badge-trigger:hover {
  color: var(--text-primary-ink);
  border-color: var(--text-primary-ink);
}

/* 有更新时整枚徽标换成琥珀色：这是全站唯一一处「需要你去处理」的提示，
   用与其它状态都不同的颜色才有辨识度 */
.version-badge-trigger.is-update {
  color: var(--text-amber);
  border-color: var(--text-amber);
  background: color-mix(in oklab, var(--color-orange) 14%, transparent);
}

.version-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.version-skeleton {
  display: inline-block;
  width: 42px;
  height: 11px;
  border-radius: var(--radius-pill);
  background: var(--color-border);
}

.version-dot {
  position: relative;
  display: inline-flex;
  width: 7px;
  height: 7px;
  flex: 0 0 7px;
}

.version-dot-ping,
.version-dot-core {
  position: absolute;
  inset: 0;
  border-radius: 50%;
}

.version-dot-ping {
  background: var(--color-orange);
  opacity: 0.7;
  animation: vb-ping 1.6s cubic-bezier(0, 0, 0.2, 1) infinite;
}

.version-dot-core {
  background: var(--color-orange);
}

@keyframes vb-ping {
  0% { transform: scale(1); opacity: 0.7; }
  75%, 100% { transform: scale(2.2); opacity: 0; }
}

/* 尊重系统的「减少动态效果」：呼吸圆点对动效敏感的人是不适源，
   而它承载的信息（有更新）同时也由颜色表达，去掉动画不丢信息 */
@media (prefers-reduced-motion: reduce) {
  .version-dot-ping { animation: none; opacity: 0; }
}

/* ---------- 面板 ---------- */
/* 定位、翻转、视口钳制都交给 popover；这里只负责外观与尺寸。
   超长内容在正文内部滚动，面板本身不无限长。 */
.version-panel {
  display: flex;
  flex-direction: column;
  width: 300px;
  max-width: calc(100vw - 16px);
}

.vb-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 10px;
  border-bottom: 1px solid var(--color-border);
  /* 标题栏固定高度、不参与压缩：正文滚动时不把标题挤扁 */
  flex: 0 0 auto;
}

.vb-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--color-text);
}

.vb-icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: var(--radius-control);
  background: transparent;
  color: var(--color-text-secondary);
  cursor: var(--cursor-hand);
}

.vb-icon-btn:hover:not(:disabled) {
  background: var(--color-icon-hover-bg);
}

.vb-icon-btn:disabled {
  cursor: default;
  opacity: 0.5;
}

/* 正文是唯一可滚动的部分。min-height: 0 是必需的 ——
   flex 子项的默认 min-height 是 auto，它会拒绝收缩到内容高度以下，
   于是 max-height 在父级上生效了、子项却把内容整个撑出面板外，
   表现为「内容溢出了但滚不动」。 */
.vb-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px;
  max-height: min(420px, 65vh);
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
}

/* 当前版本区 */
.vb-current {
  text-align: center;
}

.vb-current-row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.vb-version {
  font-family: var(--font-family-mono);
  font-size: 20px;
  font-weight: 700;
  color: var(--color-text);
}

.vb-ok {
  color: var(--color-green);
  font-size: 14px;
}

.vb-sub {
  margin-top: 2px;
  font-size: 12px;
  color: var(--color-text-secondary);
}

.vb-meta {
  margin-top: 2px;
  font-size: 11px;
  color: var(--color-text-secondary);
  opacity: 0.75;
}

/* 提示条 */
.vb-alert {
  display: flex;
  gap: 8px;
  padding: 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  font-size: 12px;
}

.vb-alert-icon {
  margin-top: 2px;
  flex: 0 0 auto;
}

.vb-alert-body strong {
  display: block;
  margin-bottom: 2px;
  font-size: 12px;
}

.vb-alert-body p {
  margin: 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-secondary);
  word-break: break-word;
}

.vb-alert.is-error {
  border-color: color-mix(in oklab, var(--color-red) 45%, transparent);
  background: color-mix(in oklab, var(--color-red) 10%, transparent);
  color: var(--text-red);
}

.vb-alert.is-warn,
.vb-alert.is-update {
  border-color: color-mix(in oklab, var(--color-orange) 45%, transparent);
  background: color-mix(in oklab, var(--color-orange) 10%, transparent);
  color: var(--text-amber);
}

.vb-alert.is-ok {
  border-color: color-mix(in oklab, var(--color-green) 45%, transparent);
  background: color-mix(in oklab, var(--color-green) 10%, transparent);
  color: var(--text-green);
}

/* 按钮 */
.vb-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  padding: 6px 10px;
  border: 1px solid transparent;
  border-radius: var(--radius-control);
  font-family: inherit;
  font-size: 12px;
  cursor: var(--cursor-hand);
  transition: background 0.2s var(--ease-expo);
}

.vb-btn:disabled {
  cursor: default;
  opacity: 0.6;
}

.vb-btn.is-primary {
  background: var(--color-primary);
  color: #fff;
}

.vb-btn.is-primary:hover:not(:disabled) {
  background: var(--text-primary-ink);
}

.vb-btn.is-warn {
  background: var(--color-orange);
  color: #2a1a00;
}

.vb-btn.is-warn:hover:not(:disabled) {
  filter: brightness(1.08);
}

.vb-btn.is-ghost {
  border-color: var(--color-border);
  background: transparent;
  color: var(--color-text);
  text-decoration: none;
}

.vb-btn.is-ghost:hover:not(:disabled) {
  background: var(--color-icon-hover-bg);
}

.vb-link {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  font-size: 11px;
  color: var(--color-text-secondary);
  text-decoration: none;
}

.vb-link:hover {
  color: var(--text-primary-ink);
}

/* 任务进度 */
.vb-task {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.vb-task-head {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--color-text);
}

.vb-task-pct {
  margin-left: auto;
  font-family: var(--font-family-mono);
  font-size: 11px;
  color: var(--color-text-secondary);
}

.vb-progress {
  height: 4px;
  overflow: hidden;
  border-radius: var(--radius-pill);
  background: var(--color-border);
}

.vb-progress-fill {
  height: 100%;
  border-radius: var(--radius-pill);
  background: var(--color-primary);
  transition: width 0.4s var(--ease-expo);
}

.vb-task-msg {
  margin: 0;
  font-size: 11px;
  color: var(--color-text-secondary);
}

.vb-result {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* 分隔与折叠 */
.vb-divider {
  height: 1px;
  background: var(--color-border);
}

.vb-collapse {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 4px 2px;
  border: none;
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 11px;
  cursor: var(--cursor-hand);
}

.vb-collapse:hover {
  color: var(--text-primary-ink);
}

.vb-chevron {
  margin-left: auto;
  transition: transform 0.2s var(--ease-expo);
}

.vb-chevron.is-open {
  transform: rotate(90deg);
}

.vb-rollback {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-top: 4px;
}

.vb-local-rb {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.vb-hint {
  margin: 0;
  font-size: 11px;
  line-height: 1.5;
  color: var(--color-text-secondary);
}

.vb-hint.is-center {
  text-align: center;
  padding: 6px 0;
}

.vb-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 8px;
  font-size: 11px;
  color: var(--color-text-secondary);
}

.vb-rb-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: transparent;
  font-family: inherit;
  font-size: 12px;
  color: var(--color-text);
  cursor: var(--cursor-hand);
}

.vb-rb-item.is-selected {
  border-color: var(--color-orange);
  background: color-mix(in oklab, var(--color-orange) 12%, transparent);
}

.vb-radio {
  width: 10px;
  height: 10px;
  flex: 0 0 10px;
  border: 1px solid var(--color-border);
  border-radius: 50%;
}

.vb-radio.is-on {
  border-color: var(--color-orange);
  background: var(--color-orange);
  box-shadow: inset 0 0 0 2px var(--color-fg);
}

.vb-rb-ver {
  font-family: var(--font-family-mono);
  font-weight: 600;
}

.vb-rb-date {
  margin-left: auto;
  font-size: 10px;
  color: var(--color-text-secondary);
}

.vb-rb-confirm {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.vb-warn-text {
  display: flex;
  align-items: flex-start;
  gap: 4px;
  margin: 0;
  font-size: 10px;
  line-height: 1.5;
  color: var(--text-amber);
}

/* 命令块：给「自动更新不可用」的部署留的出路 */
.vb-code {
  position: relative;
  padding: 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-bg);
}

.vb-code code {
  display: block;
  font-family: var(--font-family-mono);
  font-size: 10px;
  line-height: 1.6;
  color: var(--color-text-secondary);
  white-space: pre-wrap;
  word-break: break-all;
  user-select: all;
}

.vb-copy {
  position: absolute;
  top: 4px;
  right: 4px;
  padding: 1px 6px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-control);
  background: var(--color-fg);
  font-family: inherit;
  font-size: 10px;
  color: var(--color-text-secondary);
  cursor: var(--cursor-hand);
}

.vb-copy:hover {
  color: var(--text-primary-ink);
}
</style>
