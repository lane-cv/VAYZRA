import { createPinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { APIError } from '../../api/client'
import type { AIThreadDetail } from './types'

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

const detail: AIThreadDetail = {
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
    api.get.mockReset().mockResolvedValue(detail)
    api.cancel.mockReset()
    api.retry.mockReset()
    api.add.mockReset()
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
    expect(stream.subscribe).toHaveBeenCalledWith('r1', 0, expect.any(Object), expect.any(AbortSignal))
    const callbacks = (stream.subscribe.mock.calls as unknown as Array<[string, number, { onEvent(event: unknown): void }]>)[0][2]
    callbacks.onEvent({ sequence: 1, kind: 'delta', delta: '已持久化' })
    callbacks.onEvent({ sequence: 2, kind: 'delta', delta: '的部分回答' })
    await nextTick()
    expect(wrapper.get('[data-testid="streaming-answer"]').text()).toBe('已持久化的部分回答')
  })

  it('preserves matching in-memory partial text and resumes it on route re-entry', async () => {
    const pinia = createPinia()
    const first = mount(AIQuestionDetailView, {
      props: { threadId: detail.thread.id, userId: 'u1' },
      global: { plugins: [pinia], stubs: { RouterLink: true } },
    })
    await flushPromises()
    const firstCallbacks = (stream.subscribe.mock.calls as unknown as Array<[string, number, { onEvent(event: unknown): void }]>)[0][2]
    for (let sequence = 1; sequence <= 4; sequence += 1) {
      firstCallbacks.onEvent({ sequence, kind: 'delta', delta: String(sequence) })
    }
    await nextTick()
    expect(first.get('[data-testid="streaming-answer"]').text()).toBe('1234')
    first.unmount()

    const second = mount(AIQuestionDetailView, {
      props: { threadId: detail.thread.id, userId: 'u1' },
      global: { plugins: [pinia], stubs: { RouterLink: true } },
    })
    await flushPromises()
    expect(second.get('[data-testid="streaming-answer"]').text()).toBe('1234')
    const calls = stream.subscribe.mock.calls as unknown as Array<[string, number]>
    expect(calls[calls.length - 1][1]).toBe(4)
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
    api.get.mockResolvedValue({
      ...detail,
      activeRun: {
        ...detail.activeRun,
        status: 'failed',
        errorCode: 'PROVIDER_UNAVAILABLE',
        usage: { inputTokens: 8, outputTokens: 3, costMicroUSD: '19', source: 'estimated' },
      },
    })
    api.retry.mockRejectedValueOnce(new Error('网络连接异常')).mockResolvedValueOnce({
      run: { id: 'r2', status: 'queued', attemptNo: 2, lastSequence: 0 },
      eventsUrl: '/events/r2',
    })
    const wrapper = mountDetail()
    await flushPromises()
    expect(wrapper.text()).toContain('本次用量：输入 8，输出 3 tokens')
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

  it('keeps a completed partial answer visible when authoritative refresh fails, then retries and replaces it', async () => {
    api.add.mockResolvedValue({
      run: { id: 'r2', status: 'queued', attemptNo: 2, lastSequence: 0 },
      message: {
        id: 'm3',
        role: 'student',
        body: '确认后追问',
        createdAt: '2026-07-27T00:02:00Z',
        attachments: [],
      },
      eventsUrl: '/events/r2',
    })
    api.get
      .mockResolvedValueOnce(detail)
      .mockRejectedValueOnce(new APIError(503, 'request_failed', '暂时无法确认完整回答', 'req-refresh'))
      .mockResolvedValueOnce({
        ...detail,
        messages: [
          ...detail.messages,
          {
            id: 'm2',
            role: 'assistant',
            body: '**权威完整回答**',
            runId: 'r1',
            createdAt: '2026-07-27T00:01:00Z',
            attachments: [],
          },
        ],
        activeRun: { ...detail.activeRun, status: 'succeeded', lastSequence: 2 },
      })
    const wrapper = mountDetail()
    await flushPromises()
    await wrapper.get('[aria-label="AI 追问内容"]').setValue('确认后追问')
    const callbacks = (stream.subscribe.mock.calls as unknown as Array<
      [string, number, { onEvent(event: unknown): void }]
    >)[0][2]

    callbacks.onEvent({ sequence: 1, kind: 'delta', delta: '完整但尚未确认的回答' })
    callbacks.onEvent({ sequence: 2, kind: 'status', status: 'succeeded' })
    await flushPromises()

    expect(wrapper.get('[data-testid="streaming-answer"]').text()).toBe('完整但尚未确认的回答')
    const refreshAlert = wrapper.get('[data-testid="answer-refresh-error"]')
    expect(refreshAlert.text()).toContain('暂时无法确认完整回答')
    expect(refreshAlert.text()).toContain('req-refresh')
    await wrapper.get('form').trigger('submit')
    expect(api.add).not.toHaveBeenCalled()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[aria-label="重试确认完整回答"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="streaming-answer"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('权威完整回答')
    expect(wrapper.find('[data-testid="answer-refresh-error"]').exists()).toBe(false)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.add).toHaveBeenCalledOnce()
    expect(api.get).toHaveBeenLastCalledWith(
      detail.thread.id,
      { limit: 100 },
      expect.any(AbortSignal),
    )
  })

  it('blocks follow-up while authoritative refresh is still in flight', async () => {
    let resolveRefresh!: (value: AIThreadDetail) => void
    api.get
      .mockResolvedValueOnce(detail)
      .mockImplementationOnce(() => new Promise((resolve) => { resolveRefresh = resolve }))
    const wrapper = mountDetail()
    await flushPromises()
    await wrapper.get('[aria-label="AI 追问内容"]').setValue('等待确认的追问')
    const callbacks = (stream.subscribe.mock.calls as unknown as Array<
      [string, number, { onEvent(event: unknown): void }]
    >)[0][2]

    callbacks.onEvent({ sequence: 1, kind: 'delta', delta: '待确认回答' })
    callbacks.onEvent({ sequence: 2, kind: 'status', status: 'succeeded' })
    await nextTick()

    expect(wrapper.text()).toContain('正在确认完整回答')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(api.add).not.toHaveBeenCalled()

    resolveRefresh({
      ...detail,
      messages: [
        ...detail.messages,
        {
          id: 'm2',
          role: 'assistant',
          body: '权威回答',
          runId: 'r1',
          createdAt: '2026-07-27T00:01:00Z',
          attachments: [],
        },
      ],
      activeRun: { ...detail.activeRun!, status: 'succeeded', lastSequence: 2 },
    })
    await flushPromises()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
  })

  it('loads every message page after refresh with stable de-duplication so the newest follow-up remains visible', async () => {
    const firstTwenty = Array.from({ length: 20 }, (_, index) => ({
      id: `m${index + 1}`,
      role: index % 2 === 0 ? 'student' as const : 'assistant' as const,
      body: `历史消息 ${index + 1}`,
      createdAt: `2026-07-27T00:00:${String(index).padStart(2, '0')}Z`,
      attachments: [],
    }))
    api.get
      .mockResolvedValueOnce({ ...detail, messages: firstTwenty, nextMessageCursor: 'cursor-20' })
      .mockResolvedValueOnce({
        ...detail,
        messages: [
          firstTwenty[19],
          {
            id: 'm21',
            role: 'student',
            body: '最新追问',
            createdAt: '2026-07-27T00:01:00Z',
            attachments: [],
          },
          {
            id: 'm22',
            role: 'assistant',
            body: '最新回答',
            runId: 'r1',
            createdAt: '2026-07-27T00:01:01Z',
            attachments: [],
          },
        ],
        nextMessageCursor: undefined,
      })

    const wrapper = mountDetail()
    await flushPromises()

    expect(api.get.mock.calls.slice(0, 2).map((call) => call[1])).toEqual([
      { limit: 100 },
      { cursor: 'cursor-20', limit: 100 },
    ])
    expect(wrapper.findAll('.timeline > li:not(.sr-only)')).toHaveLength(22)
    expect(wrapper.text()).toContain('最新追问')
    expect(wrapper.text()).toContain('最新回答')
    expect(wrapper.text().match(/历史消息 20/g)).toHaveLength(1)
  })

  it('reports a later message-page failure with its request ID and retries the complete read', async () => {
    const first = { ...detail, nextMessageCursor: 'cursor-next' }
    api.get
      .mockResolvedValueOnce(first)
      .mockRejectedValueOnce(new APIError(503, 'request_failed', '加载后续消息失败', 'req-page'))
      .mockResolvedValueOnce(first)
      .mockResolvedValueOnce({
        ...detail,
        messages: [
          ...detail.messages,
          { id: 'latest', role: 'student', body: '分页恢复后的追问', createdAt: '2026-07-27T00:02:00Z', attachments: [] },
        ],
        nextMessageCursor: undefined,
      })
    const wrapper = mountDetail()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('req-page')
    await wrapper.get('[aria-label="重试加载 AI 问题"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('分页恢复后的追问')
    expect(api.get.mock.calls.slice(-2).map((call) => call[1])).toEqual([
      { limit: 100 },
      { cursor: 'cursor-next', limit: 100 },
    ])
  })

  it('aborts and ignores an old message page when the thread route changes', async () => {
    let resolveOldPage!: (value: AIThreadDetail) => void
    let oldPageSignal: AbortSignal | undefined
    const nextThread = {
      ...detail,
      thread: { ...detail.thread, id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', title: '新线程' },
      messages: [{ ...detail.messages[0], id: 'new-message', body: '新线程消息' }],
      activeRun: undefined,
    }
    api.get.mockImplementation((id: string, options: { cursor?: string }, signal: AbortSignal) => {
      if (id === detail.thread.id && !options.cursor) {
        return Promise.resolve({ ...detail, nextMessageCursor: 'old-next' })
      }
      if (id === detail.thread.id) {
        oldPageSignal = signal
        return new Promise((resolve) => { resolveOldPage = resolve })
      }
      return Promise.resolve(nextThread)
    })
    const wrapper = mountDetail()
    await flushPromises()

    await wrapper.setProps({ threadId: nextThread.thread.id })
    await flushPromises()
    expect(oldPageSignal?.aborted).toBe(true)
    resolveOldPage({
      ...detail,
      messages: [{ ...detail.messages[0], id: 'old-late', body: '旧线程迟到消息' }],
      nextMessageCursor: undefined,
    })
    await flushPromises()

    expect(wrapper.text()).toContain('新线程消息')
    expect(wrapper.text()).not.toContain('旧线程迟到消息')
  })
})
