// 实时推送的客户端：一条 WebSocket，多个页面订阅。
//
// 为什么全局共享一条连接：看板与日志页都要实时数据，
// 各开一条的话切页时连接反复建立，服务端也要维护多份订阅与游标。
//
// 断线重连用指数退避（1s 起，上限 15s）：后端重启时，
// 一堆标签页同时以固定间隔重连会把刚起来的服务再打一遍。
import { onUnmounted, ref } from 'vue'

type Handler = (data: any) => void

const handlers = new Map<string, Set<Handler>>()
let socket: WebSocket | null = null
let retry = 0
let reconnectTimer: number | null = null

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

function connect() {
  if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
    return
  }
  try {
    socket = new WebSocket(socketURL())
  } catch {
    scheduleReconnect()
    return
  }
  socket.onopen = () => {
    liveConnected.value = true
    retry = 0
  }
  socket.onmessage = (ev) => {
    try {
      dispatch(JSON.parse(ev.data))
    } catch {
      // 非 JSON 帧直接忽略：服务端只发 JSON，收到别的说明链路有问题，
      // 但也没必要因此把连接断开
    }
  }
  socket.onclose = () => {
    liveConnected.value = false
    socket = null
    // 还有人订阅才重连；没人订阅时让它断着，省得空转
    if (handlers.size > 0) scheduleReconnect()
  }
  socket.onerror = () => {
    // onerror 之后一定会跟一个 onclose，重连逻辑写在那边，避免重复调度
  }
}

function scheduleReconnect() {
  if (reconnectTimer !== null) return
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
    if (handlers.size === 0 && socket) {
      socket.close()
      socket = null
    }
  })
}
