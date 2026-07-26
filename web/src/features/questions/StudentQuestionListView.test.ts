import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { routeLocationKey, routerKey } from 'vue-router'

const api = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('./summaryApi', () => ({
  listQuestionSummaries: api.list,
  parseSummaryChannel: (value: unknown) => value === 'ai' || value === 'teacher' ? value : '',
}))
import StudentQuestionListView from './StudentQuestionListView.vue'

const mixed = [
  { id: 'a1', channel: 'ai', title: '函数题', rawStatus: 'streaming', lastMessageAt: '2026-07-26T08:00:00Z', createdAt: '2026-07-26T07:00:00Z' },
  { id: 't1', channel: 'teacher', title: '受力分析', rawStatus: 'waiting_student', lastMessageAt: '2026-07-26T08:00:00Z', createdAt: '2026-07-26T06:00:00Z' },
]

function mountList(query: Record<string, unknown> = {}) {
  const route = reactive({ query })
  const replace = vi.fn(async (target: { query: Record<string, unknown> }) => {
    route.query = target.query
  })
  const wrapper = mount(StudentQuestionListView, {
    global: {
      provide: {
        [routeLocationKey as symbol]: route,
        [routerKey as symbol]: { replace },
      },
      stubs: {
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
      },
    },
  })
  return { wrapper, route, replace }
}

describe('StudentQuestionListView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.list.mockReset()
    vi.useFakeTimers()
  })
  afterEach(() => vi.useRealTimers())

  it('renders the server-ordered mixed list with channel badges, mapped status and no body or note preview', async () => {
    api.list.mockResolvedValue({ items: mixed, nextCursor: undefined })
    const { wrapper } = mountList()
    await flushPromises()

    const entries = wrapper.findAll('li')
    expect(entries.map((entry) => entry.get('strong').text())).toEqual(['函数题', '受力分析'])
    expect(entries[0].text()).toContain('AI')
    expect(entries[0].text()).toContain('生成中')
    expect(entries[0].get('a').attributes('href')).toBe('/student/questions/ai/a1')
    expect(entries[1].text()).toContain('老师')
    expect(entries[1].text()).toContain('等待我回复')
    expect(entries[1].get('a').attributes('href')).toBe('/student/questions/teacher/t1')
    expect(wrapper.text()).not.toContain('消息正文')
    expect(wrapper.text()).not.toContain('老师备注')
  })

  it('stores only channel and title search in route query, debounces searches and aborts stale requests', async () => {
    let firstSignal: AbortSignal | undefined
    api.list
      .mockImplementationOnce((_filters, signal: AbortSignal) => {
        firstSignal = signal
        return new Promise(() => undefined)
      })
      .mockResolvedValueOnce({ items: mixed, nextCursor: undefined })
    const { wrapper, replace } = mountList({ channel: 'ai', search: '旧题', unexpected: 'student-data' })
    await flushPromises()
    expect(api.list).toHaveBeenCalledWith({ channel: 'ai', search: '旧题', limit: 20 }, expect.any(AbortSignal))

    await wrapper.get('[aria-label="搜索问题标题"]').setValue('函数')
    await vi.advanceTimersByTimeAsync(299)
    expect(api.list).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()

    expect(firstSignal?.aborted).toBe(true)
    expect(api.list).toHaveBeenLastCalledWith({ channel: 'ai', search: '函数', limit: 20 }, expect.any(AbortSignal))
    expect(replace).toHaveBeenLastCalledWith({ query: { channel: 'ai', search: '函数' } })
  })

  it('supports all, AI and teacher filters through canonical route query state', async () => {
    api.list.mockResolvedValue({ items: mixed, nextCursor: undefined })
    const { wrapper, replace } = mountList()
    await flushPromises()
    await wrapper.get('[aria-label="答疑类型"]').setValue('ai')
    await flushPromises()
    expect(replace).toHaveBeenLastCalledWith({ query: { channel: 'ai' } })
    expect(api.list).toHaveBeenLastCalledWith({ channel: 'ai', search: undefined, limit: 20 }, expect.any(AbortSignal))
    await wrapper.get('[aria-label="答疑类型"]').setValue('teacher')
    await flushPromises()
    expect(replace).toHaveBeenLastCalledWith({ query: { channel: 'teacher' } })
    await wrapper.get('[aria-label="答疑类型"]').setValue('')
    await flushPromises()
    expect(replace).toHaveBeenLastCalledWith({ query: {} })
  })

  it('shows loading, empty and retryable errors with the request id', async () => {
    api.list
      .mockRejectedValueOnce(Object.assign(new Error('暂不可用'), { requestId: 'req-list' }))
      .mockResolvedValueOnce({ items: [], nextCursor: undefined })
    const { wrapper } = mountList()
    expect(wrapper.text()).toContain('正在加载')
    await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('req-list')
    await wrapper.get('button[aria-label="重试加载问答"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('还没有')
  })

  it('appends stable cursor pages once, stores the opaque cursor in route query, and keeps links keyboard accessible', async () => {
    let resolveMore!: (value: unknown) => void
    api.list
      .mockResolvedValueOnce({ items: mixed, nextCursor: 'same-time:teacher:t1' })
      .mockImplementationOnce(() => new Promise((resolve) => { resolveMore = resolve }))
    const { wrapper, replace } = mountList()
    await flushPromises()
    const loadMore = wrapper.get('button[aria-label="加载更多问答"]')
    await loadMore.trigger('click')
    expect(wrapper.get('button[aria-label="加载更多问答"]').attributes('disabled')).toBeDefined()
    resolveMore({ items: [mixed[1], { id: 'a2', channel: 'ai', title: '圆锥曲线', rawStatus: 'succeeded', lastMessageAt: '2026-07-25T08:00:00Z', createdAt: '2026-07-25T07:00:00Z' }], nextCursor: undefined })
    await flushPromises()

    expect(api.list).toHaveBeenLastCalledWith({ channel: undefined, search: undefined, cursor: 'same-time:teacher:t1', limit: 20 }, expect.any(AbortSignal))
    expect(replace).toHaveBeenLastCalledWith({ query: { cursor: 'same-time:teacher:t1' } })
    expect(wrapper.findAll('li').map((entry) => entry.get('strong').text())).toEqual(['函数题', '受力分析', '圆锥曲线'])
    expect(wrapper.findAll('li a').every((link) => link.element.tagName === 'A')).toBe(true)
  })

  it('restores an opaque cursor from the route without persisting student data', async () => {
    api.list.mockResolvedValue({ items: [mixed[1]], nextCursor: undefined })
    mountList({ channel: 'teacher', cursor: 'same-time:teacher:t1', body: 'must-not-survive' })
    await flushPromises()
    expect(api.list).toHaveBeenCalledWith({
      channel: 'teacher',
      search: undefined,
      cursor: 'same-time:teacher:t1',
      limit: 20,
    }, expect.any(AbortSignal))
  })

  it.each([
    {
      name: 'channel',
      change: async (wrapper: ReturnType<typeof mountList>['wrapper']) => {
        await wrapper.get('[aria-label="答疑类型"]').setValue('ai')
      },
      expected: { channel: 'ai', search: undefined, limit: 20 },
    },
    {
      name: 'search',
      change: async (wrapper: ReturnType<typeof mountList>['wrapper']) => {
        await wrapper.get('[aria-label="搜索问题标题"]').setValue('圆锥')
        await vi.advanceTimersByTimeAsync(300)
      },
      expected: { channel: undefined, search: '圆锥', limit: 20 },
    },
  ])('clears stale rows when a $name replacement fails and retries page one without a cursor', async ({ change, expected }) => {
    const replacement = [{ ...mixed[0], title: '圆锥新题' }]
    api.list
      .mockResolvedValueOnce({ items: mixed, nextCursor: 'old-next' })
      .mockRejectedValueOnce(Object.assign(new Error('筛选暂不可用'), { requestId: 'req-replace' }))
      .mockResolvedValueOnce({ items: replacement, nextCursor: undefined })
    const { wrapper } = mountList()
    await flushPromises()
    expect(wrapper.text()).toContain('受力分析')

    await change(wrapper)
    await flushPromises()

    expect(wrapper.text()).not.toContain('函数题')
    expect(wrapper.text()).not.toContain('受力分析')
    expect(wrapper.get('[role=alert]').text()).toContain('req-replace')
    expect(wrapper.find('button[aria-label="重试加载更多问答"]').exists()).toBe(false)
    await wrapper.get('button[aria-label="重试加载问答"]').trigger('click')
    await flushPromises()

    expect(api.list).toHaveBeenLastCalledWith(expected, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('圆锥新题')
  })

  it('retains rows and the failed cursor for a load-more error, then retries the append exactly', async () => {
    const nextItem = { ...mixed[0], id: 'a2', title: '追加页' }
    api.list
      .mockResolvedValueOnce({ items: mixed, nextCursor: 'append-cursor' })
      .mockRejectedValueOnce(Object.assign(new Error('下一页暂不可用'), { requestId: 'req-more' }))
      .mockResolvedValueOnce({ items: [nextItem], nextCursor: undefined })
    const { wrapper } = mountList()
    await flushPromises()

    await wrapper.get('button[aria-label="加载更多问答"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('函数题')
    expect(wrapper.text()).toContain('受力分析')
    expect(wrapper.get('[role=alert]').text()).toContain('req-more')
    expect(wrapper.find('button[aria-label="重试加载问答"]').exists()).toBe(false)
    expect(wrapper.find('button[aria-label="加载更多问答"]').exists()).toBe(false)

    await wrapper.get('button[aria-label="重试加载更多问答"]').trigger('click')
    await flushPromises()
    expect(api.list).toHaveBeenLastCalledWith({
      channel: undefined,
      search: undefined,
      cursor: 'append-cursor',
      limit: 20,
    }, expect.any(AbortSignal))
    expect(wrapper.findAll('li').map((entry) => entry.get('strong').text())).toEqual(['函数题', '受力分析', '追加页'])
  })

  it('discards an aborted late load-more response after a newer replacement succeeds', async () => {
    let resolveLate!: (value: unknown) => void
    let lateSignal: AbortSignal | undefined
    const aiOnly = [{ ...mixed[0], title: '新的 AI 结果' }]
    api.list
      .mockResolvedValueOnce({ items: mixed, nextCursor: 'late-cursor' })
      .mockImplementationOnce((_filters, signal: AbortSignal) => {
        lateSignal = signal
        return new Promise((resolve) => { resolveLate = resolve })
      })
      .mockResolvedValueOnce({ items: aiOnly, nextCursor: undefined })
    const { wrapper } = mountList()
    await flushPromises()
    await wrapper.get('button[aria-label="加载更多问答"]').trigger('click')
    await wrapper.get('[aria-label="答疑类型"]').setValue('ai')
    await flushPromises()

    expect(lateSignal?.aborted).toBe(true)
    expect(wrapper.findAll('li').map((entry) => entry.get('strong').text())).toEqual(['新的 AI 结果'])
    resolveLate({ items: [{ ...mixed[1], id: 'late', title: '迟到旧结果' }], nextCursor: undefined })
    await flushPromises()
    expect(wrapper.findAll('li').map((entry) => entry.get('strong').text())).toEqual(['新的 AI 结果'])
  })

  it('invalidates an old request before awaiting a deferred replacement route commit', async () => {
    let resolveOld!: (value: unknown) => void
    let oldSignal: AbortSignal | undefined
    let releaseRoute!: () => void
    const aiOnly = [{ ...mixed[0], title: '路由提交后的 AI 结果' }]
    api.list
      .mockResolvedValueOnce({ items: mixed, nextCursor: 'old-cursor' })
      .mockImplementationOnce((_filters, signal: AbortSignal) => {
        oldSignal = signal
        return new Promise((resolve) => { resolveOld = resolve })
      })
      .mockResolvedValueOnce({ items: aiOnly, nextCursor: undefined })
    const { wrapper, route, replace } = mountList()
    await flushPromises()
    await wrapper.get('button[aria-label="加载更多问答"]').trigger('click')
    replace.mockImplementationOnce((target: { query: Record<string, unknown> }) => new Promise<void>((resolve) => {
      releaseRoute = () => {
        route.query = target.query
        resolve()
      }
    }))

    const changing = wrapper.get('[aria-label="答疑类型"]').setValue('ai')
    await wrapper.vm.$nextTick()
    expect(wrapper.findAll('li')).toHaveLength(0)
    expect(oldSignal?.aborted).toBe(true)

    resolveOld({ items: [{ ...mixed[1], id: 'old', title: '路由等待期间的旧结果' }], nextCursor: undefined })
    await flushPromises()
    expect(wrapper.text()).not.toContain('路由等待期间的旧结果')
    expect(api.list).toHaveBeenCalledTimes(2)

    releaseRoute()
    await changing
    await flushPromises()
    expect(api.list).toHaveBeenCalledTimes(3)
    expect(wrapper.findAll('li').map((entry) => entry.get('strong').text())).toEqual(['路由提交后的 AI 结果'])
  })
})
