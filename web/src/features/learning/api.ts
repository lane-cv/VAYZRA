import { request } from '../../api/client'
import type { RecentLesson, SearchResult, StudentCatalogKind, StudentCatalogNode, StudentLesson } from './types'

export type CatalogFilters = { gradeId?: string; termId?: string; subjectId?: string; chapterId?: string }
export function browseCatalog(kind: StudentCatalogKind, filters: CatalogFilters = {}, signal?: AbortSignal): Promise<StudentCatalogNode[]> {
  const query = new URLSearchParams({ kind, limit: '50' })
  for (const [key, value] of Object.entries(filters)) if (value) query.set(key, value)
  return request<StudentCatalogNode[]>(`/student/catalog?${query.toString()}`, { signal })
}
export function getStudentLesson(id: string, signal?: AbortSignal): Promise<StudentLesson> { return request<StudentLesson>(`/student/lessons/${encodeURIComponent(id)}`, { signal }) }
export function searchLessons(query: string, signal?: AbortSignal): Promise<SearchResult[]> { return request<SearchResult[]>(`/student/search?q=${encodeURIComponent(query.trim())}&limit=20`, { signal }) }
export function updateProgress(input: { revisionId: string; viewed: boolean; anchor: string; scrollRatio: number; observedAt: string }, keepalive = false): Promise<void> { return request<void>('/student/progress', { method: 'POST', json: input, keepalive }) }
export function recentLessons(signal?: AbortSignal): Promise<RecentLesson[]> { return request<RecentLesson[]>('/student/lessons/recent?limit=5', { signal }) }
