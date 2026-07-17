import { request, requestWithMeta } from '../../api/client'

export type StudentStatus = 'active' | 'disabled'

export type Student = {
  id: string
  username: string
  displayName: string
  status: StudentStatus
  mustChangePassword: boolean
  createdAt: string
}

export type StudentPage = { data: Student[]; nextCursor: string | null }

export async function listStudents(cursor?: string, signal?: AbortSignal): Promise<StudentPage> {
  const query = cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''
  const result = await requestWithMeta<Student[]>(`/admin/students${query}`, { signal })
  const nextCursor = typeof result.meta?.nextCursor === 'string' ? result.meta.nextCursor : null
  return { data: result.data, nextCursor }
}

export function createStudent(input: { username: string; displayName: string; temporaryPassword: string }): Promise<Student> {
  return request<Student>('/admin/students', { method: 'POST', json: input })
}

export function setStudentStatus(id: string, status: StudentStatus): Promise<void> {
  return request<void>(`/admin/students/${encodeURIComponent(id)}/status`, { method: 'POST', json: { status } })
}

export function resetStudentPassword(id: string, temporaryPassword: string): Promise<void> {
  return request<void>(`/admin/students/${encodeURIComponent(id)}/reset-password`, { method: 'POST', json: { temporaryPassword } })
}
