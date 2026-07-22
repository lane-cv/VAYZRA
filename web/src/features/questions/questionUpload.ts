import { APIError, csrfCookie, request } from '../../api/client'
import { createIndexedDBUploadSessionStore, type CompletedUpload, type UploadPart, type UploadSession, type UploadSessionStore, type UploadTransport } from '../teaching/uploadManager'

function createQuestionUploadTransport(prefix:string):UploadTransport{return {
  create: (input) => request<UploadSession>(prefix, { method: 'POST', json: input }),
  status: (id) => request<UploadSession>(`${prefix}/${encodeURIComponent(id)}`),
  async putPart(id: string, number: number, body: Blob, sha256: string, signal?: AbortSignal): Promise<UploadPart> {
    const token = csrfCookie()
    if (!token) throw new APIError(0, 'csrf_missing', '安全校验已失效，请刷新页面后重试', '')
    let response: Response
    try { response = await fetch(`/api/v1${prefix}/${encodeURIComponent(id)}/parts/${number}`, { method: 'PUT', body, signal, credentials: 'include', cache: 'no-store', headers: { Accept: 'application/json', 'Content-Type': 'application/octet-stream', 'X-Part-SHA256': sha256, 'X-CSRF-Token': token } }) }
    catch (error) { if (signal?.aborted) throw error; throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '') }
    const payload = await response.json().catch(() => undefined) as { data?: UploadPart; error?: { code?: string; message?: string; requestId?: string } } | undefined
    if (!response.ok) throw new APIError(response.status, payload?.error?.code ?? 'request_failed', payload?.error?.message ?? '分片上传失败', payload?.error?.requestId ?? '')
    if (!payload?.data) throw new APIError(response.status, 'invalid_response', '服务响应异常，请稍后重试', '')
    return payload.data
  },
  complete: (id) => request<CompletedUpload>(`${prefix}/${encodeURIComponent(id)}/complete`, { method: 'POST', json: {} }),
  cancel: (id) => request<void>(`${prefix}/${encodeURIComponent(id)}/cancel`, { method: 'POST', json: {} }),
}}
export const studentQuestionUploadTransport=createQuestionUploadTransport('/student/question-uploads')
export const adminQuestionUploadTransport=createQuestionUploadTransport('/admin/question-uploads')

export function createStudentQuestionSessionStore(userId: string): UploadSessionStore {
  const store = createIndexedDBUploadSessionStore()
  const key = (fingerprint: string) => `qa:student:${userId}:${fingerprint}`
  return { get: (fingerprint) => store.get(key(fingerprint)), set: (fingerprint, id) => store.set(key(fingerprint), id), delete: (fingerprint) => store.delete(key(fingerprint)) }
}
export function createAdminQuestionSessionStore(userId:string):UploadSessionStore{
  const store=createIndexedDBUploadSessionStore();const key=(fingerprint:string)=>`qa:admin:${userId}:${fingerprint}`
  return {get:(fingerprint)=>store.get(key(fingerprint)),set:(fingerprint,id)=>store.set(key(fingerprint),id),delete:(fingerprint)=>store.delete(key(fingerprint))}
}
