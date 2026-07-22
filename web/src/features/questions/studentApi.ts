import { APIError, request, requestWithMeta } from '../../api/client'
import { uuidV4 } from '../../utils/uuid'
import type { AttachmentInput, MessagePage, QAFileStatus, QuestionDetail, QuestionMessage, QuestionStatus, QuestionThread, ThreadPage } from './types'

type MutationResult = { thread: QuestionThread; message: QuestionMessage }
export type QuestionFilters = { status?: QuestionStatus; limit?: number }

export async function listStudentQuestions(filters: QuestionFilters, cursor?: string, signal?: AbortSignal): Promise<ThreadPage> {
  const query = new URLSearchParams()
  if (filters.status) query.set('status', filters.status)
  if (cursor) query.set('cursor', cursor)
  if (filters.limit) query.set('limit', String(filters.limit))
  const suffix = query.size ? `?${query.toString()}` : ''
  const result = await requestWithMeta<QuestionThread[]>(`/student/questions${suffix}`, { signal })
  const nextCursor = typeof result.meta?.nextCursor === 'string' && result.meta.nextCursor ? result.meta.nextCursor : undefined
  return { items: result.data, nextCursor }
}

export async function createQuestion(input: { title: string; body: string; attachments: AttachmentInput[] }, key: string): Promise<QuestionDetail> {
  const result = await request<MutationResult>('/student/questions', { method: 'POST', headers: { 'Idempotency-Key': key }, json: input })
  return { thread: result.thread, messages: [result.message] }
}

export function getStudentQuestion(id: string, signal?: AbortSignal): Promise<QuestionDetail> {
  return request<QuestionDetail>(`/student/questions/${encodeURIComponent(id)}`, { signal })
}

export async function listStudentMessages(id: string, cursor?: string, limit = 100, signal?: AbortSignal): Promise<MessagePage> {
  const query = new URLSearchParams()
  if (cursor) query.set('cursor', cursor)
  if (limit) query.set('limit', String(limit))
  const result = await requestWithMeta<QuestionMessage[]>(`/student/questions/${encodeURIComponent(id)}/messages?${query.toString()}`, { signal })
  const nextCursor = typeof result.meta?.nextCursor === 'string' && result.meta.nextCursor ? result.meta.nextCursor : undefined
  return { items: result.data, nextCursor }
}

export async function addStudentMessage(id: string, input: { body: string; attachments: AttachmentInput[] }, key: string): Promise<QuestionDetail> {
  const result = await request<MutationResult>(`/student/questions/${encodeURIComponent(id)}/messages`, { method: 'POST', headers: { 'Idempotency-Key': key }, json: input })
  return { thread: result.thread, messages: [result.message] }
}

export async function questionFileStatus(fileVersionId: string, signal?: AbortSignal): Promise<QAFileStatus> {
  let response: Response
  try { response = await fetch(`/api/v1/question-files/${encodeURIComponent(fileVersionId)}/status`, { method: 'GET', signal, credentials: 'include', cache: 'no-store', headers: { Accept: 'application/json' } }) }
  catch (cause) { if (signal?.aborted) throw cause; throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '') }
  const payload = await response.json().catch(() => undefined) as Record<string, unknown> | undefined
  if (!response.ok) {
    const details = payload?.error && typeof payload.error === 'object' ? payload.error as Record<string, unknown> : undefined
    throw new APIError(response.status, typeof details?.code === 'string' ? details.code : 'request_failed', typeof details?.message === 'string' ? details.message : '文件状态检查失败', typeof details?.requestId === 'string' ? details.requestId : '')
  }
  if (!payload || typeof payload.fileVersionId !== 'string' || typeof payload.processingState !== 'string' || typeof payload.size !== 'number' || typeof payload.previewAvailable !== 'boolean') throw new APIError(response.status, 'invalid_response', '服务响应异常，请稍后重试', '')
  return payload as QAFileStatus
}

export function newIdempotencyKey(): string { return uuidV4() }
