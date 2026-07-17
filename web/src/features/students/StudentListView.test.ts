import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import StudentListView from './StudentListView.vue'
import { useSessionStore } from '../../stores/session'

const students = [
  { id: 's1', username: 'student01', displayName: '林同学', status: 'active' as const, mustChangePassword: true, createdAt: '2026-07-18T08:00:00Z' },
  { id: 's2', username: 'student02', displayName: '陈同学', status: 'disabled' as const, mustChangePassword: false, createdAt: '2026-07-17T08:00:00Z' },
]

function listResponse(data = students, nextCursor: string | null = null) {
  return new Response(JSON.stringify({ data, meta: { nextCursor } }))
}

function adminSession() {
  const session = useSessionStore()
  session.setUser({ id: 'teacher-1', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false })
}

function mountStudentList() {
  adminSession()
  return mount(StudentListView, { attachTo: document.body })
}

describe('StudentListView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = 'hl_csrf=csrf-value; path=/'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(listResponse([])))
  })

  afterEach(() => vi.unstubAllGlobals())

  it('creates a student without retaining the temporary password', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([]))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: { id: 's1', username: 'student01', displayName: '林同学', status: 'active', mustChangePassword: true, createdAt: '2026-07-18T08:00:00Z' },
      }), { status: 201 })).mockResolvedValueOnce(listResponse([{ id: 's1', username: 'student01', displayName: '林同学', status: 'active', mustChangePassword: true, createdAt: '2026-07-18T08:00:00Z' }]))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('[aria-label="创建学生"]').trigger('click')
    await wrapper.get('[aria-label="学生账号"]').setValue('student01')
    await wrapper.get('[aria-label="学生姓名"]').setValue('林同学')
    await wrapper.get('[aria-label="临时密码"]').setValue('Temporary Password 42!')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.text()).toContain('student01')
    expect(wrapper.html()).not.toContain('Temporary Password 42!')
  })

  it('shows loading, empty, and retryable error states with the request ID', async () => {
    let resolveList: ((value: Response) => void) | undefined
    vi.mocked(fetch).mockImplementationOnce(() => new Promise<Response>((resolve) => { resolveList = resolve }))
    const wrapper = mountStudentList()
    expect(wrapper.get('[role="status"]').text()).toContain('正在加载学生')
    resolveList?.(new Response(JSON.stringify({ error: { code: 'internal_error', message: '服务暂不可用', requestId: 'req-students-1' } }), { status: 500 }))
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('支持编号：req-students-1')
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([]))
    await wrapper.get('button[aria-label="重试加载学生"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('还没有学生账号')
  })

  it('uses bounded keyset pagination and renders realistic status states', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]], 'cursor-1')).mockResolvedValueOnce(listResponse([students[1]]))
    const wrapper = mountStudentList()
    await flushPromises()
    expect(wrapper.text()).toContain('正常')
    expect(wrapper.text()).toContain('首次登录需修改密码')
    await wrapper.get('button[aria-label="下一页学生"]').trigger('click')
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls[1][0]).toBe('/api/v1/admin/students?cursor=cursor-1')
    expect(wrapper.text()).toContain('已停用')
    expect(wrapper.text()).toContain('已完成首次密码修改')
    expect(wrapper.find('input[aria-label="跳转页码"]').exists()).toBe(false)
  })

  it('requires confirmation before disabling a student and updates the row immediately', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]])).mockResolvedValueOnce(new Response(null, { status: 204 })).mockResolvedValueOnce(listResponse([{ ...students[0], status: 'disabled' }]))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="禁用 student01"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('确认禁用 student01')
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(1)
    await wrapper.get('button[aria-label="确认禁用 student01"]').trigger('click')
    await flushPromises()
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(3)
    expect(wrapper.text()).toContain('已停用')
  })

  it('requires target-named confirmation before resetting a password and clears it after an error', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]])).mockResolvedValueOnce(new Response(JSON.stringify({
      error: { code: 'invalid_request', message: '请求参数无效', requestId: 'req-reset-1' },
    }), { status: 400 }))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="重置 student01 的密码"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('确认重置 student01 的临时密码')
    await wrapper.get('[aria-label="重置临时密码"]').setValue('Temporary Password 42!')
    await wrapper.get('button[aria-label="确认重置 student01 的密码"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('支持编号：req-reset-1')
    expect((wrapper.get('[aria-label="重置临时密码"]').element as HTMLInputElement).value).toBe('')
  })

  it('prevents duplicate create submissions while the request is pending', async () => {
    let resolveCreate: ((value: Response) => void) | undefined
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([])).mockImplementationOnce(() => new Promise<Response>((resolve) => { resolveCreate = resolve }))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('[aria-label="创建学生"]').trigger('click')
    await wrapper.get('[aria-label="学生账号"]').setValue('student01')
    await wrapper.get('[aria-label="学生姓名"]').setValue('林同学')
    await wrapper.get('[aria-label="临时密码"]').setValue('Temporary Password 42!')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(2)
    resolveCreate?.(new Response(JSON.stringify({ data: students[0] }), { status: 201 }))
    await flushPromises()
  })

  it('does not render administration controls for a non-admin session', async () => {
    const session = useSessionStore()
    session.setUser({ id: 'student-1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false })
    const wrapper = mount(StudentListView)
    await flushPromises()
    expect(wrapper.text()).toContain('无权访问学生管理')
    expect(wrapper.find('[aria-label="创建学生"]').exists()).toBe(false)
    expect(vi.mocked(fetch)).not.toHaveBeenCalled()
  })
  it('requires target-named confirmation before enabling a student and updates the row immediately', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[1]])).mockResolvedValueOnce(new Response(null, { status: 204 })).mockResolvedValueOnce(listResponse([{ ...students[1], status: 'active' }]))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="启用 student02"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('确认启用 student02')
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(1)
    await wrapper.get('button[aria-label="确认启用 student02"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('正常')
  })

  it('traps Tab focus in a dialog and restores focus after Escape', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]]))
    const wrapper = mountStudentList()
    await flushPromises()
    const trigger = wrapper.get('button[aria-label="禁用 student01"]')
    await trigger.trigger('click')
    await flushPromises()
    const controls = wrapper.findAll('[role="dialog"] button')
    const first = controls[0]
    const last = controls[1]
    ;(last.element as HTMLElement).focus()
    await last.trigger('keydown', { key: 'Tab' })
    expect(document.activeElement).toBe(first.element)
    await first.trigger('keydown', { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last.element)
    await last.trigger('keydown', { key: 'Escape' })
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(document.activeElement).toBe(trigger.element)
  })

  it('clears a create password after failure and cancellation', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([])).mockResolvedValueOnce(new Response(JSON.stringify({
      error: { code: 'invalid_request', message: '请求参数无效', requestId: 'req-create-1' },
    }), { status: 400 }))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('[aria-label="创建学生"]').trigger('click')
    await wrapper.get('[aria-label="学生账号"]').setValue('student01')
    await wrapper.get('[aria-label="学生姓名"]').setValue('林同学')
    await wrapper.get('[aria-label="临时密码"]').setValue('Temporary Password 42!')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect((wrapper.get('[aria-label="临时密码"]').element as HTMLInputElement).value).toBe('')
    await wrapper.get('[role="dialog"] button[type="button"]').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    await wrapper.get('[aria-label="创建学生"]').trigger('click')
    expect((wrapper.get('[aria-label="临时密码"]').element as HTMLInputElement).value).toBe('')
  })

  it('clears a reset password after successful completion', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]])).mockResolvedValueOnce(new Response(null, { status: 204 })).mockResolvedValueOnce(listResponse([{ ...students[0], status: 'disabled' }]))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="重置 student01 的密码"]').trigger('click')
    await wrapper.get('[aria-label="重置临时密码"]').setValue('Temporary Password 42!')
    await wrapper.get('button[aria-label="确认重置 student01 的密码"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    await wrapper.get('button[aria-label="重置 student01 的密码"]').trigger('click')
    expect((wrapper.get('[aria-label="重置临时密码"]').element as HTMLInputElement).value).toBe('')
  })
  it('retries a failed next-page request with its staged cursor without changing history', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]], 'cursor-b')).mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: 'internal_error', message: '服务暂不可用', requestId: 'next-failed' } }), { status: 500 })).mockResolvedValueOnce(listResponse([students[1]]))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="下一页学生"]').trigger('click')
    await flushPromises()
    await wrapper.get('button[aria-label="重试加载学生"]').trigger('click')
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls.map(([path]) => path)).toEqual(['/api/v1/admin/students', '/api/v1/admin/students?cursor=cursor-b', '/api/v1/admin/students?cursor=cursor-b'])
    expect(wrapper.get('button[aria-label="上一页学生"]').attributes('disabled')).toBeUndefined()
  })

  it('retries a failed previous-page request with the staged prior cursor', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]], 'cursor-b')).mockResolvedValueOnce(listResponse([students[1]])).mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: 'internal_error', message: '服务暂不可用', requestId: 'previous-failed' } }), { status: 500 })).mockResolvedValueOnce(listResponse([students[0]], 'cursor-b'))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="下一页学生"]').trigger('click')
    await flushPromises()
    await wrapper.get('button[aria-label="上一页学生"]').trigger('click')
    await flushPromises()
    await wrapper.get('button[aria-label="重试加载学生"]').trigger('click')
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls.map(([path]) => path)).toEqual(['/api/v1/admin/students', '/api/v1/admin/students?cursor=cursor-b', '/api/v1/admin/students', '/api/v1/admin/students'])
    expect(wrapper.text()).toContain('student01')
  })

  it('disables both pagination controls while a staged navigation request is pending', async () => {
    let resolveNext: ((value: Response) => void) | undefined
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]], 'cursor-b')).mockImplementationOnce(() => new Promise<Response>((resolve) => { resolveNext = resolve }))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="下一页学生"]').trigger('click')
    expect(wrapper.get('button[aria-label="上一页学生"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('button[aria-label="下一页学生"]').attributes('disabled')).toBeDefined()
    resolveNext?.(listResponse([students[1]]))
    await flushPromises()
  })

  it('refreshes the canonical first page after creating from a later cursor without local prepending', async () => {
    const created = { id: 's3', username: 'student03', displayName: '周同学', status: 'active' as const, mustChangePassword: true, createdAt: '2026-07-18T10:00:00Z' }
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]], 'cursor-b')).mockResolvedValueOnce(listResponse([students[1]])).mockResolvedValueOnce(new Response(JSON.stringify({ data: created }), { status: 201 })).mockResolvedValueOnce(listResponse([created, students[0]], 'cursor-b'))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="下一页学生"]').trigger('click')
    await flushPromises()
    await wrapper.get('[aria-label="创建学生"]').trigger('click')
    await wrapper.get('[aria-label="学生账号"]').setValue('student03')
    await wrapper.get('[aria-label="学生姓名"]').setValue('周同学')
    await wrapper.get('[aria-label="临时密码"]').setValue('Temporary Password 42!')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls.map(([path]) => path)).toEqual(['/api/v1/admin/students', '/api/v1/admin/students?cursor=cursor-b', '/api/v1/admin/students', '/api/v1/admin/students'])
    expect(wrapper.text()).toContain('student03')
    expect(wrapper.text()).not.toContain('student02')
  })

  it('does not resubmit a created student when its canonical refresh fails', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([])).mockResolvedValueOnce(new Response(JSON.stringify({ data: students[0] }), { status: 201 })).mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: 'internal_error', message: '服务暂不可用', requestId: 'refresh-failed' } }), { status: 500 })).mockResolvedValueOnce(listResponse([students[0]]))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('[aria-label="创建学生"]').trigger('click')
    await wrapper.get('[aria-label="学生账号"]').setValue('student01')
    await wrapper.get('[aria-label="学生姓名"]').setValue('林同学')
    await wrapper.get('[aria-label="临时密码"]').setValue('Temporary Password 42!')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('学生已创建，但列表刷新失败')
    await wrapper.get('button[aria-label="重试加载学生"]').trigger('click')
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls.filter(([, init]) => (init as RequestInit).method === 'POST').length).toBe(1)
  })

  it('uses a native modal and makes the management background inert until close or unmount', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([]))
    const wrapper = mountStudentList()
    await flushPromises()
    const background = wrapper.get('[data-testid="student-management-background"]')
    await wrapper.get('[aria-label="创建学生"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('dialog').element.tagName).toBe('DIALOG')
    expect((wrapper.get('dialog').element as HTMLDialogElement).open).toBe(true)
    expect(background.attributes('inert')).toBeDefined()
    await wrapper.get('[role="dialog"] button[type="button"]').trigger('click')
    await flushPromises()
    expect(background.attributes('inert')).toBeUndefined()
    await wrapper.get('[aria-label="创建学生"]').trigger('click')
    wrapper.unmount()
    expect(background.attributes('inert')).toBeUndefined()
  })
  it('prevents duplicate status and reset submissions while each operation is pending', async () => {
    let resolveStatus: ((value: Response) => void) | undefined
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]])).mockImplementationOnce(() => new Promise<Response>((resolve) => { resolveStatus = resolve })).mockResolvedValueOnce(listResponse([{ ...students[0], status: 'disabled' }])).mockImplementationOnce(() => new Promise<Response>((resolve) => { resolveStatus = resolve })).mockResolvedValueOnce(listResponse([{ ...students[0], status: 'disabled', mustChangePassword: true }]))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="禁用 student01"]').trigger('click')
    await wrapper.get('button[aria-label="确认禁用 student01"]').trigger('click')
    await wrapper.get('button[aria-label="确认禁用 student01"]').trigger('click')
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(2)
    resolveStatus?.(new Response(null, { status: 204 }))
    await flushPromises()
    await wrapper.get('button[aria-label="重置 student01 的密码"]').trigger('click')
    await wrapper.get('[aria-label="重置临时密码"]').setValue('Temporary Password 42!')
    await wrapper.get('button[aria-label="确认重置 student01 的密码"]').trigger('click')
    await wrapper.get('button[aria-label="确认重置 student01 的密码"]').trigger('click')
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(4)
  })
})
