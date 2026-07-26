import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { subscribeRun } from './eventStream'
import type { StreamEvent } from './types'

function streamedResponse(chunks: Uint8Array[], headers?: HeadersInit): Response {
  return new Response(new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(chunk)
      controller.close()
    },
  }), { headers: { 'Content-Type': 'text/event-stream', ...headers } })
}

function encodedChunks(...values: string[]): Uint8Array[] {
  const encoder = new TextEncoder()
  return values.map((value) => encoder.encode(value))
}

describe('AI run event stream', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('parses split UTF-8, CRLF, comments, and multiline JSON data', async () => {
    const raw = ': heartbeat\r\nid: 1\r\nevent: message\r\ndata: {"sequence":1,\r\ndata: "kind":"delta","delta":"你"}\r\n\r\n'
    const bytes = new TextEncoder().encode(raw)
    const splitInsideUTF8 = bytes.findIndex((value) => value >= 0xe0) + 1
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(streamedResponse([
      bytes.slice(0, splitInsideUTF8),
      bytes.slice(splitInsideUTF8),
    ], { 'X-Request-ID': 'req-stream' })))
    const events: StreamEvent[] = []
    const requestIDs: string[] = []

    await subscribeRun('run /?', 0, {
      onEvent: (event) => events.push(event),
      onRequestId: (requestId) => requestIDs.push(requestId),
    }, new AbortController().signal)

    expect(events).toEqual([{ sequence: 1, kind: 'delta', delta: '你' }])
    expect(requestIDs).toEqual(['req-stream'])
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      '/api/v1/student/ai/runs/run%20%2F%3F/events?afterSequence=0',
      expect.objectContaining({ credentials: 'include', cache: 'no-store' }),
    )
    expect(new Headers(vi.mocked(fetch).mock.calls[0][1]?.headers).get('Accept')).toBe('text/event-stream')
  })

  it('does not invent a blank frame when CRLF is split between chunks', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(streamedResponse(encodedChunks(
      'data: {"sequence":1,\r',
      '\ndata: "kind":"delta","delta":"safe"}\r\n\r\n',
    ))))
    const events: StreamEvent[] = []

    await subscribeRun(
      'run-id',
      0,
      { onEvent: (event) => events.push(event) },
      new AbortController().signal,
    )

    expect(events).toEqual([{ sequence: 1, kind: 'delta', delta: 'safe' }])
  })

  it('suppresses duplicates, commits only contiguous sequences, and stops at terminal status', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(streamedResponse(encodedChunks(
      'data: {"sequence":2,"kind":"delta","delta":"old"}\n\n',
      'data: {"sequence":3,"kind":"delta","delta":"new"}\n\n',
      'data: {"sequence":4,"kind":"status","status":"succeeded"}\n\n',
      'data: {"sequence":5,"kind":"delta","delta":"never"}\n\n',
    ))))
    const events: StreamEvent[] = []

    await subscribeRun('run-id', 2, { onEvent: (event) => events.push(event) }, new AbortController().signal)

    expect(events).toEqual([
      { sequence: 3, kind: 'delta', delta: 'new' },
      { sequence: 4, kind: 'status', status: 'succeeded' },
    ])
  })

  it.each([
    ['malformed JSON', 'data: not-json\n\n'],
    ['missing sequence', 'data: {"kind":"delta","delta":"x"}\n\n'],
    ['sequence gap', 'data: {"sequence":2,"kind":"delta","delta":"x"}\n\n'],
    ['invalid event shape', 'data: {"sequence":1,"kind":"delta","status":"streaming"}\n\n'],
  ])('rejects %s without committing an event', async (_name, payload) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(streamedResponse(encodedChunks(payload))))
    const onEvent = vi.fn()

    await expect(subscribeRun('run-id', 0, { onEvent }, new AbortController().signal))
      .rejects.toMatchObject({ code: 'invalid_stream' } satisfies Partial<APIError>)
    expect(onEvent).not.toHaveBeenCalled()
  })

  it('rejects a successful response that is not an event stream', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
      'data: {"sequence":1,"kind":"delta","delta":"x"}\n\n',
      { headers: { 'Content-Type': 'application/json' } },
    )))

    await expect(subscribeRun(
      'run-id',
      0,
      { onEvent: vi.fn() },
      new AbortController().signal,
    )).rejects.toMatchObject({ code: 'invalid_stream' } satisfies Partial<APIError>)
  })

  it('allows caller-owned reconnect with the last committed sequence', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(streamedResponse(encodedChunks(
        'data: {"sequence":1,"kind":"delta","delta":"a"}\n\n',
      )))
      .mockResolvedValueOnce(streamedResponse(encodedChunks(
        'data: {"sequence":2,"kind":"status","status":"succeeded"}\n\n',
      ))))
    let afterSequence = 0
    const onEvent = (event: StreamEvent) => { afterSequence = event.sequence }

    await subscribeRun('run-id', afterSequence, { onEvent }, new AbortController().signal)
    await subscribeRun('run-id', afterSequence, { onEvent }, new AbortController().signal)

    expect(vi.mocked(fetch).mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/student/ai/runs/run-id/events?afterSequence=0',
      '/api/v1/student/ai/runs/run-id/events?afterSequence=1',
    ])
  })

  it('propagates abort without converting it to an API error', async () => {
    const controller = new AbortController()
    controller.abort()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    await expect(subscribeRun('run-id', 0, { onEvent: vi.fn() }, controller.signal))
      .rejects.toMatchObject({ name: 'AbortError' })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
