export type NotificationKind = 'qa_created' | 'qa_replied' | 'qa_followed_up' | 'qa_status_changed' | 'lesson_published'
export type NotificationItem = {
  id: string
  kind: NotificationKind
  title: string
  summary: string
  targetType: string
  targetId: string
  targetPath: string
  readAt?: string
  createdAt: string
}
export type NotificationPage = { items: NotificationItem[]; nextCursor?: string }
