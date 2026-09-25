// 版本与更新的 API 封装。
//
// 与其它 view 的调用方式一致（都走 api client），但这里额外做两件事：
//
//  1. 更新与回滚是**异步任务**，所以接口分成「启动」与「查询进度」两组，
//     而不是一个会阻塞几分钟的同步调用。
//  2. 全局限时 15 秒对更新接口是不够的 —— 虽然启动任务是立即返回的，
//     但检测更新要真的访问一次 GitHub（国内可能十几秒）。所以检测
//     单独给 30 秒，其余沿用默认。
import { api } from './client'

/** 服务端的构建形态。两种形态的更新方式完全不同，见后端 version 包。 */
export type BuildType = 'source' | 'binary'

/** 版本信息（/system/version）。这个接口不访问网络，永远可用。 */
export interface VersionInfo {
  version: string
  /** 界面上显示的短版本号（已剥掉 git 哈希与 -dirty） */
  display: string
  commit: string
  date: string
  build_type: BuildType
  go_version: string
  os: string
  arch: string
  is_release: boolean
}

export interface ReleaseInfo {
  tag_name: string
  name: string
  body: string
  published_at: string
  html_url: string
  prerelease: boolean
}

/** 检测更新的结果（/system/update/check）。 */
export interface UpdateCheck {
  current: string
  latest: string
  has_update: boolean
  build_type: BuildType
  /** 当前形态下能否一键更新 —— 与 has_update 是两个独立的问题 */
  can_apply: boolean
  /** 即将采用的更新方式：binary / manual */
  apply_mode: string
  /** can_apply 为 false 时说明原因，可直接显示 */
  blocked_reason?: string
  release?: ReleaseInfo
  /** 检测本身出问题时的说明（网络不通、GitHub 限额用尽） */
  warning?: string
  cached: boolean
  checked_at: string
}

export interface UpdateTask {
  id: string
  kind: 'update' | 'rollback' | string
  target: string
  phase: string
  percent: number
  message: string
  started_at: string
  done: boolean
  failed: boolean
  logs?: string[]
}

export interface RollbackCandidate {
  version: string
  published_at: string
  html_url: string
}

export interface RollbackVersions {
  versions: RollbackCandidate[]
  /** 是否存在可本地回滚的备份（不联网即可退回上一版） */
  has_backup: boolean
}

export interface UpdateConfig {
  enabled: boolean
  repo: string
  proxy: string
  has_token: boolean
  repo_default: string
}

// CHECK_TIMEOUT_MS 比默认 15 秒宽：检测更新要真的访问 api.github.com，
// 而国内直连时这个请求经常要十几秒才回来（或超时）。
// 用默认值会让「网络慢」表现成「检测失败」，而它其实只是慢。
const CHECK_TIMEOUT_MS = 30_000

export const versionApi = {
  /** 当前版本信息（不发网络请求） */
  info: () => api.get<VersionInfo>('/system/version'),

  /** 检测更新。force=true 跳过服务端 20 分钟缓存 */
  check: (force = false) =>
    api.get<UpdateCheck>(`/system/update/check${force ? '?force=true' : ''}`, CHECK_TIMEOUT_MS),

  /** 启动一次更新。mode 留空则按构建形态自动判定 */
  start: (mode?: string) => api.post<UpdateTask>('/system/update', { mode: mode || '' }),

  /** 查询进度。taskId 留空时返回当前/最近一次任务 */
  progress: (taskId?: string) =>
    api.get<UpdateTask>(`/system/update/progress${taskId ? `?task=${encodeURIComponent(taskId)}` : ''}`),

  /** 取消正在进行的任务 */
  cancel: () => api.post<{ message: string }>('/system/update/cancel', {}),

  /** 可回滚版本列表。要访问 GitHub，与检测更新用同一个放宽的超时 */
  rollbackVersions: () =>
    api.get<RollbackVersions>('/system/update/rollback-versions', CHECK_TIMEOUT_MS),

  /** 执行回滚。version 留空 = 本地回退到上一版（不联网） */
  rollback: (version?: string) =>
    api.post<UpdateTask>('/system/update/rollback', { version: version || '' }),

  /** 重启服务以应用更新 */
  restart: () => api.post<{ message: string }>('/system/restart', {}),

  /** 读取更新配置 */
  getConfig: () => api.get<UpdateConfig>('/system/update/config'),

  /**
   * 保存更新配置。
   *
   * token 的三态是有意的，后端按它区分三种意图：
   *   - 字段缺失（undefined）：不修改已配置的 token
   *   - 空串 `''`：清除已配置的 token
   *   - 非空字符串：设置为该值
   *
   * 注意「清除」必须用空串，**不能用 null**：Go 把 JSON 的 null 与
   * 「字段不存在」都解析成 nil 指针，两者在服务端无法区分，
   * 于是 `null` 会被当成「不修改」，清除操作静默失效。
   */
  saveConfig: (cfg: {
    enabled: boolean
    repo: string
    proxy: string
    token?: string
  }) => api.put<{ message: string }>('/system/update/config', cfg)
}
