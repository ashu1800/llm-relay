// 管理后台 API 客户端。本地自用，无鉴权头。
const BASE = '/api/admin'

export interface ApiError {
  message: string
  status: number
}

// 网络层失败时的提示文案。
//
// fetch 只在「请求根本没到达服务端」时抛异常（服务没起来、连接被中断、
// 被浏览器扩展拦截），此时它给的是英文的 "Failed to fetch" ——
// 直接显示给用户等于没说，尤其在这种整页中文的界面里。
// 所以换成能指出下一步的中文说明。
const NETWORK_ERROR = '无法连接到后端服务，请确认服务正在运行（默认 http://127.0.0.1:8888）'

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(BASE + path, {
      headers: { 'Content-Type': 'application/json' },
      ...init
    })
  } catch {
    const err: ApiError = { message: NETWORK_ERROR, status: 0 }
    throw err
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
    const err: ApiError = { message: msg, status: res.status }
    throw err
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
