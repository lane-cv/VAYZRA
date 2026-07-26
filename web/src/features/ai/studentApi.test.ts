import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import {
  addAIMessage,
  cancelAIRun,
  createAIThread,
  getAIThread,
  listAIThreads,
  newAIIdempotencyKey,
  retryAIRun,
} from './studentApi'

describe('student AI api', () => {
  beforeEach(() => {
    document.cookie = 'hl_csrf=csrf; path=/'
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => vi.unstubAllGlobals())

  it('encodes thread and message cursors and route identifiers exactly', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: [], meta: { nextCursor: 'next' } })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: { thread: { id: 't1' }, messages: [], nextMessageCursor: 'message-next' },
      })))

    await listAIThreads({ cursor: 'a+b/=', limit: 25 })
    await getAIThread('id /?', { cursor: 'm+/=', limit: 50 })

    expect(vi.mocked(fetch).mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/student/ai/threads?cursor=a%2Bb%2F%3D&limit=25',
      '/api/v1/student/ai/threads/id%20%2F%3F?cursor=m%2B%2F%3D&limit=50',
    ])
  })

  it('sends strict create and follow-up bodies with caller UUID idempotency keys', async () => {
    vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({
      data: {
        thread: { id: 't1', title: 'T', subject: 'math' },
        message: {
          id: 'm1',
          role: 'student',
          body: 'B',
          attachments: [{
            fileVersionId: 'v1',
            sortPosition: 0,
            displayName: 'work.pdf',
            detectedMime: 'application/pdf',
            size: 12,
          }],
        },
        run: { id: 'r1', status: 'queued', attemptNo: 1, lastSequence: 0 },
        eventsUrl: '/api/v1/student/ai/runs/r1/events',
      },
    }), { status: 201 }))
    const key = crypto.randomUUID()

    const created = await createAIThread({
      title: 'T',
      subject: 'math',
      body: 'B',
      attachments: [{ fileVersionId: 'v1', sortPosition: 0 }],
    }, key)
    await addAIMessage('thread /?', {
      body: 'follow up',
      attachments: [{ fileVersionId: 'v2', sortPosition: 1 }],
    }, key)

    const calls = vi.mocked(fetch).mock.calls
    expect(calls.map(([url]) => url)).toEqual([
      '/api/v1/student/ai/threads',
      '/api/v1/student/ai/threads/thread%20%2F%3F/messages',
    ])
    expect(calls.map(([, init]) => JSON.parse(String(init?.body)))).toEqual([
      { title: 'T', subject: 'math', body: 'B', attachments: [{ fileVersionId: 'v1', sortPosition: 0 }] },
      { body: 'follow up', attachments: [{ fileVersionId: 'v2', sortPosition: 1 }] },
    ])
    expect(calls.map(([, init]) => new Headers(init?.headers).get('Idempotency-Key'))).toEqual([key, key])
    expect(created.message?.attachments).toEqual([{
      fileVersionId: 'v1',
      sortPosition: 0,
      displayName: 'work.pdf',
      previewAvailable: false,
    }])
    expect(newAIIdempotencyKey()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  })

  it('uses strict empty bodies for cancel and retry and only retry carries idempotency', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { id: 'r1', status: 'cancelled', attemptNo: 1, lastSequence: 2 } })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: {
          run: { id: 'r2', status: 'queued', attemptNo: 2, lastSequence: 0 },
          eventsUrl: '/api/v1/student/ai/runs/r2/events',
        },
      }), { status: 201 }))
    const key = crypto.randomUUID()

    await cancelAIRun('run /?')
    await retryAIRun('run /?', key)

    const calls = vi.mocked(fetch).mock.calls
    expect(calls.map(([url]) => url)).toEqual([
      '/api/v1/student/ai/runs/run%20%2F%3F/cancel',
      '/api/v1/student/ai/runs/run%20%2F%3F/retries',
    ])
    expect(calls.map(([, init]) => JSON.parse(String(init?.body)))).toEqual([{}, {}])
    expect(new Headers(calls[0][1]?.headers).get('Idempotency-Key')).toBeNull()
    expect(new Headers(calls[1][1]?.headers).get('Idempotency-Key')).toBe(key)
  })

  it('preserves APIError codes and request IDs', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({
      error: { code: 'AI_BUSY', message: 'busy', requestId: 'req-ai' },
    }), { status: 409 }))

    await expect(listAIThreads({})).rejects.toMatchObject({
      status: 409,
      code: 'AI_BUSY',
      requestId: 'req-ai',
    } satisfies Partial<APIError>)
  })
})
