import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { addStudentMessage, createQuestion, getStudentQuestion, listStudentMessages, listStudentQuestions, newIdempotencyKey, questionFileStatus } from './studentApi'

describe('student question api', () => {
  beforeEach(() => { document.cookie = 'hl_csrf=csrf; path=/'; vi.stubGlobal('fetch', vi.fn()) })
  afterEach(() => vi.unstubAllGlobals())

  it('encodes status, cursor, limit, and route identifiers', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: [], meta: { nextCursor: 'next' } })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: [], meta: { nextCursor: 'm-next' } })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { thread: { id: 'x' }, messages: [] } })))
    await listStudentQuestions({ status: 'waiting_student', limit: 20 }, 'a+b/=')
    await listStudentMessages('id /?', 'm+/=', 50)
    await getStudentQuestion('id /?')
    expect(vi.mocked(fetch).mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/student/questions?status=waiting_student&cursor=a%2Bb%2F%3D&limit=20',
      '/api/v1/student/questions/id%20%2F%3F/messages?cursor=m%2B%2F%3D&limit=50',
      '/api/v1/student/questions/id%20%2F%3F',
    ])
  })

  it('sends strict mutation bodies and the caller idempotency key', async () => {
    vi.mocked(fetch).mockImplementation(async () => new Response(JSON.stringify({ data: { thread: { id: 'q1' }, message: { id: 'm1' } } }), { status: 201 }))
    const key = crypto.randomUUID()
    await createQuestion({ title: 'Title', body: 'Body', attachments: [{ fileVersionId: 'v1', sortPosition: 0 }] }, key)
    await addStudentMessage('q1', { body: 'More', attachments: [] }, key, 4)
    const calls = vi.mocked(fetch).mock.calls
    expect((calls[0][1]?.headers as Headers).get('Idempotency-Key')).toBe(key)
    expect(JSON.parse(String(calls[0][1]?.body))).toEqual({ title: 'Title', body: 'Body', attachments: [{ fileVersionId: 'v1', sortPosition: 0 }] })
    expect(JSON.parse(String(calls[1][1]?.body))).toEqual({ body: 'More', attachments: [], expectedVersion: 4 })
  })

  it('preserves APIError request IDs', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ error: { code: 'invalid_request', message: '错误', requestId: 'req-qa' } }), { status: 400 }))
    await expect(listStudentQuestions({})).rejects.toMatchObject({ status: 400, code: 'invalid_request', requestId: 'req-qa' } satisfies Partial<APIError>)
  })
  it('reads the direct safe file-status DTO and creates UUID idempotency keys', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ fileVersionId: 'v/1', processingState: 'ready', detectedMime: 'application/pdf', size: 3, previewAvailable: true })))
    await expect(questionFileStatus('v/1')).resolves.toMatchObject({ fileVersionId: 'v/1', previewAvailable: true })
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/question-files/v%2F1/status')
    expect(newIdempotencyKey()).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  })
})
