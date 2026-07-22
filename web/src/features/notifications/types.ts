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

export function safeNotificationTarget(path: string, role: 'admin' | 'student' | undefined): string | undefined {
  const prefix = role === 'admin' ? '/admin/' : role === 'student' ? '/student/' : ''
  if (!prefix || !path.startsWith(prefix) || path.includes('//') || path.includes('\\') || path.includes('%') || path.includes('?') || path.includes('#') || /[\u0000-\u001f\u007f-\u009f]/.test(path)) return undefined
  if (path.split('/').some((segment) => segment === '.' || segment === '..')) return undefined
  const base = 'https://happylearn.invalid'
  let target: URL
  try { target = new URL(path, base) } catch { return undefined }
  if (target.origin !== base || target.search || target.hash || target.pathname !== path || !target.pathname.startsWith(prefix)) return undefined
  return target.pathname
}
