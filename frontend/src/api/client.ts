// 管理后台 API 客户端。本地自用，无鉴权头。
const BASE = '/api/admin'

// 请求超时上限。管理后台的正常请求在几百毫秒量级，15 秒已经非常宽裕；
// 没有它的话，后端卡住（或代理层挂起）时页面会永远停在加载态 ——
// 既不成功也不失败，用户只能干等或刷新。
const REQUEST_TIMEOUT_MS = 15_000

// ApiError 用 class 而不是普通对象：instanceof Error 为真、带堆栈，
// 控制台与上报工具都能认。字段形状（message / status）与原来的接口一致，
// 调用方 catch (e: any) 的用法不受影响。
export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

// 网络层失败时的提示文案。
//
// fetch 只在「请求根本没到达服务端」时抛异常（服务没起来、连接被中断、
// 被浏览器扩展拦截），此时它给的是英文的 "Failed to fetch" ——
// 直接显示给用户等于没说，尤其在这种整页中文的界面里。
// 所以换成能指出下一步的中文说明。
const NETWORK_ERROR = '无法连接到后端服务，请确认服务正在运行（默认 http://127.0.0.1:8888）'
const TIMEOUT_ERROR = '请求超时（15 秒无响应），后端可能正忙，请稍后重试'

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(BASE + path, {
      ...init,
      // headers 放在 ...init 之后做合并（而不是之前的位置）：
      // 原来写在前面，调用方一旦传 headers 会把 Content-Type 整体覆盖掉。
      // 展开合并让默认头始终在场，调用方的头优先。
      headers: { 'Content-Type': 'application/json', ...init?.headers },
      // 调用方显式传了 signal 就尊重它；否则给一个兜底超时。
      // AbortSignal.timeout 到期抛的是 name 为 TimeoutError 的 DOMException，
      // 与手动 abort 的 AbortError 是两个名字，可以区分提示。
      signal: init?.signal ?? AbortSignal.timeout(REQUEST_TIMEOUT_MS)
    })
  } catch (e: any) {
    const timedOut = e?.name === 'TimeoutError'
    throw new ApiError(timedOut ? TIMEOUT_ERROR : NETWORK_ERROR, 0)
  }
  const text = await res.text()
  let data: any = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = { raw: text }
    }
  }
  if (!res.ok) {
    const msg = data?.error?.message || data?.message || '请求失败 ' + res.status
    throw new ApiError(msg, res.status)
  }
  return data as T
}

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body: unknown) =>
    request<T>(path, { method: 'POST', body: JSON.stringify(body) }),
  put: <T>(path: string, body: unknown) =>
    request<T>(path, { method: 'PUT', body: JSON.stringify(body) }),
  del: <T>(path: string) => request<T>(path, { method: 'DELETE' })
}
