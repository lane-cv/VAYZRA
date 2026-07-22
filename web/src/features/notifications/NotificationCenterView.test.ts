import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { routerKey } from 'vue-router'
import NotificationCenterView from './NotificationCenterView.vue'
import { useSessionStore } from '../../stores/session'
import { safeNotificationTarget } from './types'

const records = [
  { id: 'n1', kind: 'qa_replied', title: '<img src=x onerror=alert(1)>', summary: '<script>bad()</script>', targetType: 'qa_thread', targetId: 'q1', targetPath: '/student/questions/q1', createdAt: '2026-07-22T01:00:00Z' },
  { id: 'n2', kind: 'qa_replied', title: 'Unsafe', summary: 'No link', targetType: 'qa_thread', targetId: 'q2', targetPath: '//evil.test', createdAt: '2026-07-22T00:00:00Z' },
]

function render(role: 'student' | 'admin' = 'student') {
  const pinia = createPinia(), push = vi.fn()
  useSessionStore(pinia).setUser({ id: 'u1', username: 'u', displayName: 'User', role, mustChangePassword: false })
  const wrapper = mount(NotificationCenterView, { global: { plugins: [pinia], provide: { [routerKey as symbol]: { push } } } })
  return { wrapper, push }
}
describe('NotificationCenterView', () => {
  beforeEach(() => { document.cookie = 'hl_csrf=csrf; path=/'; vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: records, meta: { nextCursor: 'next' } })))) })
  afterEach(() => vi.unstubAllGlobals())
  it('renders server text escaped and exposes links only for the current role', async () => {
    const { wrapper } = render(); await flushPromises()
    expect(wrapper.find('img').exists()).toBe(false); expect(wrapper.find('script').exists()).toBe(false)
    expect(wrapper.findAll('a')).toHaveLength(1); expect(wrapper.get('a').attributes('href')).toBe('/student/questions/q1')
    expect(wrapper.text()).toContain('<img src=x onerror=alert(1)>')
  })
  it('tries mark-read but still navigates when it fails', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: records, meta: {} })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: { message: 'failed' } }), { status: 500 }))
    const { wrapper, push } = render(); await flushPromises(); await wrapper.get('a').trigger('click'); await flushPromises()
    expect(push).toHaveBeenCalledWith('/student/questions/q1')
  })
  it('aborts an in-flight mutation when the notification center unmounts', async () => {
    let signal: AbortSignal | undefined
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: records, meta: {} })))
      .mockImplementationOnce((_url, init) => { signal = init?.signal as AbortSignal; return new Promise(() => {}) })
    const { wrapper } = render(); await flushPromises(); await wrapper.get('li button').trigger('click'); await Promise.resolve()
    wrapper.unmount(); expect(signal?.aborted).toBe(true)
  })
  it('rejects another role path and encoded or scheme-relative paths', async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ data: records.map((record, index) => ({ ...record, targetPath: index ? '/student/%2f%2fevil' : '/admin/questions/q1' })), meta: {} })))
    const { wrapper } = render('student'); await flushPromises(); expect(wrapper.find('a').exists()).toBe(false)
  })
  it('accepts only normalized role-local path targets', () => {
    expect(safeNotificationTarget('/student/questions/abc', 'student')).toBe('/student/questions/abc')
    expect(safeNotificationTarget('/admin/questions/abc', 'admin')).toBe('/admin/questions/abc')
    for (const path of ['/student/../admin/questions/x', '/student/./questions/x', '/student/questions/../x', '/student/%2e%2e/admin', '/student//evil', '/student\\evil', '/student/questions/x?next=/admin/', '/student/questions/x#fragment', '/student/questions/\u007f', '/student/questions/\u0085']) {
      expect(safeNotificationTarget(path, 'student'), path).toBeUndefined()
    }
  })
})
