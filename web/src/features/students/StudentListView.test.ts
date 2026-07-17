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
      }), { status: 201 }))
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
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]])).mockResolvedValueOnce(new Response(null, { status: 204 }))
    const wrapper = mountStudentList()
    await flushPromises()
    await wrapper.get('button[aria-label="禁用 student01"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('确认禁用 student01')
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(1)
    await wrapper.get('button[aria-label="确认禁用 student01"]').trigger('click')
    await flushPromises()
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(2)
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
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[1]])).mockResolvedValueOnce(new Response(null, { status: 204 }))
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
    vi.mocked(fetch).mockResolvedValueOnce(listResponse([students[0]])).mockResolvedValueOnce(new Response(null, { status: 204 }))
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
})
