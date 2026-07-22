import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { routerKey } from 'vue-router'
const api = vi.hoisted(() => ({ create: vi.fn(), key: vi.fn(() => '11111111-1111-4111-8111-111111111111') }))
vi.mock('./studentApi', () => ({ createQuestion: api.create, newIdempotencyKey: api.key }))
vi.mock('./QuestionAttachmentUploader.vue', () => ({ default: { template: '<div />' } }))
import NewQuestionView from './NewQuestionView.vue'

describe('NewQuestionView', () => {
  beforeEach(() => vi.clearAllMocks())
  it('validates, prevents duplicate submits, and replaces navigation from server detail', async () => {
    let resolve!: (value: unknown) => void
    api.create.mockImplementation(() => new Promise((done) => { resolve = done }))
    const replace = vi.fn(); const wrapper = mount(NewQuestionView, { props: { userId: 'u1' }, global: { provide: { [routerKey as symbol]: { replace } } } })
    await wrapper.get('form').trigger('submit'); expect(wrapper.get('[role=alert]').text()).toContain('标题')
    await wrapper.get('[aria-label="问题标题"]').setValue('函数题'); await wrapper.get('[aria-label="问题描述"]').setValue('请老师讲解')
    await wrapper.get('form').trigger('submit'); await wrapper.get('form').trigger('submit')
    expect(api.create).toHaveBeenCalledTimes(1)
    resolve({ thread: { id: 'q1' }, messages: [] }); await flushPromises()
    expect(replace).toHaveBeenCalledWith('/student/questions/q1')
  })
  it('reuses the idempotency key after an uncertain failure and rotates it only when payload changes', async () => {
    api.key.mockReturnValueOnce('key-one').mockReturnValueOnce('key-two')
    api.create.mockRejectedValueOnce(new Error('网络连接异常')).mockRejectedValueOnce(new Error('网络连接异常')).mockResolvedValueOnce({ thread: { id: 'q2' }, messages: [] })
    const wrapper = mount(NewQuestionView, { props: { userId: 'u1' }, global: { provide: { [routerKey as symbol]: { replace: vi.fn() } } } })
    await wrapper.get('[aria-label="问题标题"]').setValue('函数题'); await wrapper.get('[aria-label="问题描述"]').setValue('请老师讲解')
    await wrapper.get('form').trigger('submit'); await flushPromises(); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.create.mock.calls.slice(0, 2).map((call) => call[1])).toEqual(['key-one', 'key-one'])
    await wrapper.get('[aria-label="问题描述"]').setValue('修改后的描述'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.create.mock.calls[2][1]).toBe('key-two')
  })
})
