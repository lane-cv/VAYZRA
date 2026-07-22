import { APIError, request, requestWithMeta } from '../../api/client'
import type { NotificationItem, NotificationKind, NotificationPage } from './types'

const kinds = new Set<NotificationKind>(['qa_created', 'qa_replied', 'qa_followed_up', 'qa_status_changed', 'lesson_published'])
function item(value: unknown): NotificationItem {
  if (!value || typeof value !== 'object') throw invalid()
  const v = value as Record<string, unknown>
  for (const key of ['id', 'kind', 'title', 'summary', 'targetType', 'targetId', 'targetPath', 'createdAt']) if (typeof v[key] !== 'string') throw invalid()
  if (!kinds.has(v.kind as NotificationKind) || (v.readAt !== undefined && typeof v.readAt !== 'string')) throw invalid()
  return v as NotificationItem
}
function invalid() { return new APIError(200, 'invalid_response', '服务响应异常，请稍后重试', '') }

export async function listNotifications(cursor?: string, limit = 20, signal?: AbortSignal): Promise<NotificationPage> {
  const query = new URLSearchParams()
  if (cursor) query.set('cursor', cursor)
  query.set('limit', String(limit))
  const result = await requestWithMeta<unknown>(`/notifications?${query.toString()}`, { signal })
  if (!Array.isArray(result.data)) throw invalid()
  const next = result.meta?.nextCursor
  if (next !== undefined && typeof next !== 'string') throw invalid()
  return { items: result.data.map(item), nextCursor: next || undefined }
}
export async function unreadNotificationCount(signal?: AbortSignal): Promise<number> {
  const data = await request<unknown>('/notifications/unread-count', { signal })
  if (!data || typeof data !== 'object' || !Number.isSafeInteger((data as { count?: unknown }).count) || Number((data as { count: number }).count) < 0) throw invalid()
  return (data as { count: number }).count
}
export async function markNotificationRead(id: string): Promise<void> {
  const data = await request<unknown>(`/notifications/${encodeURIComponent(id)}/read`, { method: 'POST', json: {} })
  if (!data || typeof data !== 'object' || Array.isArray(data) || Object.keys(data).length !== 0) throw invalid()
}
export async function markAllNotificationsRead(): Promise<number> {
  const data = await request<unknown>('/notifications/read-all', { method: 'POST', json: {} })
  if (!data || typeof data !== 'object' || !Number.isSafeInteger((data as { count?: unknown }).count) || Number((data as { count: number }).count) < 0) throw invalid()
  return (data as { count: number }).count
}
