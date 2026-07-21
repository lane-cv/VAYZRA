import { request } from '../../api/client'
import type { FileDetail, FileFilters, FilePage } from './types'

export function listFiles(filters: FileFilters, cursor = '', signal?: AbortSignal): Promise<FilePage> {
  const query = new URLSearchParams()
  if (filters.q.trim()) query.set('q', filters.q.trim())
  if (filters.type) query.set('type', filters.type)
  if (filters.state) query.set('state', filters.state)
  if (filters.reference) query.set('reference', filters.reference)
  query.set('limit', '25')
  if (cursor) query.set('cursor', cursor)
  return request<FilePage>(`/admin/files/?${query.toString()}`, { signal })
}
export function fileDetail(id: string, signal?: AbortSignal): Promise<FileDetail> { return request<FileDetail>(`/admin/files/${encodeURIComponent(id)}`, { signal }) }
export function retryFile(fileId: string, versionId: string): Promise<void> { return request<void>(`/admin/files/${encodeURIComponent(fileId)}/versions/${encodeURIComponent(versionId)}/retry`, { method: 'POST', json: {} }) }
export function replaceFile(fileId: string, uploadedVersionId: string): Promise<void> { return request<void>(`/admin/files/${encodeURIComponent(fileId)}/replace`, { method: 'POST', json: { uploadedVersionId } }) }
export function rollbackFile(fileId: string, lessonId: string, fileVersionId: string): Promise<void> { return request<void>(`/admin/files/${encodeURIComponent(fileId)}/rollback`, { method: 'POST', json: { lessonId, fileVersionId } }) }
export function deleteFile(fileId: string): Promise<void> { return request<void>(`/admin/files/${encodeURIComponent(fileId)}`, { method: 'DELETE' }) }
