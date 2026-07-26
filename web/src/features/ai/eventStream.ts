import { APIError } from '../../api/client'
import type { AIRunStatus, StreamCallbacks, StreamEvent } from './types'

const RUN_STATUSES = new Set<AIRunStatus>([
  'queued',
  'streaming',
  'succeeded',
  'failed',
  'cancelled',
])
const EVENT_KEYS = new Set(['sequence', 'kind', 'delta', 'status', 'errorCode'])

export async function subscribeRun(
  runId: string,
  afterSequence: number,
  callbacks: StreamCallbacks,
  signal: AbortSignal,
): Promise<void> {
  signal.throwIfAborted()
  if (!Number.isSafeInteger(afterSequence) || afterSequence < 0) {
    throw invalidStream()
  }
  let response: Response
  try {
    response = await fetch(
      `/api/v1/student/ai/runs/${encodeURIComponent(runId)}/events?afterSequence=${afterSequence}`,
      {
        method: 'GET',
        headers: { Accept: 'text/event-stream' },
        credentials: 'include',
        cache: 'no-store',
        signal,
      },
    )
  } catch (error) {
    if (signal.aborted) throw error
    throw new APIError(0, 'network_error', '网络连接异常，请稍后重试', '')
  }
  if (!response.ok) throw await streamHTTPError(response)
  if (response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase() !== 'text/event-stream') {
    throw invalidStream()
  }
  if (!response.body) throw invalidStream()

  const requestId = response.headers.get('X-Request-ID')
  if (requestId) callbacks.onRequestId?.(requestId)

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let committed = afterSequence
  let terminal = false
  try {
    while (!terminal) {
      const result = await reader.read()
      if (result.done) {
        buffer += decoder.decode()
        break
      }
      buffer += decoder.decode(result.value, { stream: true })
      const parsed = drainFrames(buffer)
      buffer = parsed.remainder
      for (const raw of parsed.frames) {
        const event = parseFrame(raw)
        if (!event) continue
        if (event.sequence <= committed) continue
        if (event.sequence !== committed + 1) throw invalidStream()
        callbacks.onEvent(event)
        committed = event.sequence
        if (isTerminal(event)) {
          terminal = true
          break
        }
      }
    }
  } catch (error) {
    if (signal.aborted) throw signal.reason ?? error
    throw error
  } finally {
    if (terminal) await reader.cancel().catch(() => undefined)
    reader.releaseLock()
  }
}

function drainFrames(value: string): { frames: string[]; remainder: string } {
  const frames: string[] = []
  const boundary = /\r\n\r\n|\n\n|\r\r/g
  let start = 0
  for (let match = boundary.exec(value); match; match = boundary.exec(value)) {
    frames.push(value.slice(start, match.index).replace(/\r\n?/g, '\n'))
    start = match.index + match[0].length
  }
  return { frames, remainder: value.slice(start) }
}

function parseFrame(frame: string): StreamEvent | undefined {
  const data: string[] = []
  for (const line of frame.split('\n')) {
    if (!line || line.startsWith(':')) continue
    if (line === 'data') data.push('')
    else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
  }
  if (!data.length) return undefined
  let value: unknown
  try {
    value = JSON.parse(data.join('\n'))
  } catch {
    throw invalidStream()
  }
  return validEvent(value)
}

function validEvent(value: unknown): StreamEvent {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw invalidStream()
  const record = value as Record<string, unknown>
  if (Object.keys(record).some((key) => !EVENT_KEYS.has(key))) throw invalidStream()
  if (!Number.isSafeInteger(record.sequence) || (record.sequence as number) <= 0) throw invalidStream()
  const sequence = record.sequence as number
  if (record.kind === 'delta') {
    if (typeof record.delta !== 'string' || record.status !== undefined || record.errorCode !== undefined) {
      throw invalidStream()
    }
    return { sequence, kind: 'delta', delta: record.delta }
  }
  if (record.kind === 'status') {
    if (
      typeof record.status !== 'string'
      || !RUN_STATUSES.has(record.status as AIRunStatus)
      || record.delta !== undefined
      || record.errorCode !== undefined && typeof record.errorCode !== 'string'
    ) {
      throw invalidStream()
    }
    return {
      sequence,
      kind: 'status',
      status: record.status as AIRunStatus,
      ...(typeof record.errorCode === 'string' ? { errorCode: record.errorCode } : {}),
    }
  }
  if (record.kind === 'error') {
    if (
      typeof record.errorCode !== 'string'
      || record.errorCode.length === 0
      || record.delta !== undefined
      || record.status !== undefined && record.status !== 'failed'
    ) {
      throw invalidStream()
    }
    return {
      sequence,
      kind: 'error',
      errorCode: record.errorCode,
      ...(record.status === 'failed' ? { status: 'failed' as const } : {}),
    }
  }
  throw invalidStream()
}

function isTerminal(event: StreamEvent): boolean {
  return event.kind === 'error'
    || event.status === 'succeeded'
    || event.status === 'failed'
    || event.status === 'cancelled'
}

async function streamHTTPError(response: Response): Promise<APIError> {
  const payload = await response.json().catch(() => undefined) as unknown
  const error = payload && typeof payload === 'object'
    ? (payload as { error?: unknown }).error
    : undefined
  const details = error && typeof error === 'object' ? error as Record<string, unknown> : {}
  return new APIError(
    response.status,
    typeof details.code === 'string' ? details.code : 'request_failed',
    typeof details.message === 'string' ? details.message : '请求未能完成，请稍后重试',
    typeof details.requestId === 'string' ? details.requestId : '',
  )
}

function invalidStream(): APIError {
  return new APIError(0, 'invalid_stream', 'AI 流式响应无效，请重试', '')
}
