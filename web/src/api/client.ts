export type Role = 'admin' | 'student'

export type UserView = {
  id: string
  username: string
  displayName: string
  role: Role
  mustChangePassword: boolean
}

export type APIResult<T> = { data: T; meta?: Record<string, unknown> }

type RequestOptions = Omit<RequestInit, 'body' | 'headers'> & {
  headers?: HeadersInit
  json?: unknown
}

let unauthorizedHandler: (() => void) | undefined

export class APIError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string, public readonly requestId: string) {
    super(message)
    this.name = 'APIError'
  }
}

export function registerUnauthorizedHandler(handler: (() => void) | undefined): void { unauthorizedHandler = handler }

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  return (await requestWithMeta<T>(path, options)).data
}

export async function requestWithMeta<T>(path: string, options: RequestOptions = {}): Promise<APIResult<T>> {
  const { json, headers: inputHeaders, ...init } = options
  const method = (init.method ?? 'GET').toUpperCase()
  const headers: Record<string, string> = { Accept: 'application/json' }
  for (const [name, value] of new Headers(inputHeaders).entries()) headers[name] = value
  if (json !== undefined) headers['Content-Type'] = 'application/json'
  if (!isSafeMethod(method)) {
    const csrfToken = csrfCookie()
    if (csrfToken) headers['X-CSRF-Token'] = csrfToken
  }

  let response: Response
  try {
    response = await fetch(`/api/v1${path}`, { ...init, method, body: json === undefined ? undefined : JSON.stringify(json), headers, credentials: 'include' })
  } catch {
    throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '')
  }
  if (response.status === 204) return { data: undefined as T }

  const payload = await parseJSON(response)
  if (!response.ok) {
    const error = payload && typeof payload === 'object' ? (payload as { error?: unknown }).error : undefined
    const details = error && typeof error === 'object' ? error as Record<string, unknown> : {}
    const apiError = new APIError(response.status, typeof details.code === 'string' ? details.code : 'request_failed', typeof details.message === 'string' ? details.message : '请求未能完成，请稍后重试', typeof details.requestId === 'string' ? details.requestId : '')
    if (response.status === 401) unauthorizedHandler?.()
    throw apiError
  }
  if (!payload || typeof payload !== 'object' || !('data' in payload)) throw new APIError(response.status, 'invalid_response', '服务响应异常，请稍后重试', '')
  const envelope = payload as { data: T; meta?: unknown }
  return envelope.meta === undefined ? { data: envelope.data } : { data: envelope.data, meta: envelope.meta as Record<string, unknown> }
}

function isSafeMethod(method: string): boolean { return method === 'GET' || method === 'HEAD' || method === 'OPTIONS' }
function csrfCookie(): string | undefined { return document.cookie.split(';').map((entry) => entry.trim()).find((entry) => entry.startsWith('hl_csrf='))?.slice('hl_csrf='.length) }
async function parseJSON(response: Response): Promise<unknown> { try { return await response.json() } catch { return undefined } }