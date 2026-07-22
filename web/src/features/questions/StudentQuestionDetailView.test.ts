import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
const api = vi.hoisted(() => ({ get: vi.fn(), add: vi.fn(), key: vi.fn(() => '11111111-1111-4111-8111-111111111111') }))
const paging = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('./studentApi', () => ({ getStudentQuestion: api.get, addStudentMessage: api.add, newIdempotencyKey: api.key, listStudentMessages: paging.list }))
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
    expect(api.add).toHaveBeenCalledWith('q1',{body:'追问',attachments:[]},'11111111-1111-4111-8111-111111111111',3)
    expect(wrapper.text()).toContain('追问'); expect(wrapper.text()).not.toContain('教师备注')
  })
  it('reuses a follow-up key after response loss and ignores a late reply after route change', async () => {
    api.key.mockReturnValueOnce('reply-one').mockReturnValueOnce('reply-two')
    let resolveOld!: (value: unknown) => void
    api.add.mockRejectedValueOnce(new Error('网络连接异常')).mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    const q2 = { ...detail, thread: { ...detail.thread, id: 'q2', title: '新问题', status: 'pending' }, messages: [] }
    const wrapper = mount(StudentQuestionDetailView, { props: { questionId: 'q1', userId: 'u1' } }); await flushPromises()
    await wrapper.get('[aria-label="追问内容"]').setValue('同一追问'); await wrapper.get('form').trigger('submit'); await flushPromises(); await wrapper.get('form').trigger('submit')
    expect(api.add.mock.calls.slice(0, 2).map((call) => call[2])).toEqual(['reply-one', 'reply-one'])
    api.get.mockResolvedValueOnce(q2); await wrapper.setProps({ questionId: 'q2' }); await flushPromises()
    resolveOld({ ...detail, messages: [{ id: 'late', senderRole: 'student', kind: 'student_follow_up', body: '迟到回复', createdAt: '2026-07-22T02:00:00Z', attachments: [] }] }); await flushPromises()
    expect(wrapper.text()).toContain('新问题'); expect(wrapper.text()).not.toContain('迟到回复'); expect((wrapper.get('[aria-label="追问内容"]').element as HTMLTextAreaElement).value).toBe('')
  })
  it('ignores late detail and load-more results from the previous route', async () => {
    let resolveOldDetail!: (value: unknown) => void
    api.get.mockImplementationOnce(() => new Promise((resolve) => { resolveOldDetail = resolve })).mockResolvedValueOnce({ ...detail, thread: { ...detail.thread, id: 'q2', title: '新问题' }, messages: [] })
    const wrapper = mount(StudentQuestionDetailView, { props: { questionId: 'q1', userId: 'u1' } })
    await wrapper.setProps({ questionId: 'q2' }); await flushPromises(); resolveOldDetail(detail); await flushPromises()
    expect(wrapper.text()).toContain('新问题'); expect(wrapper.text()).not.toContain('函数题')
  })
  it('aborts and ignores a late message page when the route changes', async () => {
    let resolvePage!: (value: unknown) => void
    api.get.mockResolvedValueOnce({ ...detail, nextMessageCursor: 'old-cursor' }).mockResolvedValueOnce({ ...detail, thread: { ...detail.thread, id: 'q2', title: '新问题' }, messages: [] })
    paging.list.mockImplementationOnce((_id: string, _cursor: string, _limit: number, signal: AbortSignal) => new Promise((resolve) => { resolvePage = resolve; expect(signal.aborted).toBe(false) }))
    const wrapper = mount(StudentQuestionDetailView, { props: { questionId: 'q1', userId: 'u1' } }); await flushPromises()
    await wrapper.get('button').trigger('click'); await wrapper.setProps({ questionId: 'q2' }); await flushPromises()
    resolvePage({ items: [{ id: 'late-page', senderRole: 'admin', kind: 'admin_reply', body: '旧分页', createdAt: '2026-07-22T03:00:00Z', attachments: [] }] }); await flushPromises()
    expect(wrapper.text()).toContain('新问题'); expect(wrapper.text()).not.toContain('旧分页')
  })
})
