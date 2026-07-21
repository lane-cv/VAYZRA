import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createAppRouter } from './index'
import { useSessionStore } from '../stores/session'
describe('console router guards', () => {
  beforeEach(() => setActivePinia(createPinia()))
  it('forces first-login students to change password', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: true }; const router = createAppRouter(); await router.push('/student'); expect(router.currentRoute.value.fullPath).toBe('/change-password')
  })
  it('redirects a student away from admin routes', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }; const router = createAppRouter(); await router.push('/admin'); expect(router.currentRoute.value.fullPath).toBe('/student')
  })
  it('allows an admin to open the student management route', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }; const router = createAppRouter(); await router.push('/admin/students'); expect(router.currentRoute.value.fullPath).toBe('/admin/students')
  })
  it('allows an admin to open teaching management and rejects a student', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }; const router = createAppRouter(); await router.push('/admin/teaching'); expect(router.currentRoute.value.fullPath).toBe('/admin/teaching')
    await router.push('/admin'); session.user = { id: 'u2', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }; await router.push('/admin/teaching'); expect(router.currentRoute.value.fullPath).toBe('/student')
  })
  it('redirects an authenticated user away from login without a loop', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }; const router = createAppRouter(); await router.push('/login'); expect(router.currentRoute.value.fullPath).toBe('/admin')
  })
  it('keeps a transient bootstrap failure retryable while reaching login without a loop', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(new Response(JSON.stringify({ data: { id: 'u1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false } })))
    const session = useSessionStore(); const router = createAppRouter()
    await router.push('/student')
    expect(router.currentRoute.value.fullPath).toBe('/login'); expect(session.bootstrapStatus).toBe('idle'); expect(session.user).toBeNull()
    await router.push('/student')
    expect(router.currentRoute.value.fullPath).toBe('/admin'); expect(session.bootstrapStatus).toBe('ready'); expect(session.user?.role).toBe('admin')
  })
})
