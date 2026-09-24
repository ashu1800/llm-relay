// 实时推送的客户端：一条 WebSocket，多个页面订阅。
//
// 为什么全局共享一条连接：看板与日志页都要实时数据，
// 各开一条的话切页时连接反复建立，服务端也要维护多份订阅与游标。
//
// 断线重连用指数退避（1s 起，上限 15s）：后端重启时，
// 一堆标签页同时以固定间隔重连会把刚起来的服务再打一遍。
import { onUnmounted, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'

type Handler = (data: any) => void

const handlers = new Map<string, Set<Handler>>()
let socket: WebSocket | null = null
let retry = 0
let reconnectTimer: number | null = null
let watchdogTimer: number | null = null

/** 多久没收到任何消息就认为连接已经死了。服务端有新日志就推，闲时也有 1s/2s 的
 *  兜底节拍，取 45s 很宽松 */
const STALE_MS = 45000

/** 连接状态：界面上用它显示「实时/已断开」，断线时数字不再跳动是正常现象 */
export const liveConnected = ref(false)

function socketURL() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return proto + '//' + location.host + '/api/admin/live'
}

function dispatch(msg: any) {
  if (!msg || typeof msg.type !== 'string') return
  const set = handlers.get(msg.type)
  if (!set) return
  set.forEach((fn) => {
    try {
      fn(msg.data)
    } catch (e) {
      // 单个订阅者出错不该影响其它订阅者（页面切走后回调里可能访问已卸载的组件）
      console.error('实时消息处理失败', e)
    }
  })
}

// 半开连接看门狗。
//
// 只靠 onclose 是不够的：休眠恢复、NAT/代理超时、网线拔掉这些情况下
// TCP 连接可以长时间停在「看起来还开着」的状态，浏览器不会派发 close，
// 于是数字静静地不再更新，界面却仍显示「实时」。
// 服务端有稳定的推送节奏，所以「一段时间一条都没收到」就是一个可靠的
// 死亡判据；此时主动 close，让 onclose 里的重连逻辑接手。
function armWatchdog() {
  disarmWatchdog()
  watchdogTimer = window.setTimeout(() => {
    watchdogTimer = null
    if (socket && socket.readyState === WebSocket.OPEN) {
      console.warn('实时连接超过 ' + STALE_MS / 1000 + ' 秒没有收到数据，按断开处理')
      socket.close()
    }
  }, STALE_MS)
}

function disarmWatchdog() {
  if (watchdogTimer !== null) {
    window.clearTimeout(watchdogTimer)
    watchdogTimer = null
  }
}

function connect() {
  // CLOSING(2) 也要挡住：此时再 new 一个会留下两条连接
  if (
    socket &&
    (socket.readyState === WebSocket.OPEN ||
      socket.readyState === WebSocket.CONNECTING ||
      socket.readyState === WebSocket.CLOSING)
  ) {
    return
  }
  let ws: WebSocket
  try {
    ws = new WebSocket(socketURL())
  } catch {
    scheduleReconnect()
    return
  }
  socket = ws
  ws.onopen = () => {
    liveConnected.value = true
    retry = 0
    armWatchdog()
  }
  ws.onmessage = (ev) => {
    // 收到任何一帧都说明链路还活着
    if (socket === ws) armWatchdog()
    try {
      dispatch(JSON.parse(ev.data))
    } catch {
      // 非 JSON 帧直接忽略：服务端只发 JSON，收到别的说明链路有问题，
      // 但也没必要因此把连接断开
    }
  }
  ws.onclose = () => {
    // 关键：只有「当前这条」连接关闭时才清理状态。
    //
    // 原来的实现无条件 socket = null，于是这个时序会出错：
    //   卸载旧页面（close #1，socket=null）-> 立刻挂载新页面（新建 #2）
    //   -> #1 的 onclose 此刻才派发 -> 把 #2 的引用清掉
    //   -> 退避重连又建了 #3，而 #2 再也无人引用、永远不会被关闭。
    // 结果是连接泄漏，且 #2/#3 同时推送导致消息被处理两次。
    if (socket !== ws) return
    socket = null
    liveConnected.value = false
    disarmWatchdog()
    // 还有人订阅才重连；没人订阅时让它断着，省得空转
    if (handlers.size > 0) scheduleReconnect()
  }
  ws.onerror = () => {
    // onerror 之后一定会跟一个 onclose，重连逻辑写在那边，避免重复调度
  }
}

function scheduleReconnect() {
  if (reconnectTimer !== null) return
  // 未登录（或会话已过期）时不重连：/api/admin/live 在服务端先过鉴权中间件，
  // 401 的握手再怎么重试也不会成功，只会以指数退避的节奏空转刷日志。
  // 登录成功是整页跳转，页面重新挂载订阅时这里会以新会话重新建连。
  const auth = useAuthStore()
  if (auth.statusLoaded && auth.enabled && !auth.authenticated) return
  const delay = Math.min(15000, 1000 * Math.pow(2, retry))
  retry++
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = null
    connect()
  }, delay)
}

/**
 * 订阅一类消息。组件卸载时自动退订，最后一个订阅者离开后关闭连接。
 */
export function onLive(type: string, fn: Handler) {
  let set = handlers.get(type)
  if (!set) {
    set = new Set()
    handlers.set(type, set)
  }
  set.add(fn)
  connect()

  onUnmounted(() => {
    const s = handlers.get(type)
    if (s) {
      s.delete(fn)
      if (s.size === 0) handlers.delete(type)
    }
    if (handlers.size === 0) {
      // 先摘掉引用再 close：这样 close 派发回来的 onclose 会发现
      // socket !== ws（socket 已是 null），不会误伤将来新建的连接。
      // 同时取消待执行的重连，否则没人订阅了还会建一条空转连接。
      const closing = socket
      socket = null
      if (reconnectTimer !== null) {
        window.clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
      retry = 0
      disarmWatchdog()
      liveConnected.value = false
      if (closing) closing.close()
    }
  })
}

/**
 * 实时推送触发的「静默重取」节流器（看板卡片与日志深链共用同一套行为）。
 *
 * 为什么不能有固定延迟（2026-09-18 站主二次反馈「入场动画播完了数字才变动」）：
 * 两个组件原来各自写着 setTimeout(1500) —— 推送到了先等 1.5 秒再重取，
 * 而入场动画 sweep 是 2s、glow 是 1.6s，数字的变动因此落在动画尾声甚至结束
 * 之后。上一次修复（a3a2341）只打通了服务端两帧的同拍，且那条路径只在
 * 「今天 + 全站」默认视角下被走到（其余视角合并不了推送、必须重取），
 * 这 1.5 秒的人为延迟就是残留的根因 —— 回归脚本当时只测了默认视角，测不到它。
 *
 * 所以这里的第一原则：**首帧立即执行**（数字与动画同时开始变动），
 * 只用「最小间隔」挡住持续流量下的请求风暴 —— 有流量时服务端大约每秒推
 * 一帧（logs 循环 1s 一拍，每帧日志都叫醒统计循环），不设间隔就是每秒
 * 好几次聚合请求；1000ms 意味着最坏情况数字也只比动画晚一拍请求耗时
 * （几十毫秒），仍在动画播放中，观感上就是「一起变」。
 *
 * busy 在途保护保留原语义：上一次还没回来就跳过本轮，慢查询堆起来只会
 * 让数据更晚更新。
 */
export function createThrottledLiveReloader(run: () => Promise<void>, minInterval = 1000) {
  let timer: number | null = null
  let busy = false
  let lastAt = 0

  /** 请求一次重取。首帧立即，间隔期内的后续帧合并进已排的那一次 */
  function request() {
    if (timer !== null) return
    const wait = Math.max(0, lastAt + minInterval - Date.now())
    timer = window.setTimeout(async () => {
      timer = null
      if (busy) return
      busy = true
      lastAt = Date.now()
      try {
        await run()
      } finally {
        busy = false
      }
    }, wait)
  }

  /** 组件卸载时清掉待执行的定时器：回调里会写已卸载组件的状态 */
  function dispose() {
    if (timer !== null) {
      window.clearTimeout(timer)
      timer = null
    }
  }

  return { request, dispose }
}
