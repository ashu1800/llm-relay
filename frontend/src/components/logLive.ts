// 实时推送来的新日志该怎么处理 —— 纯逻辑，不碰 DOM、不碰 Vue 响应式。
//
// 抽出来的动机与 components/logColumns.ts 同款：这段判断原本埋在
// RequestLogPanel 的 onLive('logs') 回调里，而它是「列表当前视角能不能被
// 一条推送直接改」的全部依据 —— 判错的后果是静默的（列表看起来正常，
// 只是顺序或页码不对了），只有单测能钉住。
//
// ---- 三种处置，各自的理由 ----
//
// 'insert'：直接把新行插到第一行。这是默认视角（最新在前、第 1 页、
//   无排序、无排障深链、无工具栏筛选）下的正确做法，也是这个面板存在的理由。
//
// 'reload'：不动 DOM，改为按当前视角静默重取一次。三种情况：
//   · 排障深链（?trace_id= / ?status_class=）生效时 —— 新日志符不符合
//     条件只有服务端知道，客户端不去重复实现一遍筛选语义；
//   · 看板工具栏筛选生效时（2026-10-26 列表纳入筛选后新增）—— 理据与
//     深链相同：这条推送属不属于当前筛选（渠道/密钥/模型）不该由客户端
//     复制一份判断；直接 unshift 会把不匹配的行插进列表，筛选视图被
//     静默污染（列表内容与筛选框矛盾）。放在页码判断之前：筛选生效时
//     第 3 页的「这一页仍然符合筛选」同样只有重取能保证。
//   · **有排序时**（2026-10-11 修）。这是原来漏掉的一条：`sort` 由服务端做
//     （sort=-elapsed 之类），而 insertFresh 是无条件 unshift 到首行 ——
//     于是「耗时降序」被新来的任意一条日志插到最前面，排序静默失效。
//     更糟的是排序偏好会持久化（SORT_KEY = 'log-sort'），用户刷新页面后
//     立刻又看到一张错序的表，而界面上没有任何东西提示「顺序已被打乱」。
//     判据放在这里而不是在组件里 if 一下：它有多种组合要测。
//
// 'count'：只在第 1 页之外计数，不插也不重取。新日志按定义属于第 1 页，
//   插进第 3 页只会让这一页错位（而且分页偏移已经变了）；静默重取也没必要 ——
//   用户看的是历史页，不会因为多了一条就想让这一页跟着动。但「有 N 条新日志」
//   这件事要让他知道，所以由调用方累计并提示（见 RequestLogPanel 的
//   pendingNew / 分页提示条）。
//
// ---- 判据的优先级 ----
//
// 深链、工具栏筛选与排序都排在页码之前：它们改的是**取数条件本身**，
// 无论在第几页，唯一的正确处置都是「按当前条件重取」。反过来把页码判断
// 放前面，排序或筛选生效时在第 3 页就会退化成 'count'，那一页的次序
// 与内容仍然是错的（筛选生效时 count 的计数还会把不匹配的行算进来）。

export type LiveInsertDecision = 'insert' | 'reload' | 'count'

export interface LiveInsertState {
  /** 当前页码，1 起 */
  page: number
  /** 服务端排序参数，空串 = 默认（最新在前）。见 SORT_ALLOWED */
  sort: string
  /** 排障深链：只看这条链路 */
  traceId?: string
  /** 排障深链：只看失败/成功 */
  statusClass?: string
  /**
   * 看板工具栏筛选（渠道/密钥/模型），生效时任一非空。
   * 时间范围不用传：新日志必然落在任何时间窗的最新一端。
   */
  toolbarFilter?: { channelId?: string; model?: string; keyId?: string }
}

export function decideLiveInsert(state: LiveInsertState): LiveInsertDecision {
  // 深链条件：新日志符不符合条件只有服务端知道
  if (state.traceId || state.statusClass) return 'reload'
  // 工具栏筛选生效：同上，符不符合筛选由服务端判定（2026-10-26 列表纳入筛选）
  const tf = state.toolbarFilter
  if (tf && (tf.channelId || tf.model || tf.keyId)) return 'reload'
  // 排序生效：unshift 会破坏用户刚选的次序（且排序是持久化的，错序会一直跟着）
  if (state.sort) return 'reload'
  // 不在第一页：新行属于第 1 页，插进来只会让当前页错位
  if (state.page !== 1) return 'count'
  return 'insert'
}

/**
 * 第 1 页之外攒下的新日志提示文案。
 *
 * 只说「有 N 条」不够用 —— 用户此刻在第 3 页，需要知道**下一步该做什么**，
 * 所以文案里带上「回第 1 页」。返回空串表示不显示。
 */
export function pendingLogText(pending: number, onFirstPage: boolean): string {
  if (pending <= 0 || onFirstPage) return ''
  return `有 ${pending} 条新日志，回第 1 页查看`
}
