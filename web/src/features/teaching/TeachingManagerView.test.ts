import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import TeachingManagerView from './TeachingManagerView.vue'
import { useSessionStore } from '../../stores/session'

function response(data: unknown, status = 200) {
  return new Response(JSON.stringify({ data }), { status })
}

function mountView(role: 'admin' | 'student' = 'admin') {
  const pinia = createPinia()
  useSessionStore(pinia).setUser({ id: 'u1', username: role === 'admin' ? 'teacher' : 'student01', displayName: '用户', role, mustChangePassword: false })
  return mount(TeachingManagerView, { attachTo: document.body, global: { plugins: [pinia] } })
}

describe('TeachingManagerView', () => {
  beforeEach(() => {
    document.cookie = 'hl_csrf=csrf-value; path=/'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response([])))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows loading and then an accessible empty catalog state', async () => {
    let resolveLoad: ((value: Response) => void) | undefined
    vi.mocked(fetch).mockImplementationOnce(() => new Promise((resolve) => { resolveLoad = resolve }))
    const wrapper = mountView()
    expect(wrapper.get('[role="status"]').text()).toContain('正在加载教学目录')
    resolveLoad?.(response([]))
    await flushPromises()
    expect(wrapper.text()).toContain('还没有教学目录')
    expect(wrapper.get('button[aria-label="创建年级"]').text()).toContain('创建年级')
  })

  it('creates a root grade with an explicit kind and refreshes the catalog', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response({ id: 'grade-1', parentId: '', kind: 'grade', name: '高一', description: '', sortKey: 10, status: 'active', published: false }, 201))
      .mockResolvedValueOnce(response([{ id: 'grade-1', parentId: '', kind: 'grade', name: '高一', description: '', sortKey: 10, status: 'active', published: false }]))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('button[aria-label="创建年级"]').trigger('click')
    await wrapper.get('input[aria-label="目录名称"]').setValue('高一')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls[1][0]).toBe('/api/v1/admin/catalog/grade')
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[1][1]?.body))).toMatchObject({ name: '高一', parentId: '' })
    expect(wrapper.text()).toContain('高一')
  })

  it('shows a retryable API error with its support request ID', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: 'internal_error', message: '服务暂不可用', requestId: 'catalog-req-1' } }), { status: 500 }))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('catalog-req-1')
    vi.mocked(fetch).mockResolvedValueOnce(response([]))
    await wrapper.get('button[aria-label="重试加载教学目录"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('还没有教学目录')
  })

  it('does not load or expose administration controls to students', async () => {
    const wrapper = mountView('student')
    await flushPromises()
    expect(wrapper.text()).toContain('无权访问教学管理')
    expect(wrapper.find('button[aria-label="创建年级"]').exists()).toBe(false)
    expect(fetch).not.toHaveBeenCalled()
  })
})
