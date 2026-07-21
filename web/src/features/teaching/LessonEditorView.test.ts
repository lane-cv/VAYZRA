import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import LessonEditorView from './LessonEditorView.vue'

vi.mock('../students/api', () => ({ listStudents: vi.fn().mockResolvedValue({ data: [], nextCursor: null }) }))

const lessonId = '11111111-1111-4111-8111-111111111111'
const draft = {
  lessonId,
  chapterId: '22222222-2222-4222-8222-222222222222',
  title: '力与运动',
  summary: '基础课程',
  bodyMarkdown: '# 牛顿第一定律',
  sortKey: 10,
  lockVersion: 3,
  audience: { mode: 'all', userIds: [] },
  externalVideos: [],
  updatedAt: '2026-07-21T00:00:00Z',
} as const

function response(data: unknown, status = 200) {
  return new Response(JSON.stringify({ data }), { status })
}

function conflict() {
  return new Response(JSON.stringify({ error: { code: 'draft_conflict', message: '草稿已更新，请刷新后重试', requestId: 'req-conflict' } }), { status: 409 })
}

describe('LessonEditorView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    document.cookie = 'hl_csrf=csrf-value; path=/'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ id: lessonId, chapterId: draft.chapterId, status: 'draft', draft })))
  })
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

  it('debounces autosave for 800ms and renders a safe Markdown preview', async () => {
    const wrapper = mount(LessonEditorView, { props: { lessonId } })
    await flushPromises()
    expect(wrapper.get('[aria-label="课程正文预览"]').html()).toContain('牛顿第一定律')

    vi.mocked(fetch).mockResolvedValueOnce(response({ ...draft, title: '惯性', lockVersion: 4 }))
    await wrapper.get('input[aria-label="课程标题"]').setValue('惯性')
    expect(wrapper.text()).toContain('有未保存更改')
    await vi.advanceTimersByTimeAsync(799)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()

    expect(fetch).toHaveBeenCalledTimes(2)
    expect(vi.mocked(fetch).mock.calls[1][0]).toBe(`/api/v1/admin/lessons/${lessonId}/draft`)
    expect(new Headers(vi.mocked(fetch).mock.calls[1][1]?.headers).get('If-Match')).toBe('3')
    expect(wrapper.text()).toContain('已保存')
  })

  it('serializes saves and immediately persists edits made during a request', async () => {
    let resolveFirst: ((value: Response) => void) | undefined
    vi.mocked(fetch).mockImplementationOnce(async () => response({ id: lessonId, chapterId: draft.chapterId, status: 'draft', draft }))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValueOnce(response({ ...draft, title: '最终标题', lockVersion: 5 }))
    const wrapper = mount(LessonEditorView, { props: { lessonId } })
    await flushPromises()
    await wrapper.get('input[aria-label="课程标题"]').setValue('第一次')
    await vi.advanceTimersByTimeAsync(800)
    expect(fetch).toHaveBeenCalledTimes(2)
    await wrapper.get('input[aria-label="课程标题"]').setValue('最终标题')
    await vi.advanceTimersByTimeAsync(800)
    expect(fetch).toHaveBeenCalledTimes(2)

    resolveFirst?.(response({ ...draft, title: '第一次', lockVersion: 4 }))
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(new Headers(vi.mocked(fetch).mock.calls[2][1]?.headers).get('If-Match')).toBe('4')
    expect(JSON.parse(String(vi.mocked(fetch).mock.calls[2][1]?.body)).title).toBe('最终标题')
  })

  it('stops autosave after a conflict and offers a server reload', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(response({ id: lessonId, chapterId: draft.chapterId, status: 'draft', draft })).mockResolvedValueOnce(conflict())
    const wrapper = mount(LessonEditorView, { props: { lessonId } })
    await flushPromises()
    await wrapper.get('input[aria-label="课程标题"]').setValue('本地标题')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('草稿已在其他页面更新')

    await wrapper.get('textarea[aria-label="课程摘要"]').setValue('继续编辑')
    await vi.advanceTimersByTimeAsync(1600)
    expect(fetch).toHaveBeenCalledTimes(2)

    vi.mocked(fetch).mockResolvedValueOnce(response({ id: lessonId, chapterId: draft.chapterId, status: 'draft', draft: { ...draft, title: '服务器标题', lockVersion: 8 } }))
    await wrapper.get('button[aria-label="重新加载服务器草稿"]').trigger('click')
    await flushPromises()
    expect((wrapper.get('input[aria-label="课程标题"]').element as HTMLInputElement).value).toBe('服务器标题')
    expect(wrapper.text()).toContain('已保存')
  })

  it('warns only when local changes are unsaved and reloads when the lesson id changes', async () => {
    const wrapper = mount(LessonEditorView, { props: { lessonId } })
    await flushPromises()
    const cleanEvent = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(cleanEvent)
    expect(cleanEvent.defaultPrevented).toBe(false)

    await wrapper.get('input[aria-label="课程标题"]').setValue('本地修改')
    const dirtyEvent = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(dirtyEvent)
    expect(dirtyEvent.defaultPrevented).toBe(true)

    const nextId = '33333333-3333-4333-8333-333333333333'
    vi.mocked(fetch).mockResolvedValueOnce(response({ id: nextId, chapterId: draft.chapterId, status: 'draft', draft: { ...draft, lessonId: nextId, title: '第二课' } }))
    await wrapper.setProps({ lessonId: nextId })
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls[1][0]).toBe(`/api/v1/admin/lessons/${nextId}`)
    expect((wrapper.get('input[aria-label="课程标题"]').element as HTMLInputElement).value).toBe('第二课')
  })

  it('blocks incomplete publication and publishes a clean valid draft after confirmation', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(response({ id: lessonId, chapterId: draft.chapterId, status: 'draft', draft: { ...draft, bodyMarkdown: '' } }))
    const incomplete = mount(LessonEditorView, { props: { lessonId } })
    await flushPromises()
    expect(incomplete.get('button[aria-label="发布课程"]').attributes('disabled')).toBeDefined()
    incomplete.unmount()

    vi.mocked(fetch).mockResolvedValueOnce(response({ id: lessonId, chapterId: draft.chapterId, status: 'draft', draft }))
      .mockResolvedValueOnce(response({ id: 'rev-1', lessonId, version: 1, sourceDraftVersion: 3, title: draft.title, summary: draft.summary, bodyMarkdown: draft.bodyMarkdown, sortKey: 10, audience: draft.audience, externalVideos: [], publishedBy: 'u1', publishedAt: '2026-07-21T01:00:00Z' }, 201))
    const wrapper = mount(LessonEditorView, { props: { lessonId } })
    await flushPromises()
    await wrapper.get('button[aria-label="发布课程"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('目录、受众、正文、文件预览和外部视频')
    await wrapper.get('button[aria-label="确认发布课程"]').trigger('click')
    await flushPromises()
    const calls = vi.mocked(fetch).mock.calls
    expect(calls[calls.length - 1]?.[0]).toBe(`/api/v1/admin/lessons/${lessonId}/publish`)
    expect(wrapper.text()).toContain('发布成功')
  })
})
