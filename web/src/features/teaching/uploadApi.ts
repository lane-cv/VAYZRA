import { APIError, csrfCookie, request } from '../../api/client'
import type { CompletedUpload, UploadPart, UploadSession, UploadTransport } from './uploadManager'

export const browserUploadTransport: UploadTransport = {
  create(input) { return request<UploadSession>('/admin/uploads', { method: 'POST', json: input }) },
  status(id) { return request<UploadSession>(`/admin/uploads/${encodeURIComponent(id)}`) },
  async putPart(id: string, number: number, body: Blob, sha256: string, signal?: AbortSignal): Promise<UploadPart> {
    const token = csrfCookie()
    if (!token) throw new APIError(0, 'csrf_missing', '安全校验已失效，请刷新页面后重试', '')
    let response: Response
    try {
      response = await fetch(`/api/v1/admin/uploads/${encodeURIComponent(id)}/parts/${number}`, {
        method: 'PUT', body, signal, credentials: 'include', cache: 'no-store',
        headers: { Accept: 'application/json', 'Content-Type': 'application/octet-stream', 'X-Part-SHA256': sha256, 'X-CSRF-Token': token },
      })
    } catch (error) {
      if (signal?.aborted) throw error
      throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '')
    }
    const payload = await parse(response)
    if (!response.ok) {
      const details = payload && typeof payload === 'object' && 'error' in payload && payload.error && typeof payload.error === 'object' ? payload.error as Record<string, unknown> : {}
      throw new APIError(response.status, typeof details.code === 'string' ? details.code : 'request_failed', typeof details.message === 'string' ? details.message : '分片上传失败', typeof details.requestId === 'string' ? details.requestId : '')
    }
    if (!payload || typeof payload !== 'object' || !('data' in payload)) throw new APIError(response.status, 'invalid_response', '服务响应异常，请稍后重试', '')
    return (payload as { data: UploadPart }).data
  },
  complete(id) { return request<CompletedUpload>(`/admin/uploads/${encodeURIComponent(id)}/complete`, { method: 'POST', json: {} }) },
  cancel(id) { return request<void>(`/admin/uploads/${encodeURIComponent(id)}/cancel`, { method: 'POST', json: {} }) },
}

async function parse(response: Response): Promise<unknown> { try { return await response.json() } catch { return undefined } }
