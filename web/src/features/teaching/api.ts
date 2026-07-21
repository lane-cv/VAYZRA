import { request } from '../../api/client'
import type { CatalogKind, CatalogNode, LessonDetail, LessonDraft, LessonRevision } from './types'

export async function listCatalog(signal?: AbortSignal): Promise<CatalogNode[]> {
  return request<CatalogNode[]>('/admin/catalog?limit=200', { signal })
}

export function createCatalog(input: { kind: Exclude<CatalogKind, 'lesson'>; parentId: string; name: string; description?: string; sortKey: number }): Promise<CatalogNode> {
  const { kind, ...json } = input
  return request<CatalogNode>(`/admin/catalog/${kind}`, { method: 'POST', json })
}

export function renameCatalog(node: CatalogNode, name: string): Promise<CatalogNode> {
  return request<CatalogNode>(`/admin/catalog/${node.kind}/${encodeURIComponent(node.id)}`, { method: 'PATCH', json: { name } })
}

export function reorderCatalog(node: CatalogNode, sortKey: number): Promise<void> {
  return request<void>(`/admin/catalog/${node.kind}/${encodeURIComponent(node.id)}/reorder`, { method: 'POST', json: { sortKey } })
}

export function setCatalogArchived(node: CatalogNode, archived: boolean): Promise<void> {
  return request<void>(`/admin/catalog/${node.kind}/${encodeURIComponent(node.id)}/archive`, { method: 'POST', json: { archived } })
}

export function getLesson(id: string, signal?: AbortSignal): Promise<LessonDetail> {
  return request<LessonDetail>(`/admin/lessons/${encodeURIComponent(id)}`, { signal })
}

export function saveDraft(draft: LessonDraft): Promise<LessonDraft> {
  return request<LessonDraft>(`/admin/lessons/${encodeURIComponent(draft.lessonId)}/draft`, {
    method: 'PUT',
    headers: { 'If-Match': String(draft.lockVersion) },
    json: {
      title: draft.title,
      summary: draft.summary,
      bodyMarkdown: draft.bodyMarkdown,
      sortKey: draft.sortKey,
      audience: draft.audience,
      externalVideos: draft.externalVideos,
    },
  })
}

export function createLesson(chapterId: string, title: string): Promise<LessonDraft> {
  return request<LessonDraft>('/admin/lessons', { method: 'POST', json: { chapterId, title } })
}

export function publishLesson(lessonId: string, lockVersion: number): Promise<LessonRevision> {
  return request<LessonRevision>(`/admin/lessons/${encodeURIComponent(lessonId)}/publish`, {
    method: 'POST',
    headers: { 'If-Match': String(lockVersion) },
  })
}
