import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { routerKey } from 'vue-router'
const api = vi.hoisted(() => ({
  createTeacher: vi.fn(),
  createAI: vi.fn(),
  key: vi.fn(() => '11111111-1111-4111-8111-111111111111'),
}))
const upload = vi.hoisted(() => ({ hasState: false, clear: vi.fn() }))
vi.mock('./studentApi', () => ({ createQuestion: api.createTeacher, newIdempotencyKey: api.key }))
vi.mock('../ai/studentApi', () => ({ createAIThread: api.createAI, newAIIdempotencyKey: api.key }))
vi.mock('./QuestionAttachmentUploader.vue', () => ({
  default: {
    props: ['purpose'],
    emits: ['update:attachments', 'pending-change', 'state-change'],
    methods: { clear() { upload.clear() } },
    template: '<div data-testid="uploader" :data-purpose="purpose" />',
  },
}))
import NewQuestionView from './NewQuestionView.vue'

describe('NewQuestionView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    upload.hasState = false
    vi.stubGlobal('confirm', vi.fn(() => true))
  })

  function mountComposer() {
    const replace = vi.fn()
    const wrapper = mount(NewQuestionView, {
      props: { userId: 'u1' },
      global: {
        provide: { [routerKey as symbol]: { replace } },
        stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } },
      },
    })
    return { wrapper, replace }
  }

  it('requires a channel, and AI additionally requires math or physics without exposing a model selector', async () => {
    const { wrapper } = mountComposer()
    await wrapper.get('[aria-label="问题标题"]').setValue('函数题')
    await wrapper.get('[aria-label="问题描述"]').setValue('请讲解')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role=alert]').text()).toContain('答疑方式')

    await wrapper.get('input[value="ai"]').setValue()
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role=alert]').text()).toContain('学科')
    expect(wrapper.find('[aria-label*="模型"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('选择模型')

    await wrapper.get('input[value="math"]').setValue()
    expect(wrapper.get('[data-testid="uploader"]').attributes('data-purpose')).toBe('ai')
  })

  it('submits AI with subject and shared fields, reuses duplicate intent key, then navigates canonically', async () => {
    let resolve!: (value: unknown) => void
    api.createAI.mockImplementation(() => new Promise((done) => { resolve = done }))
    const { wrapper, replace } = mountComposer()
    await wrapper.get('input[value="ai"]').setValue()
    await wrapper.get('input[value="physics"]').setValue()
    await wrapper.get('[aria-label="问题标题"]').setValue('受力题')
    await wrapper.get('[aria-label="问题描述"]').setValue('请分析')
    wrapper.getComponent({ name: 'QuestionAttachmentUploader' }).vm.$emit('update:attachments', [{ fileVersionId: 'ai-file', sortPosition: 0 }])

    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(api.createAI).toHaveBeenCalledTimes(1)
    expect(api.createAI).toHaveBeenCalledWith({
      title: '受力题',
      subject: 'physics',
      body: '请分析',
      attachments: [{ fileVersionId: 'ai-file', sortPosition: 0 }],
    }, '11111111-1111-4111-8111-111111111111')

    resolve({ thread: { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa' }, run: { id: 'r1' }, eventsUrl: '/events' })
    await flushPromises()
    expect(replace).toHaveBeenCalledWith('/student/questions/ai/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')
  })

  it('uses the isolated teacher write path without a subject and navigates canonically', async () => {
    api.createTeacher.mockResolvedValue({ thread: { id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb' }, messages: [] })
    const { wrapper, replace } = mountComposer()
    await wrapper.get('input[value="teacher"]').setValue()
    await wrapper.get('[aria-label="问题标题"]').setValue('函数题')
    await wrapper.get('[aria-label="问题描述"]').setValue('请老师讲解')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(api.createTeacher).toHaveBeenCalledWith({
      title: '函数题',
      body: '请老师讲解',
      attachments: [],
    }, '11111111-1111-4111-8111-111111111111')
    expect(api.createTeacher.mock.calls[0][0]).not.toHaveProperty('subject')
    expect(api.createAI).not.toHaveBeenCalled()
    expect(replace).toHaveBeenCalledWith('/student/questions/teacher/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb')
  })

  it('warns before discarding incompatible pending AI upload state when changing channel', async () => {
    const { wrapper } = mountComposer()
    await wrapper.get('input[value="ai"]').setValue()
    wrapper.getComponent({ name: 'QuestionAttachmentUploader' }).vm.$emit('state-change', true)
    vi.mocked(confirm).mockReturnValueOnce(false)
    await wrapper.get('input[value="teacher"]').setValue()
    expect((wrapper.get('input[value="ai"]').element as HTMLInputElement).checked).toBe(true)
    expect(upload.clear).not.toHaveBeenCalled()

    vi.mocked(confirm).mockReturnValueOnce(true)
    await wrapper.get('input[value="teacher"]').setValue()
    expect((wrapper.get('input[value="teacher"]').element as HTMLInputElement).checked).toBe(true)
    expect(upload.clear).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-testid="uploader"]').attributes('data-purpose')).toBe('teacher')
  })

  it('reuses the idempotency key after an uncertain teacher failure and rotates it only when payload changes', async () => {
    api.key.mockReturnValueOnce('key-one').mockReturnValueOnce('key-two')
    api.createTeacher.mockRejectedValueOnce(new Error('网络连接异常')).mockRejectedValueOnce(new Error('网络连接异常')).mockResolvedValueOnce({ thread: { id: 'q2' }, messages: [] })
    const { wrapper } = mountComposer()
    await wrapper.get('input[value="teacher"]').setValue()
    await wrapper.get('[aria-label="问题标题"]').setValue('函数题'); await wrapper.get('[aria-label="问题描述"]').setValue('请老师讲解')
    await wrapper.get('form').trigger('submit'); await flushPromises(); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.createTeacher.mock.calls.slice(0, 2).map((call) => call[1])).toEqual(['key-one', 'key-one'])
    await wrapper.get('[aria-label="问题描述"]').setValue('修改后的描述'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.createTeacher.mock.calls[2][1]).toBe('key-two')
  })
})
