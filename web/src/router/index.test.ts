import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createAppRouter } from './index'
import { useSessionStore } from '../stores/session'
import AIQuestionDetailView from '../features/ai/AIQuestionDetailView.vue'
import AdminAIUsageView from '../features/ai/AdminAIUsageView.vue'
describe('console router guards', () => {
  beforeEach(() => setActivePinia(createPinia()))
  it('forces first-login students to change password', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: true }; const router = createAppRouter(); await router.push('/student'); expect(router.currentRoute.value.fullPath).toBe('/change-password')
  })
  it('redirects a student away from admin routes', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }; const router = createAppRouter(); await router.push('/admin'); expect(router.currentRoute.value.fullPath).toBe('/student')
  })
  it('allows students to open the learning space and lesson reader', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }; const router = createAppRouter(); await router.push('/student/learning'); expect(router.currentRoute.value.name).toBe('student-learning')
    await router.push('/student/learning/11111111-1111-4111-8111-111111111111'); expect(router.currentRoute.value.params.lessonId).toBe('11111111-1111-4111-8111-111111111111')
  })
  it('allows student question routes and keeps admins out', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }; const router = createAppRouter()
    await router.push('/student/questions'); expect(router.currentRoute.value.name).toBe('student-questions')
    await router.push('/student/questions/new'); expect(router.currentRoute.value.name).toBe('student-question-new')
    await router.push('/student/questions/ai/11111111-1111-4111-8111-111111111111'); expect(router.currentRoute.value.name).toBe('student-ai-question-detail')
    const matched = router.currentRoute.value.matched
    expect(matched[matched.length - 1]?.components?.default).toBe(AIQuestionDetailView)
    await router.push('/student/questions/teacher/22222222-2222-4222-8222-222222222222'); expect(router.currentRoute.value.name).toBe('student-teacher-question-detail')
    await router.push('/student/questions/teacher/not-a-uuid'); expect(router.currentRoute.value.name).toBe('student-questions')
    await router.push('/student/questions/33333333-3333-4333-8333-333333333333'); expect(router.currentRoute.value.fullPath).toBe('/student/questions/teacher/33333333-3333-4333-8333-333333333333')
    await router.push('/student/questions/not-a-uuid'); expect(router.currentRoute.value.name).toBe('student-questions')
    session.user = { id: 'a1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }; await router.push('/student/questions/new'); expect(router.currentRoute.value.fullPath).toBe('/admin')
  })
  it('preserves admin question routes while canonicalizing only student detail UUIDs', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }
    const router = createAppRouter()
    await router.push('/student/questions/ai/AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA')
    expect(router.currentRoute.value.name).toBe('student-questions')
    session.user = { id: 'a1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }
    await router.push('/admin/questions/11111111-1111-4111-8111-111111111111')
    expect(router.currentRoute.value.name).toBe('admin-question-detail')
    await router.push('/notifications')
    expect(router.currentRoute.value.name).toBe('notifications')
  })
  it('allows an admin to open the student management route', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }; const router = createAppRouter(); await router.push('/admin/students'); expect(router.currentRoute.value.fullPath).toBe('/admin/students')
  })
  it('exposes AI management only inside the admin route tree', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'a1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }
    const router = createAppRouter()
    await router.push('/admin/ai')
    expect(router.currentRoute.value.name).toBe('admin-ai')
    session.user = { id: 's1', username: 'student', displayName: '林同学', role: 'student', mustChangePassword: false }
    await router.push('/student')
    await router.push('/admin/ai')
    expect(router.currentRoute.value.fullPath).toBe('/student')
    const studentRoot = router.getRoutes().find((route) => route.path === '/student')
    expect(JSON.stringify(studentRoot?.children)).not.toContain('/admin/ai')
  })
  it('exposes AI usage only inside the admin route tree', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'a1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }
    const router = createAppRouter()
    await router.push('/admin/ai-usage')
    expect(router.currentRoute.value.name).toBe('admin-ai-usage')
    const matched = router.currentRoute.value.matched
    expect(matched[matched.length - 1]?.components?.default).toBe(AdminAIUsageView)
    session.user = { id: 's1', username: 'student', displayName: '林同学', role: 'student', mustChangePassword: false }
    await router.push('/student')
    await router.push('/admin/ai-usage')
    expect(router.currentRoute.value.fullPath).toBe('/student')
  })
  it('allows only admins to open canonical teacher question routes in one persistent workspace',async()=>{const session=useSessionStore();session.bootstrapStatus='ready';session.user={id:'a1',username:'teacher',displayName:'张老师',role:'admin',mustChangePassword:false};const router=createAppRouter();await router.push('/admin/questions');expect(router.currentRoute.value.name).toBe('admin-questions');const workspace=router.currentRoute.value.matched[1].components?.default;await router.push('/admin/questions/not-a-uuid');expect(router.currentRoute.value.name).toBe('admin-questions');await router.push('/admin/questions/11111111-1111-4111-8111-111111111111');expect(router.currentRoute.value.name).toBe('admin-question-detail');expect(router.currentRoute.value.matched[1].components?.default).toBe(workspace);session.user={id:'s1',username:'student',displayName:'学生',role:'student',mustChangePassword:false};await router.push('/admin/questions');expect(router.currentRoute.value.fullPath).toBe('/student')})
  it('allows either authenticated role on the role-neutral notification route',async()=>{const session=useSessionStore();session.bootstrapStatus='ready';session.user={id:'a1',username:'teacher',displayName:'张老师',role:'admin',mustChangePassword:false};const router=createAppRouter();await router.push('/notifications');expect(router.currentRoute.value.name).toBe('notifications');session.user={id:'s1',username:'student',displayName:'学生',role:'student',mustChangePassword:false};await router.push('/student');await router.push('/notifications');expect(router.currentRoute.value.name).toBe('notifications')})
  it('allows an admin to open teaching management and rejects a student', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }; const router = createAppRouter(); await router.push('/admin/teaching'); expect(router.currentRoute.value.fullPath).toBe('/admin/teaching')
    await router.push('/admin'); session.user = { id: 'u2', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }; await router.push('/admin/teaching'); expect(router.currentRoute.value.fullPath).toBe('/student')
  })
  it('allows an admin to open the file center', async () => {
    const session = useSessionStore(); session.bootstrapStatus = 'ready'; session.user = { id: 'u1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false }; const router = createAppRouter(); await router.push('/admin/files'); expect(router.currentRoute.value.fullPath).toBe('/admin/files')
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
