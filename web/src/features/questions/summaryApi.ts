import { requestWithMeta } from '../../api/client'
import type { QuestionSummary, QuestionSummaryChannel, QuestionSummaryPage } from './types'

export type QuestionSummaryFilters = {
  channel?: QuestionSummaryChannel
  search?: string
  cursor?: string
  limit?: number
}

export function parseSummaryChannel(value: unknown): QuestionSummaryChannel | '' {
  return value === 'ai' || value === 'teacher' ? value : ''
}

export async function listQuestionSummaries(
  filters: QuestionSummaryFilters = {},
  signal?: AbortSignal,
): Promise<QuestionSummaryPage> {
  const query = new URLSearchParams()
  if (filters.channel) query.set('channel', filters.channel)
  const search = filters.search?.trim()
  if (search) query.set('search', search)
  if (filters.cursor) query.set('cursor', filters.cursor)
  if (filters.limit !== undefined) query.set('limit', String(filters.limit))
  const suffix = query.size ? `?${query.toString()}` : ''
  const result = await requestWithMeta<QuestionSummary[]>(`/student/question-summaries${suffix}`, { signal })
  const nextCursor = typeof result.meta?.nextCursor === 'string' && result.meta.nextCursor
    ? result.meta.nextCursor
    : undefined
  return { items: result.data, nextCursor }
}
