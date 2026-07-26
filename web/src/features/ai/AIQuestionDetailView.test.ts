import { createPinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { APIError } from '../../api/client'

const api = vi.hoisted(() => ({
  get: vi.fn(),
  cancel: vi.fn(),
  retry: vi.fn(),
  add: vi.fn(),
  key: vi.fn(() => '11111111-1111-4111-8111-111111111111'),
}))
const stream = vi.hoisted(() => ({ subscribe: vi.fn(() => new Promise<void>(() => undefined)) }))
vi.mock('./studentApi', () => ({
  getAIThread: api.get,
  cancelAIRun: api.cancel,
  retryAIRun: api.retry,
  addAIMessage: api.add,
  newAIIdempotencyKey: api.key,
}))
vi.mock('./eventStream', () => ({ subscribeRun: stream.subscribe }))
vi.mock('../questions/QuestionAttachmentUploader.vue', () => ({
  default: {
    props: ['purpose'],
    emits: ['update:attachments', 'pending-change'],
    template: '<div data-testid="uploader" :data-purpose="purpose" />',
  },
}))
import AIQuestionDetailView from './AIQuestionDetailView.vue'

const detail = {
  thread: {
    id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
    title: '受力分析',
    subject: 'physics',
    lastMessageAt: '2026-07-27T00:00:00Z',
    createdAt: '2026-07-27T00:00:00Z',
  },
  messages: [
    { id: 'm1', role: 'student', body: '请分析', createdAt: '2026-07-27T00:00:00Z', attachments: [] },
  ],
  activeRun: { id: 'r1', status: 'streaming', attemptNo: 1, lastSequence: 4 },
}

function mountDetail() {
  return mount(AIQuestionDetailView, {
    props: { threadId: detail.thread.id, userId: 'u1' },
    global: {
      plugins: [createPinia()],
      stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } },
    },
  })
}

describe('AIQuestionDetailView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.get.mockResolvedValue(detail)
    vi.stubGlobal('confirm', vi.fn(() => true))
  })

  it('loads history, displays subject, and resumes the active run from its server sequence', async () => {
    let resolve!: (value: typeof detail) => void
    api.get.mockImplementationOnce(() => new Promise((done) => { resolve = done }))
    const wrapper = mountDetail()
    await nextTick()
    expect(wrapper.text()).toContain('正在加载')
    resolve(detail)
    await flushPromises()
    expect(wrapper.text()).toContain('受力分析')
    expect(wrapper.text()).toContain('物理')
    expect(wrapper.text()).toContain('请分析')
    expect(stream.subscribe).toHaveBeenCalledWith('r1', 4, expect.any(Object), expect.any(AbortSignal))
  })

  it('renders request-ID errors, focuses them, and retries a failed load', async () => {
    api.get.mockRejectedValueOnce(new APIError(404, 'not_found', '问题不存在', 'req-404'))
    const wrapper = mount(AIQuestionDetailView, {
      attachTo: document.body,
      props: { threadId: detail.thread.id, userId: 'u1' },
      global: {
        plugins: [createPinia()],
        stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } },
      },
    })
    await flushPromises()
    const alert = wrapper.get('[role="alert"]')
    expect(alert.text()).toContain('问题不存在')
    expect(alert.text()).toContain('req-404')
    expect(document.activeElement).toBe(alert.element)
    api.get.mockResolvedValueOnce(detail)
    await wrapper.get('[aria-label="重试加载 AI 问题"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('受力分析')
    wrapper.unmount()
  })

  it('aborts only the subscription on unmount and calls server cancellation only after confirmation', async () => {
    const wrapper = mountDetail()
    await flushPromises()
    const streamCalls = stream.subscribe.mock.calls as unknown as Array<[string, number, unknown, AbortSignal]>
    const signal = streamCalls[0][3]
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    expect(api.cancel).not.toHaveBeenCalled()

    api.cancel.mockResolvedValue({ ...detail.activeRun, status: 'cancelled' })
    const explicit = mountDetail()
    await flushPromises()
    await explicit.get('[aria-label="停止生成"]').trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalled()
    expect(api.cancel).toHaveBeenCalledOnce()
  })

  it('reuses a retry key for an unchanged failed intent and starts the new run', async () => {
    api.get.mockResolvedValue({ ...detail, activeRun: { ...detail.activeRun, status: 'failed', errorCode: 'PROVIDER_UNAVAILABLE' } })
    api.retry.mockRejectedValueOnce(new Error('网络连接异常')).mockResolvedValueOnce({
      run: { id: 'r2', status: 'queued', attemptNo: 2, lastSequence: 0 },
      eventsUrl: '/events/r2',
    })
    const wrapper = mountDetail()
    await flushPromises()
    await wrapper.get('[aria-label="重试生成"]').trigger('click')
    await flushPromises()
    await wrapper.get('[aria-label="重试生成"]').trigger('click')
    await flushPromises()
    const retryCalls = api.retry.mock.calls as unknown as Array<[string, string]>
    expect(retryCalls.map((call) => call[1])).toEqual([
      '11111111-1111-4111-8111-111111111111',
      '11111111-1111-4111-8111-111111111111',
    ])
    expect((stream.subscribe.mock.calls as unknown as Array<[string, number]>).some((call) => call[0] === 'r2' && call[1] === 0)).toBe(true)
  })

  it('blocks follow-up while an AI attachment is pending', async () => {
    const wrapper = mountDetail()
    await flushPromises()
    const uploader = wrapper.getComponent({ name: 'QuestionAttachmentUploader' })
    uploader.vm.$emit('pending-change', true)
    await wrapper.get('[aria-label="AI 追问内容"]').setValue('继续讲解')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('安全检查')
    expect(api.add).not.toHaveBeenCalled()
  })
})
