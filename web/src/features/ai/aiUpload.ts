import { APIError, csrfCookie, request } from '../../api/client'
import {
  createIndexedDBUploadSessionStore,
  createUploadManager,
  type CompletedUpload,
  type UploadManagerState,
  type UploadPart,
  type UploadSession,
  type UploadSessionStore,
  type UploadTransport,
} from '../teaching/uploadManager'
import type { AIFileStatus } from './types'

const AI_UPLOAD_PREFIX = '/student/ai-uploads'

export const aiUploadTransport: UploadTransport = {
  create: (input) => request<UploadSession>(AI_UPLOAD_PREFIX, { method: 'POST', json: input }),
  status: (id) => request<UploadSession>(`${AI_UPLOAD_PREFIX}/${encodeURIComponent(id)}`),
  async putPart(
    id: string,
    number: number,
    body: Blob,
    sha256: string,
    signal?: AbortSignal,
  ): Promise<UploadPart> {
    const token = csrfCookie()
    if (!token) throw new APIError(0, 'csrf_missing', '安全校验已失效，请刷新页面后重试', '')
    let response: Response
    try {
      response = await fetch(
        `/api/v1${AI_UPLOAD_PREFIX}/${encodeURIComponent(id)}/parts/${number}`,
        {
          method: 'PUT',
          body,
          signal,
          credentials: 'include',
          cache: 'no-store',
          headers: {
            Accept: 'application/json',
            'Content-Type': 'application/octet-stream',
            'X-Part-SHA256': sha256,
            'X-CSRF-Token': token,
          },
        },
      )
    } catch (error) {
      if (signal?.aborted) throw error
      throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '')
    }
    const payload = await response.json().catch(() => undefined) as {
      data?: UploadPart
      error?: { code?: string; message?: string; requestId?: string }
    } | undefined
    if (!response.ok) {
      throw new APIError(
        response.status,
        payload?.error?.code ?? 'request_failed',
        payload?.error?.message ?? '分片上传失败',
        payload?.error?.requestId ?? '',
      )
    }
    if (!payload?.data) {
      throw new APIError(response.status, 'invalid_response', '服务响应异常，请稍后重试', '')
    }
    return payload.data
  },
  complete: (id) => request<CompletedUpload>(
    `${AI_UPLOAD_PREFIX}/${encodeURIComponent(id)}/complete`,
    { method: 'POST', json: {} },
  ),
  cancel: (id) => request<void>(
    `${AI_UPLOAD_PREFIX}/${encodeURIComponent(id)}/cancel`,
    { method: 'POST', json: {} },
  ),
}

export function createAIUploadSessionStore(userId: string): UploadSessionStore {
  const store = createIndexedDBUploadSessionStore()
  const key = (fingerprint: string) => `qa:ai:${userId}:${fingerprint}`
  return {
    get: (fingerprint) => store.get(key(fingerprint)),
    set: (fingerprint, sessionId) => store.set(key(fingerprint), sessionId),
    delete: (fingerprint) => store.delete(key(fingerprint)),
  }
}

export function createAIUploadManager(
  userId: string,
  onState?: (state: UploadManagerState) => void,
) {
  return createUploadManager({
    transport: aiUploadTransport,
    sessions: createAIUploadSessionStore(userId),
    onState,
  })
}

export async function aiFileStatus(
  fileVersionId: string,
  signal?: AbortSignal,
): Promise<AIFileStatus> {
  let response: Response
  try {
    response = await fetch(
      `/api/v1/ai-question-files/${encodeURIComponent(fileVersionId)}/status`,
      {
        method: 'GET',
        signal,
        credentials: 'include',
        cache: 'no-store',
        headers: { Accept: 'application/json' },
      },
    )
  } catch (error) {
    if (signal?.aborted) throw error
    throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '')
  }
  const payload = await response.json().catch(() => undefined) as Record<string, unknown> | undefined
  if (!response.ok) {
    const details = payload?.error && typeof payload.error === 'object'
      ? payload.error as Record<string, unknown>
      : undefined
    throw new APIError(
      response.status,
      typeof details?.code === 'string' ? details.code : 'request_failed',
      typeof details?.message === 'string' ? details.message : '文件状态检查失败',
      typeof details?.requestId === 'string' ? details.requestId : '',
    )
  }
  if (
    !payload
    || typeof payload.fileVersionId !== 'string'
    || typeof payload.processingState !== 'string'
    || typeof payload.size !== 'number'
    || typeof payload.previewAvailable !== 'boolean'
  ) {
    throw new APIError(response.status, 'invalid_response', '服务响应异常，请稍后重试', '')
  }
  return payload as AIFileStatus
}

export function aiFilePreviewURL(fileVersionId: string): string {
  return `/api/v1/ai-question-files/${encodeURIComponent(fileVersionId)}/preview`
}
