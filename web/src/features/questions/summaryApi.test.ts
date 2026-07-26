import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { listQuestionSummaries, parseSummaryChannel } from './summaryApi'

const summaries = [
  {
    id: '11111111-1111-4111-8111-111111111111',
    channel: 'ai',
    title: '函数题',
    rawStatus: 'streaming',
    lastMessageAt: '2026-07-26T08:00:00Z',
    createdAt: '2026-07-26T07:00:00Z',
  },
  {
    id: '22222222-2222-4222-8222-222222222222',
    channel: 'teacher',
    title: '受力分析',
    rawStatus: 'pending',
    lastMessageAt: '2026-07-26T08:00:00Z',
    createdAt: '2026-07-26T06:00:00Z',
  },
]

describe('question summary API', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => vi.unstubAllGlobals())

  it('encodes channel, title search, equal-time cursor and limit without changing the canonical summaries', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: summaries,
      meta: { nextCursor: 'same-time:teacher:id' },
    })))

    await expect(listQuestionSummaries({
      channel: 'teacher',
      search: '  函数 + 受力  ',
      cursor: 'same-time:ai:id',
      limit: 20,
    })).resolves.toEqual({ items: summaries, nextCursor: 'same-time:teacher:id' })

    expect(vi.mocked(fetch).mock.calls[0][0]).toBe(
      '/api/v1/student/question-summaries?channel=teacher&search=%E5%87%BD%E6%95%B0+%2B+%E5%8F%97%E5%8A%9B&cursor=same-time%3Aai%3Aid&limit=20',
    )
  })

  it('accepts only canonical UI channel values', () => {
    expect(parseSummaryChannel(undefined)).toBe('')
    expect(parseSummaryChannel('')).toBe('')
    expect(parseSummaryChannel('ai')).toBe('ai')
    expect(parseSummaryChannel('teacher')).toBe('teacher')
    expect(parseSummaryChannel(['ai'])).toBe('')
    expect(parseSummaryChannel('admin')).toBe('')
  })

  it('propagates API errors and their support request id', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      error: { code: 'temporarily_unavailable', message: '暂不可用', requestId: 'req-summary' },
    }), { status: 503 }))

    const error = await listQuestionSummaries({ limit: 20 }).catch((cause: unknown) => cause)
    expect(error).toBeInstanceOf(APIError)
    expect(error).toMatchObject({ status: 503, code: 'temporarily_unavailable', requestId: 'req-summary' })
  })
})
