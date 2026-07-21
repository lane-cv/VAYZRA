export type StudentCatalogKind = 'grade' | 'term' | 'subject' | 'chapter' | 'lesson'
export type StudentCatalogNode = { id: string; parentId?: string; kind: StudentCatalogKind; name: string; description?: string; sortKey: number; lessonId?: string; currentRevisionId?: string; revisionStatus?: string }
export type StudentProgress = { viewed: boolean; anchor: string; scrollRatio: number; observedAt: string; firstViewedAt?: string; lastViewedAt?: string }
export type StudentVideo = { id: string; url: string; title: string; description: string; sortKey: number }
export type StudentFile = { fileVersionId: string; policy: 'preview' | 'download'; displayName: string; description: string; sortPosition: number; detectedMime: string; browserPlayable: boolean; previewAvailable: boolean }
export type StudentLesson = { lessonId: string; revisionId: string; version: number; title: string; summary: string; bodyMarkdown: string; sortKey: number; publishedAt: string; externalVideos: StudentVideo[]; files: StudentFile[]; progress?: StudentProgress | null }
export type SearchResult = { lessonId: string; revisionId: string; title: string; summary: string; snippet: string; gradeId: string; gradeName: string; termId: string; termName: string; subjectId: string; subjectName: string; chapterId: string; chapterName: string; revisionStatus: string }
export type RecentLesson = SearchResult & { position: StudentProgress }
