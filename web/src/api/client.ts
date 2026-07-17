export type Role = 'admin' | 'student'
export type UserView = { id: string; username: string; displayName: string; role: Role; mustChangePassword: boolean }
export type APIResult<T> = { data: T; meta?: Record<string, unknown> }
type RequestOptions = Omit<RequestInit, 'body' | 'headers'> & { headers?: HeadersInit; json?: unknown }
let unauthorizedHandler: (() => void) | undefined
export class APIError extends Error { constructor(public readonly status: number, public readonly code: string, message: string, public readonly requestId: string) { super(message); this.name = 'APIError' } }
export function registerUnauthorizedHandler(handler: (() => void) | undefined): void { unauthorizedHandler = handler }
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> { return (await requestWithMeta<T>(path, options)).data }
export async function requestWithMeta<T>(path: string, options: RequestOptions = {}): Promise<APIResult<T>> {
  const { json, headers: inputHeaders, ...init } = options
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(inputHeaders)
  headers.set('Accept', 'application/json')
  if (json !== undefined) headers.set('Content-Type', 'application/json')
  if (!isSafeMethod(method) && path !== '/auth/login') {
    const csrfToken = csrfCookie()
    if (!csrfToken) throw new APIError(0, 'csrf_missing', '安全校验已失效，请刷新页面后重试', '')
    headers.set('X-CSRF-Token', csrfToken)
  }
  let response: Response
  try { response = await fetch(`/api/v1${path}`, { ...init, method, body: json === undefined ? undefined : JSON.stringify(json), headers, credentials: 'include', cache: 'no-store' }) } catch { throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '') }
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
function csrfCookie(): string | undefined { const values=document.cookie.split(';').map((part)=>part.trim()).filter((part)=>part.startsWith('hl_csrf=')).map((part)=>part.slice(8)); if(values.length!==1||!values[0]) return undefined; try { const value=decodeURIComponent(values[0]); return value || undefined } catch { return undefined } }
async function parseJSON(response: Response): Promise<unknown> { try { return await response.json() } catch { return undefined } }