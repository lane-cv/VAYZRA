import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import LearningView from './LearningView.vue'

function response(data: unknown) { return new Response(JSON.stringify({ data })) }
const grade = { id: 'g1', kind: 'grade', name: '高一', description: '', sortKey: 10 }

describe('LearningView', () => {
  beforeEach(() => { vi.useFakeTimers(); window.history.replaceState({}, '', '/student/learning'); vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response([grade]))) })
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

  it('loads accessible top filters and shows an authorized empty path', async () => {
    const wrapper = mount(LearningView)
    await flushPromises()
    expect(wrapper.get('select[aria-label="年级筛选"]').text()).toContain('高一')
    vi.mocked(fetch).mockResolvedValueOnce(response([]))
    await wrapper.get('select[aria-label="年级筛选"]').setValue('g1')
    await flushPromises()
    expect(vi.mocked(fetch).mock.calls[1][0]).toContain('kind=term')
    expect(wrapper.text()).toContain('这个年级暂时没有可学习的课程')
    expect(window.location.search).toBe('?gradeId=g1')
  })

  it('ignores stale catalog responses after the filter changes', async () => {
    let resolveOld!: (value: Response) => void
    const oldResponse = new Promise<Response>((resolve) => { resolveOld = resolve })
    vi.mocked(fetch).mockResolvedValueOnce(response([grade])).mockReturnValueOnce(oldResponse).mockResolvedValueOnce(response([]))
    const wrapper = mount(LearningView)
    await flushPromises()
    await wrapper.get('select[aria-label="年级筛选"]').setValue('g1')
    await wrapper.get('select[aria-label="年级筛选"]').setValue('')
    resolveOld(response([{ id: 't-old', kind: 'term', name: '旧学期', sortKey: 1 }]))
    await flushPromises()
    expect(wrapper.text()).not.toContain('旧学期')
  })

  it('debounces search and highlights matches without interpreting snippets as HTML', async () => {
    const wrapper = mount(LearningView)
    await flushPromises()
    vi.mocked(fetch).mockResolvedValueOnce(response([{ lessonId: 'l1', revisionId: 'r1', title: '力与运动', summary: '', snippet: '<img onerror=alert(1)> 力学', gradeId: 'g1', gradeName: '高一', termId: 't1', termName: '上学期', subjectId: 's1', subjectName: '物理', chapterId: 'c1', chapterName: '运动', revisionStatus: 'published' }]))
    await wrapper.get('input[aria-label="搜索课程"]').setValue('力学')
    await vi.advanceTimersByTimeAsync(249)
    expect(fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(2)
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('mark').text()).toBe('力学')
  })

  it('ignores a stale search response after the query changes', async () => {
    let resolveOld!: (value: Response) => void
    const oldResponse = new Promise<Response>((resolve) => { resolveOld = resolve })
    const wrapper = mount(LearningView)
    await flushPromises()
    vi.mocked(fetch).mockReturnValueOnce(oldResponse).mockResolvedValueOnce(response([{ lessonId: 'new', revisionId: 'r-new', title: '新结果', summary: '', snippet: '新结果', gradeId: 'g1', termId: 't1', subjectId: 's1', chapterId: 'c1' }]))
    await wrapper.get('input[aria-label="搜索课程"]').setValue('旧词')
    await vi.advanceTimersByTimeAsync(250)
    await wrapper.get('input[aria-label="搜索课程"]').setValue('新词')
    await vi.advanceTimersByTimeAsync(250); await flushPromises()
    resolveOld(response([{ lessonId: 'old', revisionId: 'r-old', title: '旧结果', summary: '', snippet: '旧结果', gradeId: 'g1', termId: 't1', subjectId: 's1', chapterId: 'c1' }]))
    await flushPromises()
    expect(wrapper.text()).toContain('新结果')
    expect(wrapper.text()).not.toContain('旧结果')
  })

  it('keeps search results inside the selected chapter', async () => {
    window.history.replaceState({}, '', '/student/learning?gradeId=g1&termId=t1&subjectId=s1&chapterId=c1')
    vi.mocked(fetch).mockReset()
      .mockResolvedValueOnce(response([grade]))
      .mockResolvedValueOnce(response([{ id: 't1', kind: 'term', name: '上学期', sortKey: 1 }]))
      .mockResolvedValueOnce(response([{ id: 's1', kind: 'subject', name: '物理', sortKey: 1 }]))
      .mockResolvedValueOnce(response([{ id: 'c1', kind: 'chapter', name: '力学', sortKey: 1 }]))
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([
        { lessonId: 'inside', revisionId: 'r1', title: '本章课程', summary: '', snippet: '运动', gradeId: 'g1', termId: 't1', subjectId: 's1', chapterId: 'c1' },
        { lessonId: 'outside', revisionId: 'r2', title: '别章课程', summary: '', snippet: '运动', gradeId: 'g1', termId: 't1', subjectId: 's1', chapterId: 'c2' },
      ]))
    const wrapper = mount(LearningView)
    await flushPromises()
    await wrapper.get('input[aria-label="搜索课程"]').setValue('运动')
    await vi.advanceTimersByTimeAsync(250); await flushPromises()
    expect(wrapper.text()).toContain('本章课程')
    expect(wrapper.text()).not.toContain('别章课程')
  })

  it('opens the mobile course drawer and restores focus on Escape', async () => {
    const wrapper = mount(LearningView, { attachTo: document.body })
    await flushPromises()
    const trigger = wrapper.get('button[aria-label="打开课程目录"]')
    await trigger.trigger('click'); await flushPromises()
    expect(wrapper.get('nav[aria-label="课程目录"]').attributes('aria-modal')).toBe('true')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises()
    expect(document.activeElement).toBe(trigger.element)
  })
})
