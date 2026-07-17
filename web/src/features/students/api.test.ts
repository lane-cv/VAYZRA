import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createStudent, listStudents, resetStudentPassword, setStudentStatus } from './api'

describe('student API', () => {
  beforeEach(() => {
    document.cookie = 'hl_csrf=csrf-value; path=/'
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => vi.unstubAllGlobals())

  it('uses only the supported list/create/status/reset HTTP shapes', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: [], meta: { nextCursor: 'cursor-2' } })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { id: 's1', username: 'student01', displayName: '林同学', status: 'active', mustChangePassword: true, createdAt: '2026-07-18T08:00:00Z' } }), { status: 201 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await expect(listStudents('cursor 1')).resolves.toEqual({ data: [], nextCursor: 'cursor-2' })
    await createStudent({ username: 'student01', displayName: '林同学', temporaryPassword: 'Temporary Password 42!' })
    await setStudentStatus('s1', 'disabled')
    await resetStudentPassword('s1', 'Different Temporary Password 42!')

    expect(vi.mocked(fetch).mock.calls.map(([path]) => path)).toEqual([
      '/api/v1/admin/students?cursor=cursor%201',
      '/api/v1/admin/students',
      '/api/v1/admin/students/s1/status',
      '/api/v1/admin/students/s1/reset-password',
    ])
    const createBody = JSON.parse((vi.mocked(fetch).mock.calls[1][1] as RequestInit).body as string)
    expect(createBody).toEqual({ username: 'student01', displayName: '林同学', temporaryPassword: 'Temporary Password 42!' })
    expect(createBody).not.toHaveProperty('role')
    expect(createBody).not.toHaveProperty('id')
    expect(JSON.parse((vi.mocked(fetch).mock.calls[2][1] as RequestInit).body as string)).toEqual({ status: 'disabled' })
    expect(JSON.parse((vi.mocked(fetch).mock.calls[3][1] as RequestInit).body as string)).toEqual({ temporaryPassword: 'Different Temporary Password 42!' })
  })
})
