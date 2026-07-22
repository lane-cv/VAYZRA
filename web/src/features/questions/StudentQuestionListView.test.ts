import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
const api = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('./studentApi', () => ({ listStudentQuestions: api.list }))
import StudentQuestionListView from './StudentQuestionListView.vue'

describe('StudentQuestionListView', () => {
  beforeEach(() => vi.clearAllMocks())
  it('shows loading, empty, retryable errors with request id, filters and cursor', async () => {
    api.list.mockRejectedValueOnce(Object.assign(new Error('暂不可用'), { requestId: 'req-list' })).mockResolvedValueOnce({ items: [{ id: 'q1', title: '函数题', status: 'pending', version: 1, lastMessageAt: '2026-07-22T00:00:00Z', createdAt: '', updatedAt: '' }], nextCursor: 'next' }).mockResolvedValueOnce({ items: [] })
    const wrapper = mount(StudentQuestionListView)
    expect(wrapper.text()).toContain('正在加载')
    await flushPromises(); expect(wrapper.get('[role=alert]').text()).toContain('req-list')
    await wrapper.get('button[aria-label="重试加载问答"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('函数题')
    await wrapper.get('select').setValue('completed'); await flushPromises()
    expect(api.list).toHaveBeenLastCalledWith({ status: 'completed', limit: 20 }, undefined, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('还没有')
  })
})
