import { APIError, csrfCookie, request, requestWithMeta } from '../../api/client'
import { uuidV4 } from '../../utils/uuid'
import type { AIRunStatus, AISubject } from './types'

export type ProtocolMode = 'chat_completions' | 'responses'
export type ProviderView = {
  id: string
  name: string
  baseUrl: string
  protocolMode: ProtocolMode
  active: boolean
  hasKey: boolean
  keyUpdatedAt: string
  version: number
}

export type ProviderWriteInput = {
  name: string
  baseUrl: string
  protocolMode: ProtocolMode
  apiKey?: string
  expectedVersion?: number
}

export type ModelView = {
  id: string
  providerId: string
  upstreamModelId: string
  modality: 'text' | 'vision'
  contextTokens: number
  maxOutputTokens: number
  imageQuotaTokens: number
  inputPriceMicroUsd: number
  outputPriceMicroUsd: number
  connectTimeoutMs: number
  responseHeaderTimeoutMs: number
  idleStreamTimeoutMs: number
  totalTimeoutMs: number
  enabled: boolean
  quotaBlockedAt?: string
  quotaBlockReason?: string
  version: number
}

export type ModelWriteInput = Omit<
  ModelView,
  'id' | 'providerId' | 'quotaBlockedAt' | 'quotaBlockReason' | 'version'
> & {
  clearQuotaBlock: boolean
  expectedVersion: number
}

export type PromptView = {
  id: string
  subject: AISubject
  version: number
  body: string
  active: boolean
}
export type PromptWriteInput = { body: string; expectedVersion: number }

export type LimitMode = 'disabled' | 'inherit' | 'limit'
export type LimitValue = { mode: LimitMode; value?: number }
export type LimitView = {
  dailyRequests: LimitValue
  monthlyRequests: LimitValue
  dailyTokens: LimitValue
  monthlyTokens: LimitValue
  version: number
}
export type LimitWriteInput = Omit<LimitView, 'version'> & { expectedVersion: number }
export type LimitViews = { global: LimitView; students: Record<string, LimitView> }
export type AIConfigStudent = { id: string; username: string; displayName: string }
export type AIConfigStudentPage = { items: AIConfigStudent[]; nextCursor?: string }

export type ConnectivityResult = {
  ok: boolean
  protocol: ProtocolMode
  latencyMs: number
  errorCategory?: string
}

const MAX_CONNECTIVITY_RESPONSE_BYTES = 16 * 1024
const CONNECTIVITY_RESULT_KEYS = new Set(['ok', 'protocol', 'latencyMs', 'errorCategory'])
const CONNECTIVITY_CATEGORIES = new Set([
  '',
  'auth',
  'busy',
  'cancelled',
  'malformed_stream',
  'rate_limited',
  'response_too_large',
  'stream_interrupted',
  'timeout',
  'unavailable',
  'upstream_4xx',
  'upstream_5xx',
])

export type UsageFilters = {
  studentId?: string
  modelId?: string
  status?: AIRunStatus
  from?: string
  to?: string
  cursor?: string
  limit?: number
}
export type UsageSummary = {
  requests: number
  succeeded: number
  failed: number
  inputTokens: number
  outputTokens: number
  costMicroUSD: string
  unknownUsage: number
  averageFirstByteMs: number
  averageTotalMs: number
}
export type UsageRun = {
  id: string
  studentId: string
  studentUsername: string
  studentDisplayName: string
  modelId: string
  modelLabel: string
  status: AIRunStatus
  inputTokens: number
  outputTokens: number
  usageSource: 'upstream' | 'estimated' | 'unknown'
  costMicroUSD: string
  firstByteMs?: number
  totalMs?: number
  errorCategory?: string
  createdAt: string
  startedAt?: string
  completedAt?: string
}
export type UsageRunPage = { items: UsageRun[]; nextCursor?: string }

export function listProviders(signal?: AbortSignal): Promise<ProviderView[]> {
  return request('/admin/ai/providers', { signal })
}

export function createProvider(
  input: ProviderWriteInput,
  idempotencyKey: string = uuidV4(),
  signal?: AbortSignal,
): Promise<ProviderView> {
  return request('/admin/ai/providers', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    json: {
      name: input.name,
      baseUrl: input.baseUrl,
      protocolMode: input.protocolMode,
      apiKey: input.apiKey,
    },
    signal,
  })
}

export function updateProvider(
  providerId: string,
  input: ProviderWriteInput,
  signal?: AbortSignal,
): Promise<ProviderView> {
  return request(`/admin/ai/providers/${encodeURIComponent(providerId)}`, {
    method: 'PUT',
    json: {
      name: input.name,
      baseUrl: input.baseUrl,
      protocolMode: input.protocolMode,
      apiKey: input.apiKey,
      expectedVersion: input.expectedVersion,
    },
    signal,
  })
}

export function activateProvider(
  providerId: string,
  expectedVersion: number,
  signal?: AbortSignal,
): Promise<ProviderView> {
  return request('/admin/ai/active-provider', {
    method: 'PUT',
    json: { providerId, expectedVersion },
    signal,
  })
}

export async function testProvider(providerId: string, signal?: AbortSignal): Promise<ConnectivityResult> {
  signal?.throwIfAborted()
  const token = csrfCookie()
  if (!token) throw new APIError(0, 'csrf_missing', '安全校验已失效，请刷新页面后重试', '')
  let response: Response
  try {
    response = await fetch(`/api/v1/admin/ai/providers/${encodeURIComponent(providerId)}/test`, {
      method: 'POST',
      signal,
      credentials: 'include',
      cache: 'no-store',
      headers: {
        Accept: 'application/json',
        'X-CSRF-Token': token,
      },
    })
  } catch (error) {
    if (signal?.aborted) throw error
    throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '')
  }

  const headerCode = providerTestHeaderErrorCode(response)
  const headerRequestId = providerTestRequestId(response.headers.get('X-Request-ID'))
  if (!response.ok && headerCode) {
    await response.body?.cancel().catch(() => undefined)
    throw new APIError(
      response.status,
      headerCode,
      'AI 服务暂不可用，请稍后重试',
      headerRequestId,
    )
  }

  const payload = await readBoundedConnectivityJSON(response, signal)
  const error = payload && typeof payload === 'object'
    ? (payload as { error?: unknown }).error
    : undefined
  const details = error && typeof error === 'object' ? error as Record<string, unknown> : {}
  if (!response.ok) {
    throw new APIError(
      response.status,
      typeof details.code === 'string' ? details.code : 'request_failed',
      typeof details.message === 'string' ? details.message : '请求未能完成，请稍后重试',
      headerRequestId || providerTestRequestId(details.requestId),
    )
  }

  const result = connectivityResult(payload)
  if (!result) throw new APIError(response.status, 'invalid_response', '服务响应异常，请稍后重试', '')
  return result
}

function providerTestHeaderErrorCode(response: Response): 'PROVIDER_UNAVAILABLE' | undefined {
  const code = response.headers.get('X-Error-Code')?.trim().toUpperCase()
  return code === 'PROVIDER_UNAVAILABLE' ? code : undefined
}

function providerTestRequestId(value: unknown): string {
  if (typeof value !== 'string') return ''
  const normalized = value.trim()
  return /^[A-Za-z0-9_-]{8,64}$/.test(normalized) ? normalized : ''
}

async function readBoundedConnectivityJSON(
  response: Response,
  signal?: AbortSignal,
): Promise<unknown> {
  if (!response.body) return undefined
  const declaredSize = Number(response.headers.get('Content-Length'))
  if (Number.isFinite(declaredSize) && declaredSize > MAX_CONNECTIVITY_RESPONSE_BYTES) {
    await response.body.cancel().catch(() => undefined)
    return undefined
  }

  const reader = response.body.getReader()
  const chunks: Uint8Array[] = []
  let size = 0
  const cancelForAbort = () => { void reader.cancel(signal?.reason) }
  signal?.addEventListener('abort', cancelForAbort, { once: true })
  try {
    while (true) {
      signal?.throwIfAborted()
      const { done, value } = await reader.read()
      signal?.throwIfAborted()
      if (done) break
      size += value.byteLength
      if (size > MAX_CONNECTIVITY_RESPONSE_BYTES) {
        await reader.cancel().catch(() => undefined)
        return undefined
      }
      chunks.push(value)
    }
  } catch {
    if (signal?.aborted) signal.throwIfAborted()
    return undefined
  } finally {
    signal?.removeEventListener('abort', cancelForAbort)
    reader.releaseLock()
  }

  const bytes = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) {
    bytes.set(chunk, offset)
    offset += chunk.byteLength
  }
  try {
    return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)) as unknown
  } catch {
    return undefined
  }
}

function connectivityResult(payload: unknown): ConnectivityResult | undefined {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return undefined
  const record = payload as Record<string, unknown>
  const keys = Object.keys(record)
  if (keys.length !== 4 || keys.some((key) => !CONNECTIVITY_RESULT_KEYS.has(key))) return undefined
  if (
    record.ok !== true
    || record.protocol !== 'chat_completions' && record.protocol !== 'responses'
    || !Number.isSafeInteger(record.latencyMs)
    || (record.latencyMs as number) < 0
    || typeof record.errorCategory !== 'string'
    || !CONNECTIVITY_CATEGORIES.has(record.errorCategory)
    || record.errorCategory !== ''
  ) {
    return undefined
  }
  return {
    ok: true,
    protocol: record.protocol,
    latencyMs: record.latencyMs as number,
    errorCategory: record.errorCategory,
  }
}

export function listModels(providerId: string, signal?: AbortSignal): Promise<ModelView[]> {
  return request(`/admin/ai/providers/${encodeURIComponent(providerId)}/models`, { signal })
}

export function putModel(
  providerId: string,
  modelId: string,
  input: ModelWriteInput,
  signal?: AbortSignal,
): Promise<ModelView> {
  return request(
    `/admin/ai/providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(modelId)}`,
    { method: 'PUT', json: input, signal },
  )
}

export function listPrompts(signal?: AbortSignal): Promise<PromptView[]> {
  return request('/admin/ai/prompts', { signal })
}

export function putPrompt(
  subject: AISubject,
  input: PromptWriteInput,
  signal?: AbortSignal,
): Promise<PromptView> {
  return request(`/admin/ai/prompts/${encodeURIComponent(subject)}`, {
    method: 'PUT',
    json: input,
    signal,
  })
}

export function readLimits(signal?: AbortSignal): Promise<LimitViews> {
  return request('/admin/ai/limits', { signal })
}

export function putGlobalLimits(input: LimitWriteInput, signal?: AbortSignal): Promise<LimitView> {
  return request('/admin/ai/limits/global', { method: 'PUT', json: input, signal })
}

export function putStudentLimits(
  studentId: string,
  input: LimitWriteInput,
  signal?: AbortSignal,
): Promise<LimitView> {
  return request(`/admin/ai/limits/students/${encodeURIComponent(studentId)}`, {
    method: 'PUT',
    json: input,
    signal,
  })
}

export async function listAIConfigStudents(
  cursor?: string,
  signal?: AbortSignal,
): Promise<AIConfigStudentPage> {
  const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''
  const result = await requestWithMeta<AIConfigStudent[]>(`/admin/students${query}`, { signal })
  return {
    items: result.data,
    nextCursor: typeof result.meta?.nextCursor === 'string' && result.meta.nextCursor
      ? result.meta.nextCursor
      : undefined,
  }
}

function usageQuery(filters: UsageFilters): string {
  const query = new URLSearchParams()
  if (filters.studentId) query.set('studentId', filters.studentId)
  if (filters.modelId) query.set('modelId', filters.modelId)
  if (filters.status) query.set('status', filters.status)
  if (filters.from) query.set('from', filters.from)
  if (filters.to) query.set('to', filters.to)
  if (filters.cursor) query.set('cursor', filters.cursor)
  if (filters.limit !== undefined) query.set('limit', String(filters.limit))
  return query.size ? `?${query.toString()}` : ''
}

export function readUsageSummary(
  filters: UsageFilters = {},
  signal?: AbortSignal,
): Promise<UsageSummary> {
  return request(`/admin/ai/usage/summary${usageQuery(filters)}`, { signal })
}

export async function listUsageRuns(
  filters: UsageFilters = {},
  signal?: AbortSignal,
): Promise<UsageRunPage> {
  const result = await requestWithMeta<UsageRun[]>(
    `/admin/ai/usage/runs${usageQuery(filters)}`,
    { signal },
  )
  const nextCursor = typeof result.meta?.nextCursor === 'string' && result.meta.nextCursor
    ? result.meta.nextCursor
    : undefined
  return { items: result.data, nextCursor }
}
