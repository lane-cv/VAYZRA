import { request, requestWithMeta } from '../../api/client'
import { uuidV4 } from '../../utils/uuid'
import type {
  AddAIMessageInput,
  AIMessage,
  AIRun,
  AIThread,
  AIThreadDetail,
  AIThreadMutation,
  AIThreadPage,
  CreateAIThreadInput,
} from './types'

export type AIThreadListOptions = { cursor?: string; limit?: number }
export type AIThreadDetailOptions = { cursor?: string; limit?: number }
type StudentAttachmentDTO = {
  fileVersionId: string
  sortPosition: number
  displayName: string
  detectedMime?: string
  size: number
}
type StudentMessageDTO = Omit<AIMessage, 'attachments'> & { attachments: StudentAttachmentDTO[] }
type StudentThreadDetailDTO = Omit<AIThreadDetail, 'messages'> & { messages: StudentMessageDTO[] }
type StudentThreadMutationDTO = Omit<AIThreadMutation, 'message'> & { message?: StudentMessageDTO }

function pageQuery(options: { cursor?: string; limit?: number }): string {
  const query = new URLSearchParams()
  if (options.cursor) query.set('cursor', options.cursor)
  if (options.limit !== undefined) query.set('limit', String(options.limit))
  return query.size ? `?${query.toString()}` : ''
}

export async function listAIThreads(
  options: AIThreadListOptions = {},
  signal?: AbortSignal,
): Promise<AIThreadPage> {
  const result = await requestWithMeta<AIThread[]>(`/student/ai/threads${pageQuery(options)}`, { signal })
  const nextCursor = typeof result.meta?.nextCursor === 'string' && result.meta.nextCursor
    ? result.meta.nextCursor
    : undefined
  return { items: result.data, nextCursor }
}

export async function getAIThread(
  threadId: string,
  options: AIThreadDetailOptions = {},
  signal?: AbortSignal,
): Promise<AIThreadDetail> {
  const result = await request<StudentThreadDetailDTO>(
    `/student/ai/threads/${encodeURIComponent(threadId)}${pageQuery(options)}`,
    { signal },
  )
  return { ...result, messages: result.messages.map(messageView) }
}

export async function createAIThread(
  input: CreateAIThreadInput,
  idempotencyKey: string,
  signal?: AbortSignal,
): Promise<AIThreadMutation> {
  const result = await request<StudentThreadMutationDTO>('/student/ai/threads', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    json: {
      title: input.title,
      subject: input.subject,
      body: input.body,
      attachments: input.attachments,
    },
    signal,
  })
  return mutationView(result)
}

export async function addAIMessage(
  threadId: string,
  input: AddAIMessageInput,
  idempotencyKey: string,
  signal?: AbortSignal,
): Promise<AIThreadMutation> {
  const result = await request<StudentThreadMutationDTO>(
    `/student/ai/threads/${encodeURIComponent(threadId)}/messages`,
    {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      json: { body: input.body, attachments: input.attachments },
      signal,
    },
  )
  return mutationView(result)
}

export function cancelAIRun(runId: string, signal?: AbortSignal): Promise<AIRun> {
  return request<AIRun>(`/student/ai/runs/${encodeURIComponent(runId)}/cancel`, {
    method: 'POST',
    json: {},
    signal,
  })
}

export function retryAIRun(
  runId: string,
  idempotencyKey: string,
  signal?: AbortSignal,
): Promise<AIThreadMutation> {
  return request<AIThreadMutation>(`/student/ai/runs/${encodeURIComponent(runId)}/retries`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    json: {},
    signal,
  })
}

export function newAIIdempotencyKey(): string {
  return uuidV4()
}

function messageView(message: StudentMessageDTO): AIMessage {
  return {
    ...message,
    attachments: message.attachments.map((attachment) => ({
      fileVersionId: attachment.fileVersionId,
      sortPosition: attachment.sortPosition,
      displayName: attachment.displayName,
      // Availability comes from the purpose-specific status endpoint.
      previewAvailable: false,
    })),
  }
}

function mutationView(result: StudentThreadMutationDTO): AIThreadMutation {
  const { message, ...mutation } = result
  return message ? { ...mutation, message: messageView(message) } : mutation
}
