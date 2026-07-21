export type CatalogKind = 'grade' | 'term' | 'subject' | 'chapter' | 'lesson'
export type CatalogStatus = 'active' | 'archived' | 'draft' | 'published' | 'withdrawn'

export interface CatalogNode {
  id: string
  parentId: string
  kind: CatalogKind
  name: string
  description: string
  sortKey: number
  status: CatalogStatus
  published: boolean
}

export interface ExternalVideo {
  id: string
  url: string
  title: string
  description: string
  sortKey: number
}

export interface LessonAudience {
  mode: 'all' | 'selected'
  userIds: string[]
}

export interface LessonDraft {
  lessonId: string
  chapterId: string
  title: string
  summary: string
  bodyMarkdown: string
  sortKey: number
  lockVersion: number
  audience: LessonAudience
  externalVideos: ExternalVideo[]
  updatedAt: string
}

export interface LessonDetail {
  id: string
  chapterId: string
  status: 'draft' | 'published' | 'withdrawn' | 'archived'
  publishedRevisionId?: string
  draft: LessonDraft
}
