import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import StudentHomeView from './StudentHomeView.vue'

function mountHome() {
  return mount(StudentHomeView, { global: { stubs: { RouterLink: RouterLinkStub } } })
}

describe('StudentHomeView', () => {
  beforeEach(() => vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ data: [] })))) )
  afterEach(() => vi.unstubAllGlobals())
  it('links directly to the learning space', () => {
    const wrapper = mountHome()
    const link = wrapper.findAllComponents(RouterLinkStub).find((item) => item.text().includes('开始学习'))
    expect(link?.props('to')).toBe('/student/learning')
  })
  it('uses client-side router links for every static dashboard destination', () => {
    const wrapper = mountHome()
    const destinations = wrapper.findAllComponents(RouterLinkStub).map((link) => link.props('to'))
    expect(destinations).toEqual([
      '/student/learning',
      '/student/learning',
      '/student/questions',
    ])
  })
  it('shows the most recent authorized lesson', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: [{ lessonId: 'l1', revisionId: 'r1', title: '力与运动', summary: '', snippet: '', gradeId: 'g1', gradeName: '高一', termId: 't1', termName: '上学期', subjectId: 's1', subjectName: '物理', chapterId: 'c1', chapterName: '运动', revisionStatus: 'published', position: { viewed: true, anchor: '', scrollRatio: 0.4, observedAt: '2026-07-21T00:00:00Z' } }] })))
    const wrapper = mountHome()
    await flushPromises()
    const recent = wrapper.findAllComponents(RouterLinkStub).find((item) => item.props('to') === '/student/learning/l1')
    expect(recent?.text()).toContain('力与运动')
    expect(wrapper.text()).toContain('40%')
  })
})
