import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
const { request } = vi.hoisted(() => ({ request: vi.fn() }))
vi.mock('../api/client', () => ({ request, registerUnauthorizedHandler: vi.fn(), APIError: class APIError extends Error { status = 0 } }))
import { useSessionStore } from './session'
describe('session store', () => {
  beforeEach(() => { setActivePinia(createPinia()); request.mockReset() })
  it('bootstraps the safe current user only once', async () => {
    request.mockResolvedValue({ id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }); const session = useSessionStore()
    await Promise.all([session.bootstrap(), session.bootstrap()])
    expect(request).toHaveBeenCalledTimes(1); expect(session.user).toMatchObject({ username: 'student01', role: 'student' }); expect(session.bootstrapStatus).toBe('ready')
  })
  it('clears only in-memory user data when the session ends', () => {
    const session = useSessionStore(); session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }; session.clear()
    expect(session.user).toBeNull(); expect(session.bootstrapStatus).toBe('ready')
  })
  it('keeps a transient bootstrap failure retryable', async () => {
    request.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({ id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false })
    const session = useSessionStore()
    await expect(session.bootstrap()).rejects.toThrow('offline')
    expect(session.bootstrapStatus).toBe('idle')
    await session.bootstrap()
    expect(session.user?.username).toBe('student01')
    expect(request).toHaveBeenCalledTimes(2)
  })
})