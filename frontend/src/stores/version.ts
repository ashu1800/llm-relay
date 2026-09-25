import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { versionApi, type UpdateCheck, type UpdateTask, type VersionInfo } from '@/api/version'

// 版本状态。两件事分得很清楚，混在一起会让最基本的信息依赖于最易失败的环节：
//
//   info  当前版本号与构建形态 —— 来自 /system/version，**不发任何网络请求**，
//         所以它永远可用（GitHub 被墙、限额用尽都不影响它）。
//         侧栏那枚徽标显示的就是它。
//   check 有没有新版本 —— 来自 /system/update/check，要访问 GitHub，
//         会失败、会被缓存、会限额。失败时只影响「有没有黄点」。
//
// 缓存策略与 Sub2api 一致：内存缓存 + force 穿透，不落 localStorage
// （版本检查的结果几分钟就可能过期，存下来只会在下次打开时显示一个
// 早已不成立的「有新版本」）。不做定时轮询 —— 服务端自己缓存 20 分钟，
// 而「有没有新版本」这件事不值得让管理台每隔几分钟打一次 GitHub。
export const useVersionStore = defineStore('version', () => {
  // ---- 当前版本（永远可用）----
  const info = ref<VersionInfo | null>(null)
  const infoLoaded = ref(false)
  const infoLoading = ref(false)

  // ---- 更新检测（依赖网络）----
  const check = ref<UpdateCheck | null>(null)
  const checkLoading = ref(false)
  const checkError = ref('')
  // 「检测跑过一次」由结果推导：要么拿到了结果，要么留下了错误。
  // 单独存一个 ref 的话，它与这两个状态的手工置位迟早失步。
  const checkedOnce = computed(() => check.value !== null || checkError.value !== '')

  // ---- 进行中的任务 ----
  const task = ref<UpdateTask | null>(null)
  // 正在跑的任务。
  //
  // 三个条件缺一不可：有 id（idle 的空任务没有）、未结束、且不是
  // 终态阶段。只看 done 是不够的 —— 后端在「任务已结束但界面还没
  // 轮询到」的窗口里可能返回一个 phase 仍是 done/failed 的对象，
  // 而那种状态该显示结果而不是进度条。
  const taskRunning = computed(() => {
    const t = task.value
    if (!t || !t.id) return false
    if (t.done) return false
    return t.phase !== 'done' && t.phase !== 'failed' && t.phase !== 'idle'
  })

  // 有更新且**能更新** —— 黄点只在真的能做点什么时才亮。
  // 源码构建下即便有新版本也不亮点：那会持续诱导用户去点一个
  // 只能跳到 GitHub 的提示，久了就被无视，等于把提醒功能废掉。
  const hasUpdate = computed(() => !!check.value?.has_update)
  const canApply = computed(() => !!check.value?.can_apply)
  const shouldNotify = computed(() => hasUpdate.value && canApply.value)

  const buildType = computed(() => info.value?.build_type || 'source')

  /** 拉取当前版本信息。不访问网络，所以失败通常是「后端没起来」。 */
  async function fetchInfo() {
    if (infoLoading.value) return
    infoLoading.value = true
    try {
      info.value = await versionApi.info()
      infoLoaded.value = true
    } catch {
      // 拿不到就整个不显示版本徽标：空着一格比显示一个假版本号
      // 或一条错误更容易让人判断「是这里坏了」而不是「版本是空的」
      infoLoaded.value = true
    } finally {
      infoLoading.value = false
    }
  }

  /**
   * 检测更新。
   * @param force 跳过服务端缓存
   */
  async function fetchCheck(force = false) {
    if (checkLoading.value) return
    checkLoading.value = true
    checkError.value = ''
    try {
      check.value = await versionApi.check(force)
    } catch (e: any) {
      checkError.value = e?.message || '检测更新失败'
    } finally {
      checkLoading.value = false
    }
  }

  /** 启动一次更新，返回任务。失败时抛出（由界面显示原因）。 */
  async function startUpdate(mode?: string): Promise<UpdateTask> {
    const t = await versionApi.start(mode)
    task.value = t
    // 更新之后「有没有新版本」的答案就变了，清掉缓存
    clearCheck()
    return t
  }

  /** 回滚。version 留空 = 本地回退到上一版（不联网）。 */
  async function rollback(version?: string): Promise<UpdateTask> {
    const t = await versionApi.rollback(version)
    task.value = t
    clearCheck()
    return t
  }

  /** 查询进度并更新本地任务状态。 */
  async function pollProgress() {
    try {
      const t = await versionApi.progress(task.value?.id)
      // 后端在没有任务时会返回一个 phase=idle 的空任务。
      // 它不能当成「有任务」，否则界面会显示一个 0% 的进度条
      // 与一个永远不出现的完成状态。
      if (t && t.phase === 'idle' && !t.id) {
        return task.value
      }
      task.value = t
      return t
    } catch {
      // 轮询失败不清空任务：网络抖动或后端正在重启都会让这一跳失败，
      // 而「刚才那个任务跑到哪了」是此刻最有价值的信息
      return task.value
    }
  }

  /** 取消当前任务。 */
  async function cancelTask() {
    await versionApi.cancel()
    await pollProgress()
  }

  /** 清掉检测结果（更新/回滚后调用）。错误一并清掉，checkedOnce 才会归零。 */
  function clearCheck() {
    check.value = null
    checkError.value = ''
  }

  /** 重启服务。这个请求大概率拿不到响应（进程会退出），所以吞掉错误。 */
  async function restart(): Promise<void> {
    try {
      await versionApi.restart()
    } catch {
      // 连接被重置是预期内的：进程在响应发出后就退出了
    }
  }

  return {
    // state
    info,
    infoLoaded,
    infoLoading,
    check,
    checkLoading,
    checkError,
    task,
    // getters
    checkedOnce,
    taskRunning,
    hasUpdate,
    canApply,
    shouldNotify,
    buildType,
    // actions
    fetchInfo,
    fetchCheck,
    startUpdate,
    rollback,
    pollProgress,
    cancelTask,
    clearCheck,
    restart
  }
})
