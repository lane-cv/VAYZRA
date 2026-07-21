export type ProcessingState = 'pending_scan' | 'processing' | 'ready' | 'rejected' | 'failed'
export type FileVersion = { id: string; fileId: string; version: number; displayName: string; declaredMime: string; detectedMime?: string; size: number; processingState: ProcessingState; failureCategory?: string; previewState?: string; browserPlayable: boolean; createdAt: string; retentionUntil?: string }
export type FileItem = { id: string; createdAt: string; deletedAt?: string; latest: FileVersion; referenceCount: number }
export type FileReference = { kind: 'draft' | 'published'; lessonId: string; lessonTitle: string; revisionId?: string }
export type FileDetail = { id: string; createdAt: string; deletedAt?: string; versions: FileVersion[]; references: FileReference[] }
export type FilePage = { items: FileItem[]; nextCursor?: string }
export type FileFilters = { q: string; type: string; state: string; reference: string }
