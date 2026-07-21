import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import FileCenterView from './FileCenterView.vue'
import { useSessionStore } from '../../stores/session'

const fileId = '11111111-1111-4111-8111-111111111111'
const versionId = '22222222-2222-4222-8222-222222222222'
const lessonId = '33333333-3333-4333-8333-333333333333'
const item = { id: fileId, createdAt: '2026-07-21T08:00:00Z', referenceCount: 1, latest: { id: versionId, fileId, version: 2, displayName: '牛顿定律.pdf', declaredMime: 'application/pdf', detectedMime: 'application/pdf', size: 2048, processingState: 'failed', failureCategory: 'conversion_failed', previewState: 'failed', browserPlayable: false, createdAt: '2026-07-21T08:00:00Z' } }

function response(data: unknown, status = 200) { return new Response(JSON.stringify({ data }), { status }) }
function mountView() {
  const session = useSessionStore()
  session.setUser({ id: 'teacher', username: 'teacher', displayName: '张老师', role: 'admin', mustChangePassword: false })
  return mount(FileCenterView, { attachTo: document.body })
}

describe('FileCenterView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.cookie = 'hl_csrf=csrf-value; path=/'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ items: [item], nextCursor: 'next-1' })))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('filters with bounded cursor pagination and renders sanitized state', async () => {
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.text()).toContain('牛顿定律.pdf')
    expect(wrapper.text()).toContain('转换失败')
    await wrapper.get('[aria-label="文件名筛选"]').setValue('牛顿')
    await wrapper.get('[aria-label="处理状态筛选"]').setValue('failed')
    await wrapper.get('form[aria-label="文件筛选"]').trigger('submit')
    await flushPromises()
    expect(String(vi.mocked(fetch).mock.calls[1][0])).toContain('/api/v1/admin/files/?q=%E7%89%9B%E9%A1%BF&state=failed&limit=25')
    await wrapper.get('button[aria-label="下一页文件"]').trigger('click'); await flushPromises()
    expect(String(vi.mocked(fetch).mock.calls[2][0])).toContain('cursor=next-1')
  })

  it('shows exact draft and published references without storage keys', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(response({ items: [item] })).mockResolvedValueOnce(response({ id: fileId, createdAt: item.createdAt, versions: [item.latest], references: [{ kind: 'draft', lessonId, lessonTitle: '力与运动' }, { kind: 'published', lessonId, lessonTitle: '力与运动', revisionId: '44444444-4444-4444-8444-444444444444' }] }))
    const wrapper = mountView(); await flushPromises()
    await wrapper.get(`button[aria-label="查看 ${item.latest.displayName} 引用"]`).trigger('click'); await flushPromises()
    expect(wrapper.get('[aria-label="文件详情"]').text()).toContain('草稿 · 力与运动')
    expect(wrapper.get('[aria-label="文件详情"]').text()).toContain('已发布 · 力与运动')
    expect(wrapper.html()).not.toContain('objectKey')
  })

  it('confirms deletion and gives actionable file-in-use feedback', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(response({ items: [item] })).mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: 'file_in_use', message: '文件仍被课程引用', requestId: 'req-file-1' } }), { status: 409 })).mockResolvedValueOnce(response({ id: fileId, createdAt: item.createdAt, versions: [item.latest], references: [{ kind: 'draft', lessonId, lessonTitle: '力与运动' }] }))
    const wrapper = mountView(); await flushPromises()
    await wrapper.get(`button[aria-label="删除 ${item.latest.displayName}"]`).trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('确认删除')
    await wrapper.get('button[aria-label="确认删除文件"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('请先解除草稿和发布引用')
    expect(wrapper.get('[role="alert"]').text()).toContain('req-file-1')
    expect(wrapper.get('[aria-label="文件详情"]').text()).toContain('草稿 · 力与运动')
  })
})
