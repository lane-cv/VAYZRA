import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
const api = vi.hoisted(() => ({ get: vi.fn(), add: vi.fn(), key: vi.fn(() => '11111111-1111-4111-8111-111111111111') }))
vi.mock('./studentApi', () => ({ getStudentQuestion: api.get, addStudentMessage: api.add, newIdempotencyKey: api.key, listStudentMessages: vi.fn() }))
vi.mock('./QuestionAttachmentUploader.vue', () => ({ default: { template: '<div />' } }))
import StudentQuestionDetailView from './StudentQuestionDetailView.vue'
const detail = { thread: { id: 'q1', title: '函数题', status: 'completed', version: 3, lastMessageAt: '', createdAt: '', updatedAt: '' }, messages: [{ id: 'm1', senderRole: 'admin', kind: 'admin_reply', body: '答复', createdAt: '2026-07-22T00:00:00Z', attachments: [] }] }

describe('StudentQuestionDetailView', () => {
  beforeEach(() => { vi.clearAllMocks(); api.get.mockResolvedValue(detail) })
  it('loads timeline and allows completed follow-up using returned server detail', async () => {
    api.add.mockResolvedValue({ ...detail, thread: { ...detail.thread, status: 'pending' }, messages: [...detail.messages, { id: 'm2', senderRole: 'student', kind: 'student_follow_up', body: '追问', createdAt: '2026-07-22T01:00:00Z', attachments: [] }] })
    const wrapper = mount(StudentQuestionDetailView, { props: { questionId: 'q1', userId: 'u1' } }); await flushPromises()
    expect(wrapper.text()).toContain('已完成'); expect(wrapper.text()).toContain('仍可继续追问')
    await wrapper.get('[aria-label="追问内容"]').setValue('追问'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('追问'); expect(wrapper.text()).not.toContain('教师备注')
  })
})
